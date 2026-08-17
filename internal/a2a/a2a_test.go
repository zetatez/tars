package a2a

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/session"
	"tars/internal/store"
)

func testEnv(t *testing.T) (*Server, string) {
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
	mgr := session.NewManager(st.DB(), logger, "/work", nil, dir+"/sessions", config.Session{}, nil, "parallel")
	return New(cfg, st.DB(), logger, mgr), adminKey
}

// flushRecorder 支持 Flusher 的 httptest.ResponseWrapper。
type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (flushRecorder) Flush() {}

func postRPC(t *testing.T, srv *Server, key, body string, headers map[string]string) *rpcResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/a2a", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v (%s)", err, rec.Body.String())
	}
	return &resp
}

func TestAgentCard(t *testing.T) {
	srv, _ := testEnv(t)
	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	srv.WellKnown().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("parse card: %v", err)
	}
	if card["name"] != "tars" {
		t.Fatalf("card name = %v", card["name"])
	}
	caps, _ := card["capabilities"].(map[string]any)
	if caps["streaming"] != true {
		t.Fatal("streaming capability missing")
	}
	ifaces, _ := card["supportedInterfaces"].([]any)
	if len(ifaces) == 0 {
		t.Fatal("no supported interfaces")
	}
	iface, _ := ifaces[0].(map[string]any)
	if iface["protocolBinding"] != "JSONRPC" || iface["protocolVersion"] != ProtocolVersion {
		t.Fatalf("interface = %v", iface)
	}
}

func TestUnauthorized(t *testing.T) {
	srv, _ := testEnv(t)
	req := httptest.NewRequest("POST", "/a2a", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestVersionNotSupported(t *testing.T) {
	srv, key := testEnv(t)
	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`,
		map[string]string{"A2A-Version": "0.3"})
	if resp.Error == nil || resp.Error.Code != CodeVersionNotSupported {
		t.Fatalf("expected version error, got %+v", resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	srv, key := testEnv(t)
	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":7,"method":"Nope","params":{}}`, nil)
	if resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected method not found, got %+v", resp.Error)
	}
}

func TestPushNotificationsNotSupported(t *testing.T) {
	srv, key := testEnv(t)
	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":8,"method":"CreateTaskPushNotificationConfig","params":{}}`, nil)
	if resp.Error == nil || resp.Error.Code != CodePushNotSupported {
		t.Fatalf("expected push not supported, got %+v", resp.Error)
	}
}

func TestSendMessageValidation(t *testing.T) {
	srv, key := testEnv(t)

	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"ROLE_USER","messageId":"m1"}}}`, nil)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("expected invalid params, got %+v", resp.Error)
	}

	txt := "hi"
	parts, _ := json.Marshal([]Part{{URL: "http://x/f.png", MediaType: "image/png"}})
	body := `{"jsonrpc":"2.0","id":2,"method":"SendMessage","params":{"message":{"role":"ROLE_USER","messageId":"m2","parts":` + string(parts) + `}}}`
	_ = txt
	resp = postRPC(t, srv, key, body, nil)
	if resp.Error == nil || resp.Error.Code != CodeContentTypeNotSupp {
		t.Fatalf("expected content type error, got %+v", resp.Error)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv, key := testEnv(t)
	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"nope"}}`, nil)
	if resp.Error == nil || resp.Error.Code != CodeTaskNotFound {
		t.Fatalf("expected task not found, got %+v", resp.Error)
	}
}

