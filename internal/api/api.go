package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/google/uuid"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/permission"
	"tars/internal/quota"
	"tars/internal/session"
)

type ctxKey int

const keyInfoKey ctxKey = 0

type Server struct {
	cfg   *config.Config
	db    *sql.DB
	log   *slog.Logger
	mgr   *session.Manager
	quota *quota.Checker
}

func New(cfg *config.Config, db *sql.DB, log *slog.Logger, mgr *session.Manager, q *quota.Checker) *Server {
	return &Server{cfg: cfg, db: db, log: log, mgr: mgr, quota: q}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /metrics", s.handleMetricsProm)
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)

	mux.HandleFunc("POST /api/v1/keys", s.authAdmin(s.handleCreateKey))
	mux.HandleFunc("POST /api/v1/session", s.auth(s.handleCreateSession))
	mux.HandleFunc("GET /api/v1/session", s.auth(s.handleListSessions))
	mux.HandleFunc("GET /api/v1/session/{id}", s.auth(s.handleGetSession))
	mux.HandleFunc("DELETE /api/v1/session/{id}", s.auth(s.handleDeleteSession))
	mux.HandleFunc("POST /api/v1/session/{id}/prompt", s.auth(s.handlePrompt))
	mux.HandleFunc("POST /api/v1/session/{id}/interrupt", s.auth(s.handleInterrupt))
	mux.HandleFunc("POST /api/v1/session/{id}/rollback", s.auth(s.handleRollback))
	mux.HandleFunc("GET /api/v1/session/{id}/approvals", s.auth(s.handleListApprovals))
	mux.HandleFunc("POST /api/v1/session/{id}/approval", s.auth(s.handleApproval))
	mux.HandleFunc("GET /api/v1/session/{id}/messages", s.auth(s.handleMessages))
	mux.HandleFunc("GET /api/v1/session/{id}/event", s.auth(s.handleEvent))

	mux.HandleFunc("DELETE /api/v1/keys/{id}", s.authAdmin(s.handleDeleteKey))
	mux.HandleFunc("GET /api/v1/keys/{id}/config", s.auth(s.handleGetKeyConfig))
	mux.HandleFunc("PUT /api/v1/keys/{id}/config", s.auth(s.handlePutKeyConfig))
	mux.HandleFunc("GET /api/v1/keys/{id}/stats", s.auth(s.handleKeyStats))
	mux.HandleFunc("GET /api/v1/keys/{id}/export", s.auth(s.handleKeyExport))
	mux.HandleFunc("DELETE /api/v1/keys/{id}/data", s.auth(s.handleKeyDataDelete))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ki, err := auth.Authenticate(s.db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), keyInfoKey, ki)))
	}
}

func (s *Server) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		ki, _ := r.Context().Value(keyInfoKey).(auth.KeyInfo)
		if ki.Role != auth.RoleAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin only"})
			return
		}
		next(w, r)
	})
}

func keyInfo(r *http.Request) auth.KeyInfo {
	ki, _ := r.Context().Value(keyInfoKey).(auth.KeyInfo)
	return ki
}

func (s *Server) checkRead(w http.ResponseWriter, r *http.Request, sessionKeyID string) bool {
	if !s.cfg.Tenant.ReadIsolation {
		return true
	}
	ki := keyInfo(r)
	if ki.Role == auth.RoleAdmin || ki.KeyID == sessionKeyID {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	return false
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Version 可通过 ldflags -X 注入。
var Version = "0.1.0"

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version, "name": "tars"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"goroutines":      runtime.NumGoroutine(),
		"active_sessions": len(s.mgr.List()),
	})
}

