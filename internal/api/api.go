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
	"tars/internal/session"
)

type ctxKey int

const keyInfoKey ctxKey = 0

type Server struct {
	cfg *config.Config
	db  *sql.DB
	log *slog.Logger
	mgr *session.Manager
}

func New(cfg *config.Config, db *sql.DB, log *slog.Logger, mgr *session.Manager) *Server {
	return &Server{cfg: cfg, db: db, log: log, mgr: mgr}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)

	mux.HandleFunc("POST /api/v1/keys", s.authAdmin(s.handleCreateKey))
	mux.HandleFunc("POST /api/v1/session", s.auth(s.handleCreateSession))
	mux.HandleFunc("GET /api/v1/session", s.auth(s.handleListSessions))
	mux.HandleFunc("GET /api/v1/session/{id}", s.auth(s.handleGetSession))
	mux.HandleFunc("DELETE /api/v1/session/{id}", s.auth(s.handleDeleteSession))
	mux.HandleFunc("POST /api/v1/session/{id}/prompt", s.auth(s.handlePrompt))
	mux.HandleFunc("POST /api/v1/session/{id}/interrupt", s.auth(s.handleInterrupt))
	mux.HandleFunc("GET /api/v1/session/{id}/messages", s.auth(s.handleMessages))
	mux.HandleFunc("GET /api/v1/session/{id}/event", s.auth(s.handleEvent))
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

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.1.0", "name": "tars"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"goroutines":      runtime.NumGoroutine(),
		"active_sessions": len(s.mgr.List()),
	})
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

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	ki := keyInfo(r)
	var body struct {
		Cwd   string `json:"cwd"`
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	a, err := s.mgr.Create(ki.KeyID, body.Cwd, body.Model)
	if err != nil {
		s.log.Error("create session", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create session failed"})
		return
	}
	s.log.Info("session created", "session_id", a.ID, "key_id", ki.KeyID)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": a.ID, "cwd": a.Cwd, "model": a.Model})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list := s.mgr.List()
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
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
	_ = a
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
