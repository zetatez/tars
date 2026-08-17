package a2a

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/session"
)

// Server 实现 A2A v1.0 JSON-RPC 绑定端点。
type Server struct {
	cfg *config.Config
	db  *sql.DB
	log *slog.Logger
	mgr *session.Manager

	version  string
	cardETag string

	mu    sync.Mutex
	tasks map[string]*taskRecord
	order []string // 创建顺序，供 ListTasks 排序

	gates sync.Map // sessionID → *atomic.Int32，同一会话同时只跑一个 turn
}

// New 创建 A2A server。
func New(cfg *config.Config, db *sql.DB, log *slog.Logger, mgr *session.Manager) *Server {
	return &Server{
		cfg:      cfg,
		db:       db,
		log:      log,
		mgr:      mgr,
		version:  "0.1.0",
		cardETag: fmt.Sprintf("%x", time.Now().Unix()),
		tasks:    make(map[string]*taskRecord),
	}
}

// taskRecord 跟踪一个 A2A 任务的生命周期。任务本身在内存中（重启丢失），
// 关联会话与消息持久化于 sqlite，可经 contextId 继续交互。
type taskRecord struct {
	ID        string
	ContextID string // tars session id
	KeyID     string
	created   time.Time

	mu        sync.Mutex
	state     string
	canceled  bool
	artifacts []Artifact
	watchers  map[int64]chan StreamResponse
	nextW     int64
	replay    []StreamResponse // 已发事件，供 SubscribeToTask 重放
	done      chan struct{}    // 进入终态时关闭
}

func newTaskRecord(contextID, keyID string) *taskRecord {
	return &taskRecord{
		ID:        uuid.NewString(),
		ContextID: contextID,
		KeyID:     keyID,
		created:   time.Now(),
		state:     StateSubmitted,
		watchers:  make(map[int64]chan StreamResponse),
		done:      make(chan struct{}),
	}
}

func (t *taskRecord) snapshotState() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// emit 记录并广播事件；终态 statusUpdate 时置 state 并关闭 done。
func (t *taskRecord) emit(ev StreamResponse) {
	var terminal bool
	t.mu.Lock()
	if ev.StatusUpdate != nil && terminalState(ev.StatusUpdate.Status.State) {
		t.state = ev.StatusUpdate.Status.State
	}
	if ev.ArtifactUpdate != nil {
		t.artifacts = append(t.artifacts, ev.ArtifactUpdate.Artifact)
	}
	t.replay = append(t.replay, ev)
	for _, ch := range t.watchers {
		select {
		case ch <- ev:
		default: // 慢消费者丢弃，避免阻塞任务执行
		}
	}
	terminal = terminalState(t.state)
	t.mu.Unlock()
	if terminal {
		close(t.done)
	}
}

func (t *taskRecord) addWatcher() (int64, chan StreamResponse) {
	ch := make(chan StreamResponse, 128)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextW++
	id := t.nextW
	t.watchers[id] = ch
	return id, ch
}

func (t *taskRecord) removeWatcher(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ch, ok := t.watchers[id]; ok {
		delete(t.watchers, id)
		close(ch)
	}
}

func (t *taskRecord) replayEvents() []StreamResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]StreamResponse, len(t.replay))
	copy(out, t.replay)
	return out
}

// ---- JSON-RPC 信封 ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []any  `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func errorInfo(reason string, metadata map[string]string) map[string]any {
	return map[string]any{
		"@type":    "type.googleapis.com/google.rpc.ErrorInfo",
		"reason":   reason,
		"domain":   "a2a-protocol.org",
		"metadata": metadata,
	}
}

