package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// keyConfigField 描述一个可配置参数的元信息（模板叶子节点）。
// Default 为全局兜底值（nil 表示未设置、沿用 config.yaml）；Value 为当前生效值。
type keyConfigField struct {
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     any    `json:"default"`
	Value       any    `json:"value"`
}

func field(desc, typ string, def any) keyConfigField {
	return keyConfigField{Description: desc, Type: typ, Default: def, Value: def}
}

// keyConfigTemplate 返回 per-key 配置的完整模板（含每个参数的 description/type/default）。
// 结构对应 docs/ARCHITECTURE.md §13.1：可配置项为
// agent(system_prompt/model/temperature/max_tokens/max_tool_steps)、prompt_mode、
// permissions.rules、quota 阈值微调。
func keyConfigTemplate() map[string]any {
	return map[string]any{
		"agent": map[string]any{
			"system_prompt":  field("追加到系统提示词的额外指令（下个 turn 生效）", "string", ""),
			"model":          field("默认模型名，覆盖 config.yaml 的 agent.model", "string", ""),
			"temperature":    field("采样温度 0.0-2.0（留空用全局默认）", "number", nil),
			"max_tokens":     field("单次回复最大 token 数", "integer", nil),
			"max_tool_steps": field("单个 turn 的最大工具步数", "integer", nil),
		},
		"prompt_mode": field("会话默认模式：build（可执行工具）| plan（只读规划）", "string", "build"),
		"permissions": map[string]any{
			"rules": field("追加的权限白名单规则（数组，格式同 config.yaml permissions.rules）", "array", []any{}),
		},
		"quota": map[string]any{
			"max_active_sessions":          field("该 key 最大活跃会话数", "integer", nil),
			"max_concurrent_turns_per_key": field("该 key 最大并发 turn 数", "integer", nil),
		},
	}
}

// fillTemplateValue 把已存储的配置（嵌套 JSON）合并进模板，填充各叶子的 value。
func fillTemplateValue(tpl map[string]any, stored map[string]any) {
	for k, v := range stored {
		t, ok := tpl[k]
		if !ok {
			continue // 未知字段：不进模板
		}
		if tm, ok := t.(map[string]any); ok {
			if sm, ok := v.(map[string]any); ok {
				fillTemplateValue(tm, sm)
			}
			continue
		}
		if f, ok := t.(keyConfigField); ok {
			f.Value = v
			tpl[k] = f
		}
	}
}

// validateKeyConfigKeys 校验 body 的键都在模板白名单内，返回第一个未知键。
func validateKeyConfigKeys(body map[string]any, tpl map[string]any, prefix string) error {
	for k, v := range body {
		t, ok := tpl[k]
		if !ok {
			return fmt.Errorf("unknown key config field: %s%s", prefix, k)
		}
		if tm, ok := t.(map[string]any); ok {
			if sm, ok := v.(map[string]any); ok {
				if err := validateKeyConfigKeys(sm, tm, prefix+k+"."); err != nil {
					return err
				}
			}
			continue
		}
		// 叶子节点：基本类型校验（可选）
		if f, ok := t.(keyConfigField); ok {
			switch f.Type {
			case "integer":
				if _, ok := v.(float64); !ok {
					return fmt.Errorf("field %s%s must be integer", prefix, k)
				}
			case "number":
				if _, ok := v.(float64); !ok {
					return fmt.Errorf("field %s%s must be number", prefix, k)
				}
			case "string":
				if _, ok := v.(string); !ok {
					return fmt.Errorf("field %s%s must be string", prefix, k)
				}
			case "array":
				if _, ok := v.([]any); !ok {
					return fmt.Errorf("field %s%s must be array", prefix, k)
				}
			}
		}
	}
	return nil
}

// handleGetKeyConfig 返回完整参数模板：每个可配置项含 description/type/default/当前值。
func (s *Server) handleGetKeyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	tpl := keyConfigTemplate()
	var config string
	err := s.db.QueryRow(`SELECT config FROM key_config WHERE key_id = ?`, id).Scan(&config)
	if err != nil {
		writeJSON(w, http.StatusOK, tpl)
		return
	}
	stored := map[string]any{}
	if json.Unmarshal([]byte(config), &stored) == nil {
		fillTemplateValue(tpl, stored)
	}
	writeJSON(w, http.StatusOK, tpl)
}

// handlePutKeyConfig 更新配置：字段白名单校验 + 与已有配置深度合并。
func (s *Server) handlePutKeyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := validateKeyConfigKeys(body, keyConfigTemplate(), ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	existing := map[string]any{}
	var existingRaw string
	if err := s.db.QueryRow(`SELECT config FROM key_config WHERE key_id = ?`, id).Scan(&existingRaw); err == nil {
		_ = json.Unmarshal([]byte(existingRaw), &existing)
	}
	merged := mergeMaps(existing, body)
	mergedJSON, _ := json.Marshal(merged)
	_, err := s.db.Exec(
		`INSERT INTO key_config (key_id, config, time_updated) VALUES (?, ?, ?)
		 ON CONFLICT(key_id) DO UPDATE SET config = excluded.config, time_updated = excluded.time_updated`,
		id, string(mergedJSON), time.Now().Unix(),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("key config updated", "key_id", id)
	writeJSON(w, http.StatusOK, json.RawMessage(mergedJSON))
}

// mergeMaps 深度合并：b 覆盖 a；嵌套 map 递归合并。
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			if am, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(am, bm)
				continue
			}
		}
		out[k] = v
	}
	return out
}
