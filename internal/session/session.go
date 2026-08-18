package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
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
	promptMode string

	agent *agent.Agent

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

func (m *Manager) Create(keyID, cwd, model, role string, depth int, promptMode string) (*Actor, error) {
	if cwd == "" {
		cwd = m.defaultCwd
	}
	if promptMode == "" {
		promptMode = m.promptMode
	}
	id := uuid.NewString()
	now := time.Now().Unix()
	if _, err := m.db.Exec(
		`INSERT INTO session (id, key_id, cwd, env, title, status, model, time_created, time_updated)
		 VALUES (?, ?, ?, NULL, NULL, 'idle', ?, ?, ?)`,
		id, keyID, cwd, model, now, now,
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

	if w, err := log.NewRotatingWriter(
		filepath.Join(m.dataDir, "logs", "sessions"), id+".log",
		m.sessCfg.LogMaxSizeMB, 0, m.sessCfg.LogMaxBackups,
	); err == nil {
		a.sessLog = slog.New(slog.NewJSONHandler(w, nil))
		a.sessLogCloser = w
	}

	m.mu.Lock()
	m.sessions[id] = a
	m.mu.Unlock()

	go a.loop()
	return a, nil
}

func (m *Manager) Get(id string) (*Actor, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.sessions[id]
	return a, ok
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
	a, err := m.Create(keyID, cwd, model, role, depth, "")
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
		a.sessLog.Info("turn.started", "text", req.Text)
	}

	if a.agent != nil {
		a.agent.RunTurn(ctx, a, agent.PromptReq{Text: req.Text, Files: req.Files})
	} else {
		a.appendMessage("assistant", map[string]any{"v": 1, "text": "agent 未配置"})
	}

	if a.sessLog != nil {
		a.sessLog.Info("turn.finished")
	}
	a.setStatus("idle")
}

func (a *Actor) SessionID() string    { return a.ID }
func (a *Actor) SessionKeyID() string { return a.KeyID }
func (a *Actor) SessionCwd() string   { return a.Cwd }
func (a *Actor) SessionModel() string { return a.Model }
func (a *Actor) SessionRole() string  { return a.Role }
func (a *Actor) SessionDepth() int    { return a.Depth }
func (a *Actor) Append(role string, content any) {
	a.appendMessage(role, content)
}
func (a *Actor) Notify(typ string, data any) {
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
	a.broadcast(Event{Type: "message.created", Seq: seq, Data: map[string]any{"id": id, "role": role, "content": content}})
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
