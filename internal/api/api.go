package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/llm"
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
	llm   *llm.Pool
}

func New(cfg *config.Config, db *sql.DB, log *slog.Logger, mgr *session.Manager, q *quota.Checker, llm *llm.Pool) *Server {
	return &Server{cfg: cfg, db: db, log: log, mgr: mgr, quota: q, llm: llm}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /metrics", s.handleMetricsProm)
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)
	// 无鉴权：暴露机器指纹，供客户端派生 admin key（machine-id 非机密，密钥仍需口令）
	mux.HandleFunc("GET /api/v1/machine-id", s.handleMachineID)

	mux.HandleFunc("POST /api/v1/keys", s.authAdmin(s.handleCreateKey))
	mux.HandleFunc("POST /api/v1/session", s.auth(s.handleCreateSession))
	mux.HandleFunc("GET /api/v1/session", s.auth(s.handleListSessions))
	mux.HandleFunc("GET /api/v1/sessions", s.auth(s.handleGlobalSessions))
	mux.HandleFunc("GET /api/v1/session/{id}", s.auth(s.handleGetSession))
	mux.HandleFunc("PATCH /api/v1/session/{id}", s.auth(s.handleUpdateSession))
	mux.HandleFunc("DELETE /api/v1/session/{id}", s.auth(s.handleDeleteSession))
	mux.HandleFunc("GET /api/v1/models", s.auth(s.handleModels))
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
	if !s.cfg.ReadIsolation {
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

// handleMachineID 返回机器指纹（无鉴权）。machine-id 本身非机密，
// admin key 还需管理员口令才能派生，因此可直接暴露。
func (s *Server) handleMachineID(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"machine_id": auth.MachineID()})
}

// Version 可通过 ldflags -X 注入。
var Version = "0.1.0"

func serverIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && !ip.IP.IsLoopback() && ip.IP.To4() != nil {
			return ip.IP.String()
		}
	}
	return ""
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  Version,
		"name":     "tars",
		"hostname": hostname,
		"ip":       serverIP(),
	})
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
		Cwd        string `json:"cwd"`
		Model      string `json:"model"`
		PromptMode string `json:"prompt_mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.PromptMode == "" {
		body.PromptMode = s.cfg.PromptMode
	}
	if s.quota != nil {
		if err := s.quota.CheckCreateSession(ki.KeyID); err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
	}
	a, err := s.mgr.Create(ki.KeyID, body.Cwd, body.Model, ki.Role, 0, body.PromptMode, r.Header.Get("X-Client-User"), clientIP(r))
	if err != nil {
		s.log.Error("create session", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create session failed"})
		return
	}
	s.log.Info("session created", "session_id", a.ID, "key_id", ki.KeyID, "client_user", a.ClientUser, "client_ip", a.ClientIP)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": a.ID, "cwd": a.Cwd, "model": a.Model})
}

// clientIP 返回请求来源 IP（去掉端口；保留 X-Forwarded-For 直连场景兜底）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if host, _, err := net.SplitHostPort(xff); err == nil {
			return host
		}
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ki := keyInfo(r)
	list := s.mgr.List()
	// 按最近活跃排序（内存 Actor.TimeUpdated，随 setStatus/Create 更新）
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].SessionTimeUpdated() > list[j].SessionTimeUpdated()
	})
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		if s.cfg.ReadIsolation && ki.Role != auth.RoleAdmin && a.KeyID != ki.KeyID {
			continue
		}
		out = append(out, map[string]any{"id": a.ID, "key_id": a.KeyID, "cwd": a.Cwd, "status": a.Status, "model": a.Model, "client_user": a.ClientUser, "client_ip": a.ClientIP, "time_updated": a.TimeUpdated})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleGlobalSessions 返回全局活跃会话（跨所有 client），按最近活跃排序、可翻页；
// access 标注当前 key 是否可写（rw/ro）。
func (s *Server) handleGlobalSessions(w http.ResponseWriter, r *http.Request) {
	ki := keyInfo(r)
	limit := 8
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	list, total, err := s.mgr.GlobalSessions(limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, g := range list {
		mine := ki.Role == auth.RoleAdmin || ki.KeyID == g.KeyID
		access := "ro"
		if mine {
			access = "rw"
		}
		out = append(out, map[string]any{
			"id": g.ID, "key_id": g.KeyID, "cwd": g.Cwd, "status": g.Status, "model": g.Model,
			"client_user": g.ClientUser, "client_ip": g.ClientIP, "time_updated": g.TimeUpdated,
			"mine": mine, "access": access,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "total": total, "limit": limit, "offset": offset})
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
	provider, model := s.resolveSessionModel(a.Model)
	writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "key_id": a.KeyID, "cwd": a.Cwd, "status": a.Status, "model": model, "provider": provider, "client_user": a.ClientUser, "client_ip": a.ClientIP})
}

// resolveSessionModel 返回会话生效的 provider 名与 model（model 为空时用后端默认）
func (s *Server) resolveSessionModel(model string) (string, string) {
	if s.llm != nil {
		return s.llm.Resolve(model)
	}
	if model == "" {
		model = s.cfg.Agent.Model
	}
	return "", model
}

// handleModels 列出可用 provider 与模型（供 /models 选择）
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type m struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	out := []m{}
	for _, p := range s.cfg.LLM.Providers {
		if p.Model != "" {
			out = append(out, m{Provider: p.Name, Model: p.Model})
		}
	}
	found := false
	for _, mm := range out {
		if mm.Model == s.cfg.Agent.Model {
			found = true
			break
		}
	}
	if s.cfg.Agent.Model != "" && !found {
		out = append(out, m{Provider: "", Model: s.cfg.Agent.Model})
	}
	if len(out) == 0 && s.llm != nil {
		p, model := s.llm.Resolve("")
		if model != "" {
			out = append(out, m{Provider: p, Model: model})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out, "default": s.cfg.Agent.Model})
}

// handleUpdateSession 更新会话（目前仅 model）
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
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
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if body.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model required"})
		return
	}
	if err := a.SetModel(body.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("session model updated", "session_id", a.ID, "model", body.Model)
	writeJSON(w, http.StatusOK, map[string]any{"model": body.Model})
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
		Mode  string   `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
		return
	}
	if body.Mode != "" && body.Mode != "build" && body.Mode != "plan" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be build or plan"})
		return
	}
	req := session.PromptReq{
		Text:           body.Text,
		Files:          body.Files,
		Mode:           body.Mode,
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
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	id := r.PathValue("id")
	var rows *sql.Rows
	var err error
	if before > 0 {
		// before 模式：取 seq < before 的最早 limit 条（倒序查询再翻转），供上拉加载更多
		rows, err = s.db.Query(
			`SELECT id, seq, role, content, created FROM message WHERE session_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`,
			id, before, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, seq, role, content, created FROM message WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
			id, after, limit,
		)
	}
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
		var created int64
		if err := rows.Scan(&mid, &seq, &role, &content, &created); err != nil {
			continue
		}
		msgs = append(msgs, map[string]any{
			"id":      mid,
			"seq":     seq,
			"role":    role,
			"content": json.RawMessage(content),
			"created": created,
		})
	}
	// before 模式倒序查询，翻转为升序返回
	if before > 0 {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
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
		`SELECT id, seq, role, content, created FROM message WHERE session_id = ? AND seq > ? ORDER BY seq`,
		a.ID, after,
	)
	if err == nil {
		for rows.Next() {
			var mid string
			var seq int64
			var role, content string
			var created int64
			if rows.Scan(&mid, &seq, &role, &content, &created) != nil {
				continue
			}
			sse(w, fl, session.Event{Type: "message.created", Seq: seq, Data: map[string]any{
				"id": mid, "role": role, "content": json.RawMessage(content), "created": created,
			}}, seq)
		}
		rows.Close()
	}

	// 重放当前 pending 的审批，避免客户端在 approval.requested 广播前才建立订阅而错过
	if rows2, err := s.db.Query(
		`SELECT id, action, resource FROM approval WHERE session_id = ? AND status = 'pending' ORDER BY created`,
		a.ID,
	); err == nil {
		for rows2.Next() {
			var aid, action, resource string
			if rows2.Scan(&aid, &action, &resource) == nil {
				sse(w, fl, session.Event{Type: "approval.requested", Data: map[string]any{"id": aid, "action": action, "resource": resource}}, 0)
			}
		}
		rows2.Close()
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