func (s *Server) handleMetricsProm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP tars_goroutines Number of goroutines.\n# TYPE tars_goroutines gauge\ntars_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP tars_active_sessions Number of active sessions.\n# TYPE tars_active_sessions gauge\ntars_active_sessions %d\n", len(s.mgr.List()))
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	plain, keyID, err := auth.CreateKey(s.db, auth.RoleUser)
	if err != nil {
		s.log.Error("create key", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create key failed"})
		return
	}
	s.log.Info("key created", "key_id", keyID, "label", body.Label)
	writeJSON(w, http.StatusCreated, map[string]string{"key_id": keyID, "key": plain})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.Exec(`UPDATE api_keys SET active = 0 WHERE key_id = ?`, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("key revoked", "key_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkKeyAccess(w http.ResponseWriter, r *http.Request, id string) bool {
	ki := keyInfo(r)
	if ki.Role != auth.RoleAdmin && ki.KeyID != id {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return false
	}
	return true
}

func (s *Server) handleGetKeyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	var config string
	err := s.db.QueryRow(`SELECT config FROM key_config WHERE key_id = ?`, id).Scan(&config)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(config))
}

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
	existing := map[string]any{}
	var existingRaw string
	if err := s.db.QueryRow(`SELECT config FROM key_config WHERE key_id = ?`, id).Scan(&existingRaw); err == nil {
		_ = json.Unmarshal([]byte(existingRaw), &existing)
	}
	for k, v := range body {
		existing[k] = v
	}
	merged, _ := json.Marshal(existing)
	_, err := s.db.Exec(
		`INSERT INTO key_config (key_id, config, time_updated) VALUES (?, ?, ?)
		 ON CONFLICT(key_id) DO UPDATE SET config = excluded.config, time_updated = excluded.time_updated`,
		id, string(merged), time.Now().Unix(),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("key config updated", "key_id", id)
	writeJSON(w, http.StatusOK, json.RawMessage(merged))
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ki := keyInfo(r)
	if a.KeyID != ki.KeyID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if err := permission.Rollback(s.db, a.ID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolled_back": true})
}

func (s *Server) handleKeyStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	var sessions, messages, memories, auditCount int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM session WHERE key_id = ?`, id).Scan(&sessions)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE session_id IN (SELECT id FROM session WHERE key_id = ?)`, id).Scan(&messages)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memory WHERE key_id = ?`, id).Scan(&memories)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE client_key = ?`, id).Scan(&auditCount)
	writeJSON(w, http.StatusOK, map[string]int{
		"sessions": sessions,
		"messages": messages,
		"memories": memories,
		"audit":    auditCount,
	})
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := s.db.Query(
		`SELECT id, action, resource, status, created FROM approval WHERE session_id = ? AND status = 'pending' ORDER BY created`,
		id,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, action, resource, status string
		var created int64
		if rows.Scan(&id, &action, &resource, &status, &created) == nil {
			out = append(out, map[string]any{"id": id, "action": action, "resource": resource, "status": status, "created": created})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": out})
}

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ki := keyInfo(r)
	if a.KeyID != ki.KeyID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Decision  string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RequestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requestId and decision required"})
		return
	}
	if body.Decision != "approved" && body.Decision != "denied" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "decision must be approved or denied"})
		return
	}
	if _, err := s.db.Exec(
		`UPDATE approval SET status = ?, resolved = ? WHERE id = ? AND session_id = ? AND status = 'pending'`,
		body.Decision, time.Now().Unix(), body.RequestID, a.ID,
	); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("approval resolved", "session_id", a.ID, "request_id", body.RequestID, "decision", body.Decision)
	writeJSON(w, http.StatusOK, map[string]string{"resolved": body.Decision})
}

func (s *Server) handleKeyExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	type sessRow struct {
		id, cwd, status, model string
	}
	var sessList []sessRow
	rows, err := s.db.Query(`SELECT id, cwd, status, model FROM session WHERE key_id = ?`, id)
	if err == nil {
		for rows.Next() {
			var sr sessRow
			if rows.Scan(&sr.id, &sr.cwd, &sr.status, &sr.model) == nil {
				sessList = append(sessList, sr)
			}
		}
		rows.Close()
	}

	sessions := []map[string]any{}
	for _, sr := range sessList {
		msgs := []map[string]any{}
		mrows, _ := s.db.Query(`SELECT seq, role, content FROM message WHERE session_id = ? ORDER BY seq`, sr.id)
		if mrows != nil {
			for mrows.Next() {
				var seq int64
				var role, content string
				if mrows.Scan(&seq, &role, &content) == nil {
					msgs = append(msgs, map[string]any{"seq": seq, "role": role, "content": json.RawMessage(content)})
				}
			}
			mrows.Close()
		}
		sessions = append(sessions, map[string]any{"id": sr.id, "cwd": sr.cwd, "status": sr.status, "model": sr.model, "messages": msgs})
	}

	memories := []map[string]any{}
	mrows, err := s.db.Query(`SELECT key, content, scope, importance FROM memory WHERE key_id = ?`, id)
	if err == nil {
		for mrows.Next() {
			var k, c, sc string
			var imp int
			if mrows.Scan(&k, &c, &sc, &imp) == nil {
				memories = append(memories, map[string]any{"key": k, "content": c, "scope": sc, "importance": imp})
			}
		}
		mrows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"key_id": id, "sessions": sessions, "memories": memories})
}

