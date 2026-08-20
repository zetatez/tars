package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"tars/internal/agent"
	"tars/internal/config"
	"tars/internal/log"
	"tars/internal/quota"
)

type Event struct {
	Type string `json:"type"`
	Seq  int64  `json:"seq,omitempty"`
	Data any    `json:"data,omitempty"`
}

type PromptReq struct {
	Text           string   `json:"text"`
	Files          []string `json:"files"`
	Mode           string   `json:"mode"` // "build" | "plan"
	IdempotencyKey string   `json:"-"`
}

type Subscriber struct {
	ID int64
	Ch chan Event
}

type Actor struct {
	ID         string
	KeyID      string
	Cwd        string
	Model      string
	Status     string
	Role       string
	Depth      int
	ClientUser string // 创建该会话的 client 用户名
	ClientIP   string // 创建该会话的 client IP
	promptMode string

	agent *agent.Agent

	dir           string // dataDir/sessions/<id>，会话专属文件夹
	sessLog       *slog.Logger
	sessLogCloser io.Closer

	ch   chan PromptReq
	stop chan struct{}

	mu        sync.Mutex
	subs      map[int64]*Subscriber
	turnC     context.CancelFunc
	nextSeq   int64
	subID     int64
	lastIdem  string
	processed map[string]struct{}

	db  *sql.DB
	log *slog.Logger
}

type Manager struct {
	mu         sync.RWMutex
	sessions   map[string]*Actor
	db         *sql.DB
	log        *slog.Logger
	defaultCwd string
	agent      *agent.Agent
	dataDir    string
	sessCfg    config.Session
	qc         *quota.Checker
	promptMode string
}

const MaxAgentDepth = 3

func NewManager(db *sql.DB, log *slog.Logger, defaultCwd string, ag *agent.Agent, dataDir string, sessCfg config.Session, qc *quota.Checker, promptMode string) *Manager {
	return &Manager{
		sessions:   make(map[string]*Actor),
		db:         db,
		log:        log,
		defaultCwd: defaultCwd,
		agent:      ag,
		dataDir:    dataDir,
		sessCfg:    sessCfg,
		qc:         qc,
		promptMode: promptMode,
	}
}

