package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/perm"
	"tars/internal/session"
	"tars/internal/tools"
)

type Server struct {
	cfg   *config.Config
	db    *sql.DB
	log   *slog.Logger
	tools *tools.Registry
	perm  *perm.Evaluator
	mgr   *session.Manager
}

func New(cfg *config.Config, db *sql.DB, log *slog.Logger, tools *tools.Registry, perm *perm.Evaluator, mgr *session.Manager) *Server {
	return &Server{cfg: cfg, db: db, log: log, tools: tools, perm: perm, mgr: mgr}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", s.handleRPC)
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

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	ki, err := auth.Authenticate(s.db, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}

	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.dispatch(r.Context(), req, ki.KeyID, ki.Role)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest, keyID, role string) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "tars", "version": "0.1.0"},
			},
		}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.listTools()}}
	case "tools/call":
		name, _ := req.Params["name"].(string)
		arguments, _ := req.Params["arguments"].(map[string]any)
		result, err := s.callTool(ctx, name, arguments, keyID, role)
		if err != nil {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: &mcpCallResult{
				Content: []mcpContent{{Type: "text", Text: err.Error()}},
				IsError: true,
			}}
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found"}}
	}
}

func (s *Server) listTools() []mcpTool {
	out := make([]mcpTool, 0, len(s.tools.List())+1)
	for _, t := range s.tools.List() {
		if t.PolicyAction == "task_done" {
			continue
		}
		out = append(out, mcpTool{Name: t.Name, Description: t.Description, InputSchema: t.Params})
	}
	out = append(out, mcpTool{
		Name:        "agent",
		Description: "委派一个完整 agent turn（独立子会话），返回结果。参数：prompt(必填)、cwd、model。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string"},
				"cwd":    map[string]any{"type": "string"},
				"model":  map[string]any{"type": "string"},
			},
			"required": []string{"prompt"},
		},
	})
	return out
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any, keyID, role string) (*mcpCallResult, error) {
	if name == "agent" {
		prompt, _ := args["prompt"].(string)
		cwd, _ := args["cwd"].(string)
		model, _ := args["model"].(string)
		text, err := s.mgr.RunSync(keyID, cwd, model, role, prompt)
		if err != nil {
			return nil, err
		}
		return &mcpCallResult{Content: []mcpContent{{Type: "text", Text: text}}}, nil
	}

	_, ok := s.tools.Get(name)
	if !ok {
		return nil, errors.New("unknown tool: " + name)
	}

	dec := s.evaluate(name, args, role)
	if dec.Effect != perm.EffectAllow {
		return nil, errors.New("denied: " + dec.Reason)
	}

	cwd, _ := args["cwd"].(string)
	if cwd == "" {
		cwd = s.cfg.DefaultCwd
	}
	scope := &tools.Scope{Cwd: cwd, KeyID: keyID, DB: s.db, Cfg: s.cfg}

	results := s.tools.ExecuteBatch(ctx, []tools.Call{{ID: "1", Name: name, Args: args}}, scope, 1)
	cr := results[0]
	text, _ := json.Marshal(cr.Result)
	return &mcpCallResult{
		Content: []mcpContent{{Type: "text", Text: string(text)}},
		IsError: cr.Status == "error" || cr.Status == "denied",
	}, nil
}

func (s *Server) evaluate(name string, args map[string]any, role string) perm.Decision {
	switch name {
	case "exec_command":
		return s.perm.EvaluateExec(toStringSlice(args["argv"]), role)
	case "write_file", "edit_file":
		path, _ := args["path"].(string)
		return s.perm.EvaluateWrite(path, role)
	default:
		return s.perm.EvaluateRead(name, "")
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