func (s *Server) handleKeyDataDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkKeyAccess(w, r, id) {
		return
	}
	for _, a := range s.mgr.List() {
		if a.KeyID == id && a.Status == "running" {
			a.Interrupt()
		}
	}
	if _, err := s.db.Exec(`DELETE FROM session WHERE key_id = ?`, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := s.db.Exec(`DELETE FROM memory WHERE key_id = ?`, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("key data cleared", "key_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	ki := keyInfo(r)
	var body struct {
		Cwd   string `json:"cwd"`
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if s.quota != nil {
		if err := s.quota.CheckCreateSession(ki.KeyID); err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
	}
	a, err := s.mgr.Create(ki.KeyID, body.Cwd, body.Model, ki.Role)
	if err != nil {
		s.log.Error("create session", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create session failed"})
		return
	}
	s.log.Info("session created", "session_id", a.ID, "key_id", ki.KeyID)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": a.ID, "cwd": a.Cwd, "model": a.Model})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ki := keyInfo(r)
	list := s.mgr.List()
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		if s.cfg.Tenant.ReadIsolation && ki.Role != auth.RoleAdmin && a.KeyID != ki.KeyID {
			continue
		}
		out = append(out, map[string]any{"id": a.ID, "key_id": a.KeyID, "cwd": a.Cwd, "status": a.Status, "model": a.Model})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.checkRead(w, r, a.KeyID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "key_id": a.KeyID, "cwd": a.Cwd, "status": a.Status, "model": a.Model})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ki := keyInfo(r)
	if a.KeyID != ki.KeyID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if err := s.mgr.Delete(a.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ki := keyInfo(r)
	if a.KeyID != ki.KeyID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if s.quota != nil {
		if err := s.quota.CheckTurn(ki.KeyID); err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
	}
	var body struct {
		Text  string   `json:"text"`
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
		return
	}
	req := session.PromptReq{
		Text:           body.Text,
		Files:          body.Files,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
	if err := a.Prompt(req); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"turnId": uuid.NewString()})
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	ki := keyInfo(r)
	if a.KeyID != ki.KeyID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	a.Interrupt()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.checkRead(w, r, a.KeyID) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	id := r.PathValue("id")
	rows, err := s.db.Query(
		`SELECT id, seq, role, content FROM message WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		id, after, limit,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	msgs := make([]map[string]any, 0, limit)
	for rows.Next() {
		var mid string
		var seq int64
		var role, content string
		if err := rows.Scan(&mid, &seq, &role, &content); err != nil {
			continue
		}
		msgs = append(msgs, map[string]any{
			"id":      mid,
			"seq":     seq,
			"role":    role,
			"content": json.RawMessage(content),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.checkRead(w, r, a.KeyID) {
		return
	}

	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

	sse(w, fl, session.Event{Type: "server.connected", Data: map[string]string{"version": "0.1.0"}}, 0)

	rows, err := s.db.Query(
		`SELECT id, seq, role, content FROM message WHERE session_id = ? AND seq > ? ORDER BY seq`,
		a.ID, after,
	)
	if err == nil {
		for rows.Next() {
			var mid string
			var seq int64
			var role, content string
			if rows.Scan(&mid, &seq, &role, &content) != nil {
				continue
			}
			sse(w, fl, session.Event{Type: "message.created", Seq: seq, Data: map[string]any{
				"id": mid, "role": role, "content": json.RawMessage(content),
			}}, seq)
		}
		rows.Close()
	}

	sub := a.Subscribe()
	defer a.Unsubscribe(sub)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		case ev, okc := <-sub.Ch:
			if !okc {
				return
			}
			sse(w, fl, ev, ev.Seq)
		}
	}
}

func sse(w http.ResponseWriter, fl http.Flusher, ev session.Event, seq int64) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if seq > 0 {
		fmt.Fprintf(w, "id: %d\n", seq)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