func rpcErrResp(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func okResp(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

// ---- HTTP handler ----

// Handler 返回 JSON-RPC 端点处理器。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ki, err := auth.Authenticate(s.db, r)
	if err != nil {
		writeRPC(w, http.StatusUnauthorized, rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: CodeInvalidRequest, Message: "unauthorized"},
		})
		return
	}
	// 版本协商：客户端显式声明且不受支持时拒绝（§3.6 / §14.2）
	if v := r.Header.Get("A2A-Version"); v != "" && v != ProtocolVersion && v != "1" {
		writeRPC(w, http.StatusOK, rpcResponse{
			JSONRPC: "2.0",
			Error: &rpcError{
				Code:    CodeVersionNotSupported,
				Message: "version not supported",
				Data:    []any{errorInfo("VERSION_NOT_SUPPORTED", map[string]string{"requested": v, "supported": ProtocolVersion})},
			},
		})
		return
	}

	ct := r.Header.Get("Content-Type")
	if ct != "" && ct != "application/json" && ct != "application/a2a+json" {
		writeRPC(w, http.StatusOK, parseErrorResponse())
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, http.StatusOK, parseErrorResponse())
		return
	}
	if req.JSONRPC != "2.0" || len(req.ID) == 0 || req.Method == "" {
		writeRPC(w, http.StatusOK, invalidRequest(req.ID))
		return
	}

	switch req.Method {
	case "SendStreamingMessage":
		fl, ok := w.(http.Flusher)
		if !ok {
			writeRPC(w, http.StatusInternalServerError, rpcErrResp(req.ID, CodeInternalError, "streaming unsupported"))
			return
		}
		s.handleSendStream(w, fl, r, ki, req)
	case "SubscribeToTask":
		fl, ok := w.(http.Flusher)
		if !ok {
			writeRPC(w, http.StatusInternalServerError, rpcErrResp(req.ID, CodeInternalError, "streaming unsupported"))
			return
		}
		s.handleSubscribeStream(w, fl, r, ki, req)
	default:
		writeRPC(w, http.StatusOK, s.dispatch(r, ki, req))
	}
}

func parseErrorResponse() rpcResponse {
	return rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: CodeParseError, Message: "Invalid JSON payload"}}
}

func invalidRequest(id json.RawMessage) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: CodeInvalidRequest, Message: "Request payload validation error"}}
}

func writeRPC(w http.ResponseWriter, status int, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// dispatch 非流式 JSON-RPC 方法分发（PascalCase，规格 §9.4）。
func (s *Server) dispatch(r *http.Request, ki auth.KeyInfo, req rpcRequest) rpcResponse {
	switch req.Method {
	case "SendMessage":
		return s.handleSend(r, ki, req)
	case "GetTask":
		return s.handleGetTask(ki, req)
	case "ListTasks":
		return s.handleListTasks(ki, req)
	case "CancelTask":
		return s.handleCancelTask(ki, req)
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig":
		return rpcErrResp(req.ID, CodePushNotSupported, "push notifications not supported")
	case "GetExtendedAgentCard":
		return rpcErrResp(req.ID, CodeExtCardNotConfigured, "extended agent card not configured")
	default:
		return rpcErrResp(req.ID, CodeMethodNotFound, "Method not found")
	}
}

// ---- 参数对象 ----

type sendConfiguration struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes"`
	HistoryLength       *int     `json:"historyLength"`
	ReturnImmediately   *bool    `json:"returnImmediately"`
}

type sendMessageParams struct {
	Tenant        string             `json:"tenant"`
	Message       *Message           `json:"message"`
	Configuration *sendConfiguration `json:"configuration"`
	Metadata      map[string]any     `json:"metadata"`
}

type getTaskParams struct {
	ID            string `json:"id"`
	HistoryLength *int   `json:"historyLength"`
}

type listTasksParams struct {
	ContextID string `json:"contextId"`
	Status    string `json:"status"`
	PageSize  int    `json:"pageSize"`
	PageToken string `json:"pageToken"`
}

// ---- 会话与任务解析 ----

// canRead 判断调用者是否可见该 key 的资源（readIsolation 生效时非 admin 仅限本人）。
func (s *Server) canRead(ki auth.KeyInfo, keyID string) bool {
	return !s.cfg.ReadIsolation || ki.Role == auth.RoleAdmin || ki.KeyID == keyID
}

