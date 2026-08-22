package acp

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/permission"
	"tars/internal/session"
	"tars/internal/store"
	"tars/internal/tools"
)

func testEnv(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir, config.Storage{
		Synchronous: "NORMAL",
		BusyTimeout: config.Duration{Duration: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	adminKey := "acptest_" + strings.Repeat("a", 64)
	if err := auth.EnsureAdmin(st.DB(), adminKey); err != nil {
		t.Fatalf("ensure admin: %v", err)
	}

	cfg := &config.Config{ReadIsolation: false}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	perm := permission.New(cfg)
	mgr := session.NewManager(st.DB(), logger, "/work", nil, dir+"/sessions", config.Session{}, nil, "parallel")
	reg := tools.Default(cfg)
	return New(cfg, st.DB(), logger, mgr, reg, perm), st, adminKey
}

func post(t *testing.T, srv *Server, key, body string) *rpcResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/acp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &resp
}

func TestInitialize(t *testing.T) {
	srv, _, key := testEnv(t)
	resp := post(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	if result["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	caps, _ := result["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatalf("loadSession capability missing")
	}
}

func TestUnauthorized(t *testing.T) {
	srv, _, _ := testEnv(t)
	req := httptest.NewRequest("POST", "/acp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNewSessionAndList(t *testing.T) {
	srv, _, key := testEnv(t)
	resp := post(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work"}}`)
	if resp.Error != nil {
		t.Fatalf("session/new error: %v", resp.Error)
	}
	sid, _ := resp.Result.(map[string]any)["sessionId"].(string)
	if sid == "" {
		t.Fatal("no sessionId")
	}

	resp2 := post(t, srv, key, `{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}`)
	if resp2.Error != nil {
		t.Fatalf("session/list error: %v", resp2.Error)
	}
	sessions, _ := resp2.Result.(map[string]any)["sessions"].([]any)
	found := false
	for _, s := range sessions {
		if m, ok := s.(map[string]any); ok && m["sessionId"] == sid {
			found = true
		}
	}
	if !found {
		t.Fatal("new session not in list")
	}
}

func TestMethodNotFound(t *testing.T) {
	srv, _, key := testEnv(t)
	resp := post(t, srv, key, `{"jsonrpc":"2.0","id":9,"method":"nope","params":{}}`)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected method not found, got %+v", resp.Error)
	}
}

var _ = os.Getenv