func (m *Manager) Create(keyID, cwd, model, role string, depth int, promptMode, clientUser, clientIP string) (*Actor, error) {
	if cwd == "" {
		cwd = m.defaultCwd
	}
	if promptMode == "" {
		promptMode = m.promptMode
	}
	id := uuid.NewString()
	now := time.Now().Unix()
	if _, err := m.db.Exec(
		`INSERT INTO session (id, key_id, cwd, env, title, status, model, client_user, client_ip, time_created, time_updated)
		 VALUES (?, ?, ?, NULL, NULL, 'idle', ?, ?, ?, ?, ?)`,
		id, keyID, cwd, model, clientUser, clientIP, now, now,
	); err != nil {
		return nil, err
	}
	a := &Actor{
		ID:         id,
		KeyID:      keyID,
		Cwd:        cwd,
		Model:      model,
		Status:     "idle",
		Role:       role,
		Depth:      depth,
		ClientUser: clientUser,
		ClientIP:   clientIP,
		promptMode: promptMode,
		agent:      m.agent,
		ch:         make(chan PromptReq, 8),
		stop:       make(chan struct{}),
		subs:       make(map[int64]*Subscriber),
		processed:  make(map[string]struct{}),
		db:         m.db,
		log:        m.log,
	}
	var maxSeq int64
	if err := m.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM message WHERE session_id = ?`, id).Scan(&maxSeq); err != nil {
		return nil, err
	}
	a.nextSeq = maxSeq + 1

	// 每个 session 一个独立文件夹：日志（轮转）+ 会话级记忆
	a.dir = m.sessionDir(id)
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		m.log.Warn("session dir", "err", err, "session_id", id)
	}
	if w, err := log.NewRotatingWriter(
		a.dir, "session.log",
		m.sessCfg.LogMaxSizeMB, 0, m.sessCfg.LogMaxBackups,
	); err == nil {
		a.sessLog = slog.New(slog.NewJSONHandler(w, nil))
		a.sessLogCloser = w
	} else {
		m.log.Warn("session log", "err", err, "session_id", id)
	}
	a.persistMemory()

	m.mu.Lock()
	m.sessions[id] = a
	m.mu.Unlock()

	go a.loop()
	return a, nil
}

func (m *Manager) sessionDir(id string) string {
	return filepath.Join(m.dataDir, "sessions", id)
}

// CleanupOrphanedFolders 定期清理“会话已从 DB 删除但文件夹残留”的孤儿目录
// （如 retention 过期删除的会话），保证 dataDir/sessions 与 DB 一致。
func (m *Manager) CleanupOrphanedFolders(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			base := m.sessionDir("")
			entries, err := os.ReadDir(base)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				id := e.Name()
				m.mu.RLock()
				_, inMem := m.sessions[id]
				m.mu.RUnlock()
				if inMem {
					continue
				}
				var one int
				if m.db != nil && m.db.QueryRow(`SELECT 1 FROM session WHERE id = ?`, id).Scan(&one) == nil {
					continue
				}
				_ = os.RemoveAll(filepath.Join(base, id))
				m.log.Info("removed orphaned session folder", "session_id", id)
			}
		}
	}
}

func (m *Manager) Get(id string) (*Actor, bool) {
	m.mu.RLock()
	a, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		return a, true
	}
	return m.resume(id)
}

// resume 在服务重启后从 DB 恢复会话到内存（会话可继续 prompt/订阅）。
func (m *Manager) resume(id string) (*Actor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.sessions[id]; ok {
		return a, true
	}
	if m.db == nil {
		return nil, false
	}
	var keyID, cwd, model, clientUser, clientIP string
	if err := m.db.QueryRow(`SELECT key_id, cwd, model, client_user, client_ip FROM session WHERE id = ?`, id).Scan(&keyID, &cwd, &model, &clientUser, &clientIP); err != nil {
		return nil, false
	}
	role := "user"
	_ = m.db.QueryRow(`SELECT role FROM api_keys WHERE key_id = ?`, keyID).Scan(&role)

	a := &Actor{
		ID:         id,
		KeyID:      keyID,
		Cwd:        cwd,
		Model:      model,
		Status:     "idle", // 重启后无进行中 turn
		Role:       role,
		ClientUser: clientUser,
		ClientIP:   clientIP,
		promptMode: m.promptMode,
		agent:      m.agent,
		ch:         make(chan PromptReq, 8),
		stop:       make(chan struct{}),
		subs:       make(map[int64]*Subscriber),
		processed:  make(map[string]struct{}),
		db:         m.db,
		log:        m.log,
	}
	var maxSeq int64
	if err := m.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM message WHERE session_id = ?`, id).Scan(&maxSeq); err != nil {
		return nil, false
	}
	a.nextSeq = maxSeq + 1

	a.dir = m.sessionDir(id)
	if err := os.MkdirAll(a.dir, 0o755); err == nil {
		if w, err := log.NewRotatingWriter(a.dir, "session.log", m.sessCfg.LogMaxSizeMB, 0, m.sessCfg.LogMaxBackups); err == nil {
			a.sessLog = slog.New(slog.NewJSONHandler(w, nil))
			a.sessLogCloser = w
		}
	}
	// 刷新 DB 中的 status（若上次异常退出遗留 running）
	a.db.Exec(`UPDATE session SET status = 'idle' WHERE id = ?`, id)
	a.persistMemory()

	m.sessions[id] = a
	go a.loop()
	return a, true
}

func (m *Manager) List() []*Actor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Actor, 0, len(m.sessions))
	for _, a := range m.sessions {
		out = append(out, a)
	}
	return out
}

// GlobalSession 描述一个全局会话（跨所有 client），用于顶部状态栏列表。
type GlobalSession struct {
	ID          string `json:"id"`
	KeyID       string `json:"key_id"`
	Cwd         string `json:"cwd"`
	Status      string `json:"status"`
	Model       string `json:"model"`
	ClientUser  string `json:"client_user"`
	ClientIP    string `json:"client_ip"`
	TimeUpdated int64  `json:"time_updated"`
}

