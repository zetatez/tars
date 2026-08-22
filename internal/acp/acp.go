// Package acp 实现 Agent Client Protocol（ACP）服务端。
// ACP 是连接代码编辑器与编码 agent 的开放协议（JSON-RPC 2.0 over Streamable HTTP）。
// 参考：https://agentclientprotocol.com
package acp

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/permission"
	"tars/internal/session"
	"tars/internal/tools"
)

// ProtocolVersion 本服务端实现的 ACP 协议版本。
const ProtocolVersion = 1

type Server struct {
	cfg   *config.Config
	db    *sql.DB
	log   *slog.Logger
	mgr   *session.Manager
	tools *tools.Registry
	perm  *permission.Evaluator
}

func New(cfg *config.Config, db *sql.DB, log *slog.Logger, mgr *session.Manager, tools *tools.Registry, perm *permission.Evaluator) *Server {
	return &Server{cfg: cfg, db: db, log: log, mgr: mgr, tools: tools, perm: perm}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /acp", s.handle)
	mux.HandleFunc("GET /acp", s.handleSSE)
	return mux
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handle POST /acp：接收 JSON-RPC 请求。
// 普通方法返回 application/json 响应；session/prompt 等长操作返回 SSE 流。
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return
	}

	ki, err := auth.Authenticate(s.db, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32001, Message: "unauthorized"}})
		return
	}
	ctx := context.WithValue(r.Context(), keyInfoKey, ki)

	// 长操作（prompt）走 SSE 流，其余同步处理。
	if req.Method == "session/prompt" {
		s.handlePromptSSE(w, r, ctx, req)
		return
	}

	resp := s.dispatch(ctx, req, ki)
	writeJSON(w, http.StatusOK, resp)
}

// handleSSE GET /acp：健康/握手端点（Streamable HTTP 可选）。
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "protocol": "acp"})
}

type ctxKey struct{}

var keyInfoKey ctxKey

func (s *Server) dispatch(ctx context.Context, req rpcRequest, ki auth.KeyInfo) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleInitialize(req)}
	case "session/new":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleNewSession(ctx, req, ki)}
	case "session/load", "session/resume":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleLoadSession(ctx, req, ki)}
	case "session/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleListSessions(ctx, ki)}
	case "session/close":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleCloseSession(ctx, req, ki)}
	case "session/delete":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleDeleteSession(ctx, req, ki)}
	case "session/cancel":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.handleCancelSession(ctx, req, ki)}
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

// ---- 核心方法 ----

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type clientCapabilities struct {
}

func (s *Server) handleInitialize(req rpcRequest) map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession":    true,
			"streaming":      true,
			"customMessages": true,
			"terminal":       false,
		},
		"agentInfo": map[string]any{
			"name":    "tars",
			"version": "0.1.0",
		},
		"clientInfo":         firstStringMap(req.Params["clientInfo"]),
		"clientCapabilities": req.Params["clientCapabilities"],
	}
}

func (s *Server) handleNewSession(ctx context.Context, req rpcRequest, ki auth.KeyInfo) map[string]any {
	cwd := strOr(req.Params["cwd"], s.cfg.DefaultCwd)
	if cwd == "" {
		return errResult("cwd is required")
	}
	role := ki.Role
	if role == "" {
		role = "read_only"
	}
	a, err := s.mgr.Create(ki.KeyID, cwd, s.cfg.Agent.Model, role, 0, s.cfg.PromptMode, "", "")
	if err != nil {
		return errResult(err.Error())
	}
	return map[string]any{"sessionId": a.ID}
}

func (s *Server) handleLoadSession(ctx context.Context, req rpcRequest, ki auth.KeyInfo) map[string]any {
	sid := strOr(req.Params["sessionId"], "")
	if sid == "" {
		return errResult("sessionId is required")
	}
	a, ok := s.mgr.Get(sid)
	if !ok {
		return errResult("session not found")
	}
	if !s.checkRead(ki, a.KeyID) {
		return errResult("forbidden")
	}
	// resume 重新进入会话；load 返回会话元信息
	msgs, _ := s.messagesOf(a, 0, 200)
	return map[string]any{
		"sessionId": a.ID,
		"messages":  msgs,
	}
}

func (s *Server) handleListSessions(ctx context.Context, ki auth.KeyInfo) map[string]any {
	gs, _, err := s.mgr.GlobalSessions(200, 0)
	if err != nil {
		return errResult(err.Error())
	}
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		if !s.checkRead(ki, g.KeyID) {
			continue
		}
		out = append(out, map[string]any{
			"sessionId":   g.ID,
			"cwd":         g.Cwd,
			"status":      g.Status,
			"model":       g.Model,
			"timeUpdated": g.TimeUpdated,
		})
	}
	return map[string]any{"sessions": out}
}

func (s *Server) handleCloseSession(ctx context.Context, req rpcRequest, ki auth.KeyInfo) map[string]any {
	sid := strOr(req.Params["sessionId"], "")
	a, ok := s.mgr.Get(sid)
	if !ok {
		return errResult("session not found")
	}
	if !s.checkRead(ki, a.KeyID) {
		return errResult("forbidden")
	}
	a.Interrupt()
	return map[string]any{"sessionId": sid}
}

func (s *Server) handleDeleteSession(ctx context.Context, req rpcRequest, ki auth.KeyInfo) map[string]any {
	sid := strOr(req.Params["sessionId"], "")
	a, ok := s.mgr.Get(sid)
	if !ok {
		return errResult("session not found")
	}
	if !s.checkRead(ki, a.KeyID) {
		return errResult("forbidden")
	}
	if err := s.mgr.Delete(sid); err != nil {
		return errResult(err.Error())
	}
	return map[string]any{"sessionId": sid}
}

func (s *Server) handleCancelSession(ctx context.Context, req rpcRequest, ki auth.KeyInfo) map[string]any {
	sid := strOr(req.Params["sessionId"], "")
	a, ok := s.mgr.Get(sid)
	if !ok {
		return errResult("session not found")
	}
	if !s.checkRead(ki, a.KeyID) {
		return errResult("forbidden")
	}
	a.Interrupt()
	return map[string]any{"sessionId": sid}
}

// ---- 辅助 ----

func (s *Server) checkRead(ki auth.KeyInfo, sessionKeyID string) bool {
	if !s.cfg.ReadIsolation {
		return true
	}
	return ki.Role == auth.RoleAdmin || ki.KeyID == sessionKeyID
}

func errResult(msg string) map[string]any {
	return map[string]any{"error": msg}
}

func strOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func firstStringMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
