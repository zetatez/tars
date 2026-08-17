package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tars/internal/config"
	"tars/internal/secret"
)

func execTool() *Tool {
	return &Tool{
		Name:        "exec_command",
		Description: "执行系统命令（不经 shell，argv 数组）。可用于读取系统信息、管理服务、安装软件、修改配置等。需要 shell 特性（管道/重定向/通配符）时显式调用 sh -c。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "命令及参数数组，argv[0] 为命令名"},
				"cwd":  map[string]any{"type": "string", "description": "工作目录，默认 session cwd"},
			},
			"required": []string{"argv"},
		},
		PolicyAction: "exec",
		ResourceKey:  "argv",
		ParallelSafe: false,
		Execute:      runExec,
	}
}

func runExec(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
	argvAny, ok := args["argv"].([]any)
	if !ok || len(argvAny) == 0 {
		return nil, errors.New("argv required")
	}
	argv := make([]string, len(argvAny))
	for i, a := range argvAny {
		argv[i], _ = a.(string)
	}

	if hit, matched := matchDestructive(argv, sc.Cfg); hit {
		return Result{"rejected": true, "reason": "destructive command blocked: " + matched}, nil
	}
	for _, a := range argv {
		if secret.ContainsSecret(a) {
			return Result{"rejected": true, "reason": "credential detected in argv, use stdin/env instead"}, nil
		}
	}

	cwd := sc.Cwd
	if v, ok := args["cwd"].(string); ok && v != "" {
		cwd = v
	}

	timeout := sc.Cfg.Tools.Exec.Timeout.Duration
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx2, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, err
		}
	}

	maxOut := sc.Cfg.Tools.Exec.MaxOutput
	return Result{
		"exit":   exitCode,
		"stdout": truncate(stdout.String(), maxOut),
		"stderr": truncate(stderr.String(), maxOut),
	}, nil
}

func matchDestructive(argv []string, cfg *config.Config) (bool, string) {
	if len(argv) == 0 {
		return false, ""
	}
	base := filepath.Base(argv[0])
	for _, d := range cfg.SystemProt.DestructiveCommands {
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

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}
