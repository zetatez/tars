package a2a

import (
	"encoding/json"
	"net/http"
	"strings"
)

// WellKnown 返回 /.well-known/agent-card.json 发现端点（无需鉴权，
// 客户端据此获取认证要求与服务地址）。
func (s *Server) WellKnown() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, err := json.Marshal(s.AgentCard(r))
		if err != nil {
			http.Error(w, "card encode failed", http.StatusInternalServerError)
			return
		}
		etag := `"` + s.cardETag + `"`
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(b)
	})
}

// AgentCard 构造 Agent Card，接口 URL 由请求 Host 推导。
func (s *Server) AgentCard(r *http.Request) map[string]any {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
	}
	base := scheme + "://" + r.Host

	return map[string]any{
		"name":        "tars",
		"description": "Tars coding agent: 在受控工作区内执行编码任务的远程 agent，支持多轮会话、工具调用与审批。",
		"version":     s.version,
		"provider": map[string]any{
			"organization": "tars",
			"url":          base,
		},
		"supportedInterfaces": []any{
			map[string]any{
				"url":             base + "/a2a",
				"protocolBinding": "JSONRPC",
				"protocolVersion": ProtocolVersion,
			},
		},
		"capabilities": map[string]any{
			"streaming":         true,
			"pushNotifications": false,
		},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills": []any{
			map[string]any{
				"id":          "coding-agent",
				"name":        "Coding Agent",
				"description": "读写文件、执行命令并完成编码任务；通过 contextId 延续同一会话的多轮交互。",
				"tags":        []string{"coding", "agent", "tools"},
				"examples": []string{
					"修复 src/foo.go 中的编译错误并说明原因",
					"为 internal/api 包补充单元测试",
				},
			},
		},
		"securitySchemes": map[string]any{
			"bearer": map[string]any{
				"type":        "http",
				"scheme":      "bearer",
				"description": "tars API key（key_id + '_' + hex secret）",
			},
		},
		"securityRequirements": []any{
			map[string]any{"bearer": []string{}},
		},
	}
}
