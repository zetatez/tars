package permission

import (
	"testing"

	"tars/internal/config"
)

func testCfg() *config.Config {
	cfg := config.Default()
	cfg.SystemProt.SystemPaths = []string{"/etc", "/opt"}
	cfg.Permissions.Rules = []config.Rule{
		{Action: "exec", Resource: "*", Effect: "deny"},
		{Action: "exec", Resource: "grep *", Effect: "allow"},
		{Action: "read_file", Resource: "/home/agent/work/**", Effect: "allow"},
		{Action: "write_file", Resource: "*", Effect: "allow"},
		{Action: "edit_file", Resource: "*", Effect: "allow"},
		{Action: "apply_patch", Resource: "*", Effect: "allow"},
	}
	return cfg
}

func TestRelativePathBypass(t *testing.T) {
	e := New(testCfg())
	dec := e.EvaluateToolCall("write_file", "write_file", "path",
		map[string]any{"path": "../../../etc/x"}, "user", "/home/agent/work")
	if dec.Level != LevelSystem {
		t.Errorf("relative path bypass: expected LevelSystem, got %d", dec.Level)
	}
}

func TestGlobRecursive(t *testing.T) {
	e := New(testCfg())
	dec := e.EvaluateToolCall("read_file", "read_file", "path",
		map[string]any{"path": "/home/agent/work/a/b/c.txt"}, "user", "")
	if dec.Effect != EffectAllow {
		t.Errorf("recursive ** should match nested file: %+v", dec)
	}
	deny := e.EvaluateToolCall("read_file", "read_file", "path",
		map[string]any{"path": "/var/log/syslog"}, "user", "")
	if deny.Effect != EffectDeny {
		t.Errorf("outside workspace should be denied: %+v", deny)
	}
}

func TestExecRule(t *testing.T) {
	e := New(testCfg())
	allow := e.EvaluateToolCall("exec_command", "exec", "argv",
		map[string]any{"argv": []any{"grep", "-r", "foo", "/x"}}, "user", "")
	if allow.Effect != EffectAllow {
		t.Errorf("grep * should match: %+v", allow)
	}
	deny := e.EvaluateToolCall("exec_command", "exec", "argv",
		map[string]any{"argv": []any{"cat", "/etc/grep.conf"}}, "user", "")
	if deny.Effect != EffectDeny {
		t.Errorf("cat should not match grep rule: %+v", deny)
	}
}

func TestEditFileAction(t *testing.T) {
	e := New(testCfg())
	dec := e.EvaluateToolCall("edit_file", "edit_file", "path",
		map[string]any{"path": "/home/agent/work/a.txt"}, "user", "")
	if dec.Effect != EffectAllow {
		t.Errorf("edit_file should match its own rule: %+v", dec)
	}
}

func TestApplyPatchAction(t *testing.T) {
	e := New(testCfg())
	dec := e.EvaluatePatch("*** Begin Patch\n*** Update File: /home/agent/work/a.txt\n@@\n-old\n+new\n*** End Patch", "user", "")
	if dec.Effect != EffectAllow {
		t.Errorf("apply_patch should match its own rule: %+v", dec)
	}
}

func TestDestructiveBlocked(t *testing.T) {
	e := New(testCfg())
	dec := e.EvaluateToolCall("exec_command", "exec", "argv",
		map[string]any{"argv": []any{"rm", "-rf", "/"}}, "admin", "")
	if dec.Effect != EffectDeny || dec.Level != LevelDestructive {
		t.Errorf("destructive must be denied for any role: %+v", dec)
	}
}