// resolveTarget 将请求消息绑定到 (task, session)。rec 为 nil 表示新建会话上下文。
func (s *Server) resolveTarget(r *http.Request, ki auth.KeyInfo, msg *Message) (*taskRecord, *session.Actor, *rpcResponse) {
	if msg.TaskID != "" {
		rec := s.getTask(msg.TaskID)
		if rec == nil || rec.KeyID != ki.KeyID || !s.canRead(ki, rec.KeyID) {
			e := rpcErrResp(nil, CodeTaskNotFound, "Task not found") // 不泄露他人任务存在性
			return nil, nil, &e
		}
		a, ok := s.mgr.Get(rec.ContextID)
		if !ok {
			e := rpcErrResp(nil, CodeTaskNotFound, "context not available")
			return nil, nil, &e
		}
		return rec, a, nil
	}
	if msg.ContextID != "" {
		a, ok := s.mgr.Get(msg.ContextID)
		if ok && a.SessionKeyID() == ki.KeyID {
			return nil, a, nil
		}
		if ok {
			e := rpcErrResp(nil, CodeInvalidParams, "context not owned by caller")
			return nil, nil, &e
		}
		// 会话不存在（已删除等）：新建会话继续对话
	}
	a, err := s.newSession(r, ki)
	if err != nil {
		s.log.Error("a2a create session", "err", err)
		e := rpcErrResp(nil, CodeInternalError, "Internal error")
		return nil, nil, &e
	}
	return nil, a, nil
}

func (s *Server) newSession(r *http.Request, ki auth.KeyInfo) (*session.Actor, error) {
	return s.mgr.Create(ki.KeyID, s.cfg.DefaultCwd, "", ki.Role, 0, s.cfg.PromptMode, "a2a:"+ki.KeyID, remoteAddr(r))
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) getTask(id string) *taskRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id]
}

func (s *Server) registerTask(rec *taskRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[rec.ID] = rec
	s.order = append(s.order, rec.ID)
}

// acquireGate 尝试独占会话执行权；返回释放函数或 false（已有任务在跑）。
func (s *Server) acquireGate(sessionID string) (func(), bool) {
	g, _ := s.gates.LoadOrStore(sessionID, &atomic.Int32{})
	n := g.(*atomic.Int32)
	if !n.CompareAndSwap(0, 1) {
		return nil, false
	}
	return func() { n.Store(0) }, true
}

// ---- SendMessage ----

// handleSend 处理 SendMessage：默认阻塞至终态（规格 §3.2.2 returnImmediately）。
func (s *Server) handleSend(r *http.Request, ki auth.KeyInfo, req rpcRequest) rpcResponse {
	var p sendMessageParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Message == nil || len(p.Message.Parts) == 0 {
		return rpcErrResp(req.ID, CodeInvalidParams, "Invalid parameters")
	}
	text, nonText := extractText(p.Message.Parts)
	if nonText {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
			Code:    CodeContentTypeNotSupp,
			Message: "content type not supported",
			Data:    []any{errorInfo("CONTENT_TYPE_NOT_SUPPORTED", map[string]string{"supported": "text/plain"})},
		}}
	}
	if text == "" {
		return rpcErrResp(req.ID, CodeInvalidParams, "message text required")
	}

	rec, a, errResp := s.resolveTarget(r, ki, p.Message)
	if errResp != nil {
		errResp.ID = req.ID
		return *errResp
	}
	if rec == nil {
		rec = newTaskRecord(a.ID, ki.KeyID)
		s.registerTask(rec)
	} else if terminalState(rec.snapshotState()) {
		return rpcErrResp(req.ID, CodeUnsupportedOp, "task is in a terminal state")
	}

	release, ok := s.acquireGate(rec.ContextID)
	if !ok {
		return rpcErrResp(req.ID, CodeUnsupportedOp, "session has an active task; retry later")
	}

	sub := a.Subscribe()
	if err := a.Prompt(session.PromptReq{Text: text}); err != nil {
		a.Unsubscribe(sub)
		release()
		return rpcErrResp(req.ID, CodeUnsupportedOp, err.Error())
	}
	go s.runTask(rec, a, sub, release)

	blocking := p.Configuration == nil || p.Configuration.ReturnImmediately == nil || !*p.Configuration.ReturnImmediately
	if !blocking {
		return okResp(req.ID, StreamResponse{Task: s.taskSnapshot(rec)})
	}
	select {
	case <-rec.done:
	case <-r.Context().Done():
		// 客户端断开：任务后台继续，可经 GetTask 轮询结果
		return okResp(req.ID, StreamResponse{Task: s.taskSnapshot(rec)})
	}
	var hl *int
	if p.Configuration != nil {
		hl = p.Configuration.HistoryLength
	}
	return okResp(req.ID, StreamResponse{Task: s.buildTaskResult(rec, hl)})
}