func TestCancelTerminalTask(t *testing.T) {
	srv, key := testEnv(t)
	rec := newTaskRecord("ctx", "acptest")
	srv.registerTask(rec)
	rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: rec.ID, ContextID: "ctx",
		Status: TaskStatus{State: StateCompleted}}})

	select {
	case <-rec.done:
	default:
		t.Fatal("done should be closed after terminal emit")
	}

	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"CancelTask","params":{"id":"`+rec.ID+`"}}`, nil)
	if resp.Error == nil || resp.Error.Code != CodeTaskNotCancelable {
		t.Fatalf("expected not cancelable, got %+v", resp.Error)
	}
}

func TestSubscribeToTaskReplayAndFollow(t *testing.T) {
	srv, key := testEnv(t)
	rec := newTaskRecord("ctx", "acptest")
	srv.registerTask(rec)

	body := `{"jsonrpc":"2.0","id":"s1","method":"SubscribeToTask","params":{"id":"` + rec.ID + `"}}`
	req := httptest.NewRequest("POST", "/a2a", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	ctx, cancel := contextWithCancel()
	req = req.WithContext(ctx)
	recorder := &flushRecorder{httptest.NewRecorder()}
	served := make(chan struct{})
	go func() {
		defer close(served)
		srv.Handler().ServeHTTP(recorder, req)
	}()

	// 确定性等待：handler 挂载 watcher 后再发事件，避免鉴权耗时导致的时序抖动
	deadline := time.Now().Add(5 * time.Second)
	for rec.watcherCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("handler never attached watcher")
		}
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: rec.ID, ContextID: "ctx",
			Status: TaskStatus{State: StateWorking}}})
		rec.emit(StreamResponse{ArtifactUpdate: &ArtifactUpdateEvent{
			TaskID: rec.ID, ContextID: "ctx",
			Artifact: Artifact{ArtifactID: "a1", Parts: []Part{TextPart("hello")}},
		}})
		rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: rec.ID, ContextID: "ctx",
			Status: TaskStatus{State: StateCompleted}}})
	}()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
	cancel()

	out := recorder.Body.String()
	if !strings.Contains(out, `"TASK_STATE_WORKING"`) {
		t.Fatalf("replayed working event missing:\n%s", out)
	}
	if !strings.Contains(out, "artifactUpdate") || !strings.Contains(out, "hello") {
		t.Fatalf("artifact event missing:\n%s", out)
	}
	if !strings.Contains(out, `"TASK_STATE_COMPLETED"`) {
		t.Fatalf("terminal event missing:\n%s", out)
	}
}

func TestListTasksFiltering(t *testing.T) {
	srv, key := testEnv(t)
	r1 := newTaskRecord("ctx-a", "acptest")
	r2 := newTaskRecord("ctx-b", "acptest")
	srv.registerTask(r1)
	srv.registerTask(r2)

	resp := postRPC(t, srv, key, `{"jsonrpc":"2.0","id":1,"method":"ListTasks","params":{"contextId":"ctx-a"}}`, nil)
	if resp.Error != nil {
		t.Fatalf("list error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	tasks, _ := result["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task, _ := tasks[0].(map[string]any)
	if task["contextId"] != "ctx-a" {
		t.Fatalf("contextId = %v", task["contextId"])
	}

	// 状态过滤
	r2.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: r2.ID, ContextID: "ctx-b",
		Status: TaskStatus{State: StateFailed}}})
	resp = postRPC(t, srv, key, `{"jsonrpc":"2.0","id":2,"method":"ListTasks","params":{"status":"`+StateFailed+`"}}`, nil)
	result, _ = resp.Result.(map[string]any)
	tasks, _ = result["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["id"] != r2.ID {
		t.Fatalf("status filter failed: %v", result)
	}
}

func TestTaskRecordEmitWatchers(t *testing.T) {
	rec := newTaskRecord("c", "k")
	wid, ch := rec.addWatcher()
	go func() {
		rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: rec.ID, ContextID: "c",
			Status: TaskStatus{State: StateWorking}}})
		rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{TaskID: rec.ID, ContextID: "c",
			Status: TaskStatus{State: StateCompleted}}})
	}()

	ev := <-ch
	if ev.StatusUpdate == nil || ev.StatusUpdate.Status.State != StateWorking {
		t.Fatalf("unexpected first event: %+v", ev)
	}
	ev = <-ch
	if ev.StatusUpdate == nil || ev.StatusUpdate.Status.State != StateCompleted {
		t.Fatalf("unexpected terminal event: %+v", ev)
	}
	select {
	case <-rec.done:
	case <-time.After(time.Second):
		t.Fatal("done not closed")
	}
	rec.removeWatcher(wid)
	if len(rec.replayEvents()) != 2 {
		t.Fatalf("replay log size = %d", len(rec.replayEvents()))
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
