package permission

import (
	"path/filepath"
	"strings"

	"tars/internal/config"
)

const (
	LevelRead        = 0
	LevelWorkspace   = 1
	LevelSystem      = 2
	LevelPrivileged  = 3
	LevelDestructive = 4
)

const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
	EffectAsk   = "ask"
)

type Decision struct {
	Effect     string
	Level      int
	NeedBackup bool
	Reason     string
}

type Evaluator struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Evaluator {
	return &Evaluator{cfg: cfg}
}

func ResolvePath(cwd, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func (e *Evaluator) IsSystemPath(p string) bool {
	p = filepath.Clean(p)
	for _, d := range e.cfg.SystemProt.SystemPaths {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func (e *Evaluator) EvaluateToolCall(name, policyAction, resourceKey string, args map[string]any, role, cwd string) Decision {
	switch policyAction {
	case "exec":
		return e.evalExec(name, args, role, cwd)
	case "write_file", "edit_file":
		return e.evalWrite(policyAction, args, role, cwd)
	default:
		return e.evalRead(policyAction, resourceKey, args)
	}
}

func (e *Evaluator) evalExec(name string, args map[string]any, role, cwd string) Decision {
	argv := toStringSlice(args["argv"])
	if hit, matched := e.matchDestructive(argv); hit {
		return Decision{Effect: EffectDeny, Level: LevelDestructive, Reason: "destructive command blocked: " + matched}
	}
	level := LevelWorkspace
	if hit, _ := e.matchPrivileged(argv); hit {
		level = LevelPrivileged
	} else if c, _ := args["cwd"].(string); c != "" && e.IsSystemPath(ResolvePath(cwd, c)) {
		level = LevelSystem
	}
	full := strings.Join(argv, " ")
	effect := e.matchRule("exec", full, argv)
	if level >= LevelSystem {
		return e.guard(effect, level, role, full)
	}
	return Decision{Effect: effect, Level: level}
}

func (e *Evaluator) evalWrite(policyAction string, args map[string]any, role, cwd string) Decision {
	raw, _ := args["path"].(string)
	path := ResolvePath(cwd, raw)
	level := LevelWorkspace
	if e.IsSystemPath(path) {
		level = LevelSystem
	}
	effect := e.matchRule(policyAction, path, nil)
	if level == LevelSystem {
		return e.guard(effect, level, role, path)
	}
	return Decision{Effect: effect, Level: level}
}

func (e *Evaluator) evalRead(policyAction, resourceKey string, args map[string]any) Decision {
	resource := ""
	if resourceKey != "" {
		if v, ok := args[resourceKey].(string); ok {
			resource = v
		}
	}
	effect := e.matchRule(policyAction, resource, nil)
	return Decision{Effect: effect, Level: LevelRead}
}

func (e *Evaluator) guard(effect string, level int, role, resource string) Decision {
	if effect != EffectAllow {
		return Decision{Effect: EffectDeny, Level: level, Reason: "not in allowlist: " + resource}
	}
	if level == LevelSystem || level == LevelPrivileged {
		if e.cfg.SystemProt.AdminAuto && role == "admin" {
			return Decision{Effect: EffectAllow, Level: level, NeedBackup: level == LevelSystem}
		}
		if e.cfg.Approval.Enabled {
			return Decision{Effect: EffectAsk, Level: level, NeedBackup: level == LevelSystem}
		}
		return Decision{Effect: EffectDeny, Level: level, Reason: "system operation requires admin key"}
	}
	return Decision{Effect: EffectAllow, Level: level}
}

func normalizeAction(a string) string {
	if a == "edit_file" {
		return "write_file"
	}
	return a
}

func (e *Evaluator) matchRule(action, resource string, argv []string) string {
	effect := EffectDeny
	for _, r := range e.cfg.Permissions.Rules {
		if !matchAction(r.Action, action) {
			continue
		}
		if !matchResource(r.Resource, resource, argv) {
			continue
		}
		effect = r.Effect
	}
	return effect
}

func matchAction(ruleAction, action string) bool {
	if ruleAction == "*" {
		return true
	}
	return normalizeAction(ruleAction) == normalizeAction(action)
}

func matchResource(pattern, resource string, argv []string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if argv != nil {
		return matchExec(pattern, argv)
	}
	return matchGlob(pattern, resource)
}

func matchExec(pattern string, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	ptoks := strings.Fields(pattern)
	if len(ptoks) == 0 {
		return false
	}
	if ok, _ := filepath.Match(ptoks[0], argv[0]); !ok {
		return false
	}
	if len(ptoks) == 1 {
		return true
	}
	return matchTokenSeq(ptoks[1:], argv[1:])
}

func matchTokenSeq(toks, args []string) bool {
	if len(toks) == 0 {
		return true
	}
	if len(args) == 0 {
		return false
	}
	if ok, _ := filepath.Match(toks[0], args[0]); ok {
		if matchTokenSeq(toks[1:], args[1:]) {
			return true
		}
	}
	return matchTokenSeq(toks, args[1:])
}

func matchGlob(pattern, path string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegs(pp, sp []string) bool {
	if len(pp) == 0 {
		return len(sp) == 0
	}
	if pp[0] == "**" {
		for i := 0; i <= len(sp); i++ {
			if matchSegs(pp[1:], sp[i:]) {
				return true
			}
		}
		return false
	}
	if len(sp) == 0 {
		return false
	}
	ok, _ := filepath.Match(pp[0], sp[0])
	if !ok {
		return false
	}
	return matchSegs(pp[1:], sp[1:])
}

func (e *Evaluator) matchDestructive(argv []string) (bool, string) {
	if len(argv) == 0 {
		return false, ""
	}
	base := filepath.Base(argv[0])
	for _, d := range e.cfg.SystemProt.DestructiveCommands {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !strings.ContainsAny(d, " ") {
			if base == d {
				return true, d
			}
			continue
		}
		if tokenMatch(argv, strings.Fields(d)) {
			return true, d
		}
	}
	if base == "dd" {
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "of=/dev/") {
				return true, "dd of=/dev/*"
			}
		}
	}
	return false, ""
}

func tokenMatch(argv []string, pattern []string) bool {
	for i := 0; i+len(pattern) <= len(argv); i++ {
		match := true
		for j, p := range pattern {
			if argv[i+j] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (e *Evaluator) matchPrivileged(argv []string) (bool, string) {
	if len(argv) == 0 {
		return false, ""
	}
	base := filepath.Base(argv[0])
	for _, c := range e.cfg.SystemProt.PrivilegedCommands {
		if base == c {
			return true, c
		}
	}
	return false, ""
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, a := range arr {
		out[i], _ = a.(string)
	}
	return out
}