// runTask 消费一个订阅者的会话事件流，驱动 rec 至终态后释放订阅与闸门。
func (s *Server) runTask(rec *taskRecord, a *session.Actor, sub *session.Subscriber, release func()) {
	defer a.Unsubscribe(sub)
	defer release()

	rec.emit(StreamResponse{Task: s.taskSnapshot(rec)})

	for ev := range sub.Ch {
		switch ev.Type {
		case "turn.started":
			rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{
				TaskID: rec.ID, ContextID: rec.ContextID,
				Status: TaskStatus{State: StateWorking, Timestamp: nowTimestamp()},
			}})
		case "message.created":
			data, _ := ev.Data.(map[string]any)
			role, _ := data["role"].(string)
			content, _ := data["content"].(json.RawMessage)
			if role == "assistant" {
				rec.mu.Lock()
				n := len(rec.artifacts) + 1
				rec.mu.Unlock()
				rec.emit(StreamResponse{ArtifactUpdate: &ArtifactUpdateEvent{
					TaskID: rec.ID, ContextID: rec.ContextID,
					Artifact: Artifact{
						ArtifactID: uuid.NewString(),
						Name:       fmt.Sprintf("response-%d", n),
						Parts:      []Part{TextPart(contentText(content))},
					},
					LastChunk: true,
				}})
			}
		case "turn.failed":
			msg := "turn failed"
			if data, ok := ev.Data.(map[string]any); ok {
				if e, ok := data["error"].(string); ok && e != "" {
					msg = e
				}
			}
			rec.mu.Lock()
			state := StateFailed
			if rec.canceled {
				state = StateCanceled
			}
			rec.mu.Unlock()
			rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{
				TaskID: rec.ID, ContextID: rec.ContextID,
				Status: TaskStatus{
					State: state,
					Message: &Message{Role: RoleAgent, MessageID: uuid.NewString(),
						ContextID: rec.ContextID, TaskID: rec.ID, Parts: []Part{TextPart(msg)}},
					Timestamp: nowTimestamp(),
				},
			}})
			return
		case "turn.done":
			rec.mu.Lock()
			state := StateCompleted
			if rec.canceled {
				state = StateCanceled
			}
			rec.mu.Unlock()
			rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{
				TaskID: rec.ID, ContextID: rec.ContextID,
				Status: TaskStatus{State: state, Timestamp: nowTimestamp()},
			}})
			return
		}
	}
	// 订阅异常关闭（会话销毁）：以失败收尾
	rec.emit(StreamResponse{StatusUpdate: &StatusUpdateEvent{
		TaskID: rec.ID, ContextID: rec.ContextID,
		Status: TaskStatus{
			State:     StateFailed,
			Message:   &Message{Role: RoleAgent, MessageID: uuid.NewString(), Parts: []Part{TextPart("session closed")}},
			Timestamp: nowTimestamp(),
		},
	}})
}

// ---- SendStreamingMessage / SubscribeToTask（SSE）----

// sseWrite 输出一条 JSON-RPC 包裹的 StreamResponse 事件。
func sseWrite(w http.ResponseWriter, fl http.Flusher, id json.RawMessage, result StreamResponse) {
	b, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: &result})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}

// startStream 设置 SSE 响应头。
func startStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

