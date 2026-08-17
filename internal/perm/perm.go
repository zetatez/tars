package perm

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

func (e *Evaluator) IsSystemPath(p string) bool {
	p = filepath.Clean(p)
	for _, d := range e.cfg.SystemProt.SystemPaths {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func (e *Evaluator) matchRule(action, resource string) string {
	effect := EffectDeny
	for _, r := range e.cfg.Permissions.Rules {
		if !matchAction(r.Action, action) {
			continue
		}
		if !matchResource(r.Resource, resource) {
			continue
		}
		effect = r.Effect
	}
	return effect
}

func matchAction(ruleAction, action string) bool {
	return ruleAction == "*" || ruleAction == action
}

func matchResource(pattern, resource string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if ok, _ := filepath.Match(pattern, resource); ok {
		return true
	}
	return strings.Contains(resource, pattern)
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

func (e *Evaluator) EvaluateExec(argv []string, role string) Decision {
	if hit, matched := e.matchDestructive(argv); hit {
		return Decision{Effect: EffectDeny, Level: LevelDestructive, Reason: "destructive command blocked: " + matched}
	}
	full := strings.Join(argv, " ")
	var level int
	if hit, _ := e.matchPrivileged(argv); hit {
		level = LevelPrivileged
	} else {
		level = LevelWorkspace
	}
	effect := e.matchRule("exec", full)
	if level >= LevelPrivileged {
		return e.guard(effect, level, role, full)
	}
	return Decision{Effect: effect, Level: level}
}

func (e *Evaluator) EvaluateWrite(path string, role string) Decision {
	if e.IsSystemPath(path) {
		effect := e.matchRule("write_file", path)
		return e.guard(effect, LevelSystem, role, path)
	}
	effect := e.matchRule("write_file", path)
	return Decision{Effect: effect, Level: LevelWorkspace}
}

func (e *Evaluator) EvaluateRead(action, resource string) Decision {
	effect := e.matchRule(action, resource)
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
		return Decision{Effect: EffectDeny, Level: level, Reason: "system operation requires admin key"}
	}
	return Decision{Effect: EffectAllow, Level: level}
}
