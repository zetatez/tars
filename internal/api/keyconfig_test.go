package api

import (
	"testing"
)

func TestKeyConfigTemplateComplete(t *testing.T) {
	tpl := keyConfigTemplate()

	agent, ok := tpl["agent"].(map[string]any)
	if !ok {
		t.Fatal("missing agent group")
	}
	for _, k := range []string{"system_prompt", "model", "temperature", "max_tokens", "max_tool_steps"} {
		f, ok := agent[k].(keyConfigField)
		if !ok {
			t.Fatalf("agent.%s missing or not a field", k)
		}
		if f.Description == "" {
			t.Fatalf("agent.%s missing description", k)
		}
	}
	pm, ok := tpl["prompt_mode"].(keyConfigField)
	if !ok || pm.Description == "" {
		t.Fatal("prompt_mode missing or no description")
	}
	perm, ok := tpl["permissions"].(map[string]any)
	if !ok {
		t.Fatal("missing permissions group")
	}
	if _, ok := perm["rules"].(keyConfigField); !ok {
		t.Fatal("permissions.rules missing")
	}
	quota, ok := tpl["quota"].(map[string]any)
	if !ok {
		t.Fatal("missing quota group")
	}
	if _, ok := quota["max_active_sessions"].(keyConfigField); !ok {
		t.Fatal("quota.max_active_sessions missing")
	}
	if _, ok := quota["max_concurrent_turns_per_key"].(keyConfigField); !ok {
		t.Fatal("quota.max_concurrent_turns_per_key missing")
	}
}

func TestFillTemplateValue(t *testing.T) {
	tpl := keyConfigTemplate()
	stored := map[string]any{
		"agent": map[string]any{
			"model": "deepseek-v4-flash",
		},
		"prompt_mode": "plan",
	}
	fillTemplateValue(tpl, stored)

	agent := tpl["agent"].(map[string]any)
	mf := agent["model"].(keyConfigField)
	if mf.Value != "deepseek-v4-flash" {
		t.Fatalf("agent.model value = %v", mf.Value)
	}
	// 未设置的字段保留 default 作为 value
	tf := agent["temperature"].(keyConfigField)
	if tf.Value != tf.Default {
		t.Fatalf("temperature value should equal default, got %v", tf.Value)
	}
	pm := tpl["prompt_mode"].(keyConfigField)
	if pm.Value != "plan" {
		t.Fatalf("prompt_mode value = %v", pm.Value)
	}
	// 未知字段不进模板
	fillTemplateValue(tpl, map[string]any{"hack": "x"})
	if _, ok := tpl["hack"]; ok {
		t.Fatal("unknown field leaked into template")
	}
}

func TestValidateKeyConfigKeys(t *testing.T) {
	tpl := keyConfigTemplate()
	// 合法
	valid := map[string]any{
		"agent":       map[string]any{"model": "x", "temperature": 0.7},
		"prompt_mode": "plan",
		"quota":       map[string]any{"max_active_sessions": float64(10)},
	}
	if err := validateKeyConfigKeys(valid, tpl, ""); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	// 未知字段拒绝
	bad := map[string]any{"agent": map[string]any{"evil": "x"}}
	if err := validateKeyConfigKeys(bad, tpl, ""); err == nil {
		t.Fatal("unknown field accepted")
	}
	// 类型错误拒绝
	badType := map[string]any{"prompt_mode": 42}
	if err := validateKeyConfigKeys(badType, tpl, ""); err == nil {
		t.Fatal("wrong type accepted")
	}
}

func TestMergeMapsDeep(t *testing.T) {
	a := map[string]any{
		"agent":       map[string]any{"model": "a", "temperature": 0.5},
		"prompt_mode": "build",
	}
	b := map[string]any{
		"agent":       map[string]any{"temperature": 1.0},
		"prompt_mode": "plan",
	}
	out := mergeMaps(a, b)
	agent := out["agent"].(map[string]any)
	if agent["model"] != "a" || agent["temperature"] != 1.0 {
		t.Fatalf("merge agent = %v", agent)
	}
	if out["prompt_mode"] != "plan" {
		t.Fatalf("prompt_mode = %v", out["prompt_mode"])
	}
}