// handleSendStream 处理 SendStreamingMessage：任务生命周期事件实时推送，
// 终态后关闭流（规格 §3.1.2 任务生命周期流模式）。
func (s *Server) handleSendStream(w http.ResponseWriter, fl http.Flusher, r *http.Request, ki auth.KeyInfo, req rpcRequest) {
	var p sendMessageParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Message == nil || len(p.Message.Parts) == 0 {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeInvalidParams, "Invalid parameters"))
		return
	}
	text, nonText := extractText(p.Message.Parts)
	if nonText || text == "" {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeContentTypeNotSupp, "only text parts are supported"))
		return
	}
	rec, a, errResp := s.resolveTarget(r, ki, p.Message)
	if errResp != nil {
		errResp.ID = req.ID
		writeRPC(w, http.StatusOK, *errResp)
		return
	}
	if rec == nil {
		rec = newTaskRecord(a.ID, ki.KeyID)
		s.registerTask(rec)
	} else if terminalState(rec.snapshotState()) {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeUnsupportedOp, "task is in a terminal state"))
		return
	}
	release, ok := s.acquireGate(rec.ContextID)
	if !ok {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeUnsupportedOp, "session has an active task; retry later"))
		return
	}

	sub := a.Subscribe()
	if err := a.Prompt(session.PromptReq{Text: text}); err != nil {
		a.Unsubscribe(sub)
		release()
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeUnsupportedOp, err.Error()))
		return
	}

	startStream(w)
	wid, ch := rec.addWatcher() // 先挂 watcher 再启动，确保收到首个 task 快照
	go s.runTask(rec, a, sub, release)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			rec.removeWatcher(wid)
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			sseWrite(w, fl, req.ID, ev)
			if ev.StatusUpdate != nil && terminalState(ev.StatusUpdate.Status.State) {
				rec.removeWatcher(wid)
				return
			}
		}
	}
}

// handleSubscribeStream 处理 SubscribeToTask：重放历史事件并跟随至终态。
func (s *Server) handleSubscribeStream(w http.ResponseWriter, fl http.Flusher, r *http.Request, ki auth.KeyInfo, req rpcRequest) {
	var p getTaskParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeInvalidParams, "Invalid parameters"))
		return
	}
	rec := s.getTask(p.ID)
	if rec == nil || !s.canRead(ki, rec.KeyID) {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeTaskNotFound, "Task not found"))
		return
	}
	if terminalState(rec.snapshotState()) {
		writeRPC(w, http.StatusOK, rpcErrResp(req.ID, CodeUnsupportedOp, "task is in a terminal state"))
		return
	}

	startStream(w)
	wid, ch := rec.addWatcher()
	defer rec.removeWatcher(wid)
	for _, ev := range rec.replayEvents() { // 重放可能重复，客户端按幂等状态处理
		sseWrite(w, fl, req.ID, ev)
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			sseWrite(w, fl, req.ID, ev)
			if ev.StatusUpdate != nil && terminalState(ev.StatusUpdate.Status.State) {
				return
			}
		}
	}
}

// ---- GetTask / ListTasks / CancelTask ----

func (s *Server) handleGetTask(ki auth.KeyInfo, req rpcRequest) rpcResponse {
	var p getTaskParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
		return rpcErrResp(req.ID, CodeInvalidParams, "Invalid parameters")
	}
	rec := s.getTask(p.ID)
	if rec == nil || !s.canRead(ki, rec.KeyID) {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
			Code: CodeTaskNotFound, Message: "Task not found",
			Data: []any{errorInfo("TASK_NOT_FOUND", map[string]string{"taskId": p.ID})},
		}}
	}
	return okResp(req.ID, StreamResponse{Task: s.taskSnapshotWithArtifacts(rec, p.HistoryLength)})
}

