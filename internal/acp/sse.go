package acp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"tars/internal/auth"
	"tars/internal/session"
)

// message.created 事件 Data：{ id, role, content }
type msgEvent struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// handlePromptSSE：session/prompt 通过 SSE 流返回。
// 先返回 JSON-RPC 请求结果（sessionId），随后推送 ACP 通知，
// turn.done/turn.failed 后结束流。
func (s *Server) handlePromptSSE(w http.ResponseWriter, r *http.Request, ctx context.Context, req rpcRequest) {
	sid := strOr(req.Params["sessionId"], "")
	if sid == "" {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "sessionId required"}})
		return
	}
	a, ok := s.mgr.Get(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32004, Message: "session not found"}})
		return
	}
	ki, _ := ctx.Value(keyInfoKey).(auth.KeyInfo)
	if !s.checkRead(ki, a.KeyID) {
		writeJSON(w, http.StatusForbidden, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32003, Message: "forbidden"}})
		return
	}

	text := promptText(req.Params["prompt"])
	if text == "" {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "prompt required"}})
		return
	}

	// 先确认请求已接受（返回 JSON-RPC 结果），然后建立 SSE 流。
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)

	// 立即响应请求结果：{ sessionId }
	writeSSE(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"sessionId": sid}})
	if fl != nil {
		fl.Flush()
	}

	sub := a.Subscribe()
	defer a.Unsubscribe(sub)

	promptReq := session.PromptReq{Text: text, Mode: promptModeOf(req)}
	if err := a.Prompt(promptReq); err != nil {
		writeSSE(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: err.Error()}})
		if fl != nil {
			fl.Flush()
		}
		return
	}

	// 心跳
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctxDone := r.Context().Done()
	for {
		select {
		case <-ctxDone:
			return
		case <-heartbeat.C:
			// SSE 注释心跳
			_, _ = w.Write([]byte(":\n\n"))
			if fl != nil {
				fl.Flush()
			}
		case ev, ok := <-sub.Ch:
			if !ok {
				return
			}
			finished := false
			switch ev.Type {
			case "message.created":
				writeSSE(w, s.acpNotification(ev))
			case "session.updated":
				// 忽略状态推送，避免打扰
			case "turn.done", "turn.failed":
				finished = true
			}
			if fl != nil {
				fl.Flush()
			}
			if finished {
				return
			}
		}
	}
}

// acpNotification 把内部 message.created 事件转为 ACP session/update 通知。
// 内部事件 Data：{ id, role, content }
func (s *Server) acpNotification(ev session.Event) map[string]any {
	data, _ := ev.Data.(map[string]any)
	mid, _ := data["id"].(string)
	role, _ := data["role"].(string)
	content := data["content"]

	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"message": map[string]any{
				"id":      mid,
				"role":    role,
				"content": content,
			},
		},
	}
}

// messagesOf 从 DB 读取会话消息（供 session/load）。
func (s *Server) messagesOf(a *session.Actor, after int64, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, seq, role, content FROM message WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		a.ID, after, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		var mid string
		var seq int64
		var role, content string
		if err := rows.Scan(&mid, &seq, &role, &content); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":      mid,
			"seq":     seq,
			"role":    role,
			"content": json.RawMessage(content),
		})
	}
	return out, rows.Err()
}

// promptText 从 ACP prompt 数组提取文本内容。
func promptText(prompt any) string {
	arr, ok := prompt.([]any)
	if !ok {
		if s, ok2 := prompt.(string); ok2 {
			return s
		}
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "text":
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func promptModeOf(req rpcRequest) string {
	m, _ := req.Params["mode"].(string)
	if m == "" {
		return "build"
	}
	return m
}

func writeSSE(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
}