// GlobalSessions 按最近活跃返回全局会话列表（分页）。limit<=0 时取全部。
func (m *Manager) GlobalSessions(limit, offset int) ([]GlobalSession, int, error) {
	var total int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM session`).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id, key_id, cwd, status, model, COALESCE(client_user,''), COALESCE(client_ip,''), time_updated
	      FROM session ORDER BY time_updated DESC, id`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := m.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]GlobalSession, 0, limit)
	for rows.Next() {
		var g GlobalSession
		if err := rows.Scan(&g.ID, &g.KeyID, &g.Cwd, &g.Status, &g.Model, &g.ClientUser, &g.ClientIP, &g.TimeUpdated); err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

func (m *Manager) Delete(id string) error {
	a, ok := m.Get(id)
	if !ok {
		return errors.New("session not found")
	}
	close(a.stop)
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	if a.sessLogCloser != nil {
		a.sessLogCloser.Close()
	}
	// 清理该 session 专属文件夹（日志 + 记忆）
	if a.dir != "" {
		_ = os.RemoveAll(a.dir)
	}
	_, err := m.db.Exec(`DELETE FROM session WHERE id = ?`, id)
	return err
}

func (m *Manager) RunSync(keyID, cwd, model, role, text string, depth int) (string, error) {
	if depth > MaxAgentDepth {
		return "", errors.New("max agent nesting depth exceeded")
	}
	if m.qc != nil {
		if err := m.qc.CheckCreateSession(keyID); err != nil {
			return "", err
		}
		if err := m.qc.CheckTurn(keyID); err != nil {
			return "", err
		}
	}
	a, err := m.Create(keyID, cwd, model, role, depth, "", "", "")
	if err != nil {
		return "", err
	}
	defer m.Delete(a.ID)

	sub := a.Subscribe()
	defer a.Unsubscribe(sub)

	if err := a.Prompt(PromptReq{Text: text}); err != nil {
		return "", err
	}

	for ev := range sub.Ch {
		if ev.Type == "turn.done" || ev.Type == "turn.failed" {
			break
		}
	}

	var content string
	if err := m.db.QueryRow(
		`SELECT content FROM message WHERE session_id = ? AND role = 'assistant' ORDER BY seq DESC LIMIT 1`,
		a.ID,
	).Scan(&content); err != nil {
		return "", err
	}
	var c struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal([]byte(content), &c)
	if c.Error != "" {
		return "", errors.New(c.Error)
	}
	return c.Text, nil
}

func (a *Actor) loop() {
	for {
		select {
		case <-a.stop:
			return
		case req := <-a.ch:
			a.runTurn(req)
		}
	}
}

func (a *Actor) Prompt(req PromptReq) error {
	select {
	case <-a.stop:
		return errors.New("session closed")
	default:
	}
	a.mu.Lock()
	if req.IdempotencyKey != "" {
		if _, dup := a.processed[req.IdempotencyKey]; dup {
			a.mu.Unlock()
			return errors.New("duplicate idempotency key")
		}
		a.processed[req.IdempotencyKey] = struct{}{}
	}
	if a.promptMode != "queue" && a.turnC != nil {
		a.turnC()
	}
	a.mu.Unlock()
	select {
	case a.ch <- req:
		return nil
	case <-a.stop:
		return errors.New("session closed")
	}
}

func (a *Actor) Interrupt() {
	a.mu.Lock()
	if a.turnC != nil {
		a.turnC()
	}
	a.mu.Unlock()
}

func (a *Actor) runTurn(req PromptReq) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.turnC = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.turnC = nil
		a.mu.Unlock()
	}()

	a.setStatus("running")
	a.broadcast(Event{Type: "turn.started"})
	if a.sessLog != nil {
		a.sessLog.Info("turn.started", "text", req.Text, "files", req.Files)
	}

	if a.agent != nil {
		a.agent.RunTurn(ctx, a, agent.PromptReq{Text: req.Text, Files: req.Files, Mode: req.Mode})
	} else {
		a.appendMessage("assistant", map[string]any{"v": 1, "text": "agent 未配置"})
	}

	if a.sessLog != nil {
		a.sessLog.Info("turn.finished")
	}
	// 每轮结束将本会话（含该 key 的全局/工作区）记忆快照持久化到会话文件夹
	a.persistMemory()
	a.setStatus("idle")
}

func (a *Actor) SessionID() string    { return a.ID }
func (a *Actor) SessionKeyID() string { return a.KeyID }
func (a *Actor) SessionCwd() string   { return a.Cwd }
func (a *Actor) SessionModel() string { return a.Model }

// SetModel 更新会话模型（下次 turn 生效），同步写回 DB
func (a *Actor) SetModel(model string) error {
	a.Model = model
	_, err := a.db.Exec(`UPDATE session SET model = ?, time_updated = ? WHERE id = ?`, model, time.Now().Unix(), a.ID)
	return err
}
func (a *Actor) SessionRole() string       { return a.Role }
func (a *Actor) SessionDepth() int         { return a.Depth }
func (a *Actor) SessionClientUser() string { return a.ClientUser }
func (a *Actor) SessionClientIP() string   { return a.ClientIP }
func (a *Actor) Append(role string, content any) {
	a.appendMessage(role, content)
}
func (a *Actor) Notify(typ string, data any) {
	if a.sessLog != nil {
		switch typ {
		case "approval.requested", "approval.resolved", "turn.done", "turn.failed", "session.updated":
			a.sessLog.Info("event."+typ, "data", data)
		}
	}
	a.broadcast(Event{Type: typ, Data: data})
}