func (s *Server) handleListTasks(ki auth.KeyInfo, req rpcRequest) rpcResponse {
	var p listTasksParams
	_ = json.Unmarshal(req.Params, &p)

	pageSize := p.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	offset := 0
	if p.PageToken != "" {
		if n, err := strconv.Atoi(p.PageToken); err == nil && n >= 0 {
			offset = n
		}
	}

	s.mu.Lock()
	all := make([]*taskRecord, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- { // 最近优先
		rec := s.tasks[s.order[i]]
		if rec == nil {
			continue
		}
		if p.ContextID != "" && rec.ContextID != p.ContextID {
			continue
		}
		if p.Status != "" && rec.snapshotState() != p.Status {
			continue
		}
		if !s.canRead(ki, rec.KeyID) {
			continue
		}
		all = append(all, rec)
	}
	total := len(all)
	end := offset + pageSize
	if end > total {
		end = total
	}
	page := all[offset:end]
	s.mu.Unlock()

	tasks := make([]*Task, 0, len(page))
	for _, rec := range page {
		tasks = append(tasks, s.taskSnapshotWithArtifacts(rec, nil))
	}
	next := ""
	if end < total {
		next = strconv.Itoa(end)
	}
	return okResp(req.ID, map[string]any{
		"tasks":         tasks,
		"totalSize":     total,
		"pageSize":      pageSize,
		"nextPageToken": next,
	})
}

func (s *Server) handleCancelTask(ki auth.KeyInfo, req rpcRequest) rpcResponse {
	var p getTaskParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
		return rpcErrResp(req.ID, CodeInvalidParams, "Invalid parameters")
	}
	rec := s.getTask(p.ID)
	if rec == nil || !s.canRead(ki, rec.KeyID) {
		return rpcErrResp(req.ID, CodeTaskNotFound, "Task not found")
	}
	if terminalState(rec.snapshotState()) {
		return rpcErrResp(req.ID, CodeTaskNotCancelable, "task not cancelable")
	}
	a, ok := s.mgr.Get(rec.ContextID)
	if !ok || a.SessionKeyID() != ki.KeyID {
		return rpcErrResp(req.ID, CodeTaskNotCancelable, "task not cancelable")
	}
	rec.mu.Lock()
	rec.canceled = true
	rec.mu.Unlock()
	a.Interrupt()
	select {
	case <-rec.done:
	case <-time.After(10 * time.Second):
	}
	return okResp(req.ID, StreamResponse{Task: s.taskSnapshotWithArtifacts(rec, nil)})
}

// ---- 快照与历史 ----

func (s *Server) taskSnapshot(rec *taskRecord) *Task {
	return &Task{
		ID:        rec.ID,
		ContextID: rec.ContextID,
		Status:    TaskStatus{State: rec.snapshotState(), Timestamp: nowTimestamp()},
	}
}

// taskSnapshotWithArtifacts 组装含产物与受限历史的 Task 视图。
func (s *Server) taskSnapshotWithArtifacts(rec *taskRecord, historyLen *int) *Task {
	t := s.taskSnapshot(rec)
	rec.mu.Lock()
	if len(rec.artifacts) > 0 {
		t.Artifacts = append([]Artifact(nil), rec.artifacts...)
	}
	rec.mu.Unlock()
	if hist := s.loadHistory(rec.ContextID, historyLen); len(hist) > 0 {
		t.History = hist
	}
	return t
}

func (s *Server) buildTaskResult(rec *taskRecord, historyLen *int) *Task {
	return s.taskSnapshotWithArtifacts(rec, historyLen)
}

// loadHistory 从 sqlite 读取会话消息转为 A2A Message（limit 为最近 N 条）。
func (s *Server) loadHistory(sessionID string, limit *int) []Message {
	rows, err := s.db.Query(
		`SELECT id, role, content FROM message WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var mid, role, content string
		if rows.Scan(&mid, &role, &content) != nil {
			continue
		}
		r := RoleUser
		if role == "assistant" {
			r = RoleAgent
		}
		msgs = append(msgs, Message{
			Role:      r,
			MessageID: mid,
			ContextID: sessionID,
			Parts:     []Part{TextPart(contentText(json.RawMessage(content)))},
		})
	}
	if limit != nil {
		if *limit <= 0 {
			return nil
		}
		if len(msgs) > *limit {
			msgs = msgs[len(msgs)-*limit:]
		}
	}
	return msgs
}

// watcherCount 返回当前 watcher 数（测试用）。
func (t *taskRecord) watcherCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.watchers)
}