func (a *Actor) appendMessage(role string, content any) {
	b, err := json.Marshal(content)
	if err != nil {
		a.log.Error("marshal message", "err", err)
		return
	}
	seq := a.nextSeq
	id := uuid.NewString()
	if _, err := a.db.Exec(
		`INSERT INTO message (id, session_id, seq, role, content, created) VALUES (?, ?, ?, ?, ?, ?)`,
		id, a.ID, seq, role, string(b), time.Now().Unix(),
	); err != nil {
		a.log.Error("append message", "err", err)
		return
	}
	a.nextSeq++
	// 完整输入输出记录到会话日志
	if a.sessLog != nil {
		a.sessLog.Info("message", "seq", seq, "role", role, "content", content)
	}
	a.broadcast(Event{Type: "message.created", Seq: seq, Data: map[string]any{"id": id, "role": role, "content": content}})
}

type memoryEntry struct {
	Key        string `json:"key"`
	Content    string `json:"content"`
	Scope      string `json:"scope"`
	Importance int    `json:"importance"`
	Source     string `json:"source"`
	Updated    int64  `json:"time_updated"`
}

// persistMemory 将会话级记忆（含该 key 的 global/workspace 记忆）快照到会话文件夹，
// 用于会话快速恢复与人工/离线检查。
func (a *Actor) persistMemory() {
	if a.db == nil || a.dir == "" {
		return
	}
	rows, err := a.db.Query(
		`SELECT key, content, scope, importance, source, time_updated FROM memory
		 WHERE key_id = ? AND (scope IN ('global','workspace') OR (scope = 'session' AND session_id = ?))
		 ORDER BY CASE scope WHEN 'session' THEN 0 WHEN 'workspace' THEN 1 ELSE 2 END, importance DESC, time_updated DESC`,
		a.KeyID, a.ID,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	entries := make([]memoryEntry, 0, 8)
	for rows.Next() {
		var e memoryEntry
		if rows.Scan(&e.Key, &e.Content, &e.Scope, &e.Importance, &e.Source, &e.Updated) == nil {
			entries = append(entries, e)
		}
	}
	if rows.Err() != nil {
		return
	}

	jsonData, err := json.MarshalIndent(map[string]any{
		"session_id": a.ID,
		"key_id":     a.KeyID,
		"updated":    time.Now().Unix(),
		"entries":    entries,
	}, "", "  ")
	if err != nil {
		return
	}
	writeFileAtomic(filepath.Join(a.dir, "memory.json"), jsonData)

	md := a.renderMemoryMarkdown(entries)
	writeFileAtomic(filepath.Join(a.dir, "memory.md"), []byte(md))
}

func (a *Actor) renderMemoryMarkdown(entries []memoryEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Session Memory\n\n")
	fmt.Fprintf(&sb, "- session: `%s`\n- key: `%s`\n- updated: %s\n\n", a.ID, a.KeyID, time.Now().Format(time.RFC3339))
	if len(entries) == 0 {
		sb.WriteString("（暂无记忆）\n")
		return sb.String()
	}
	lastScope := ""
	for _, e := range entries {
		if e.Scope != lastScope {
			if lastScope != "" {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "## %s\n\n", e.Scope)
			lastScope = e.Scope
		}
		fmt.Fprintf(&sb, "- **[%s]** %s (importance=%d, source=%s)\n", e.Key, e.Content, e.Importance, e.Source)
	}
	return sb.String()
}

func writeFileAtomic(path string, data []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (a *Actor) setStatus(status string) {
	a.mu.Lock()
	a.Status = status
	a.mu.Unlock()
	a.db.Exec(`UPDATE session SET status = ?, time_updated = ? WHERE id = ?`, status, time.Now().Unix(), a.ID)
	a.broadcast(Event{Type: "session.updated", Data: map[string]any{"status": status}})
}

func (a *Actor) Subscribe() *Subscriber {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subID++
	s := &Subscriber{ID: a.subID, Ch: make(chan Event, 256)}
	a.subs[s.ID] = s
	return s
}

func (a *Actor) Unsubscribe(s *Subscriber) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur, ok := a.subs[s.ID]; ok {
		delete(a.subs, s.ID)
		close(cur.Ch)
	}
}

func (a *Actor) broadcast(ev Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, sub := range a.subs {
		select {
		case sub.Ch <- ev:
		default:
			close(sub.Ch)
			delete(a.subs, id)
		}
	}
}
