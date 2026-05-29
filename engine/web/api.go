package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	store       *Store
	runner      *Runner
	cfgPath     string
	logDir      string
	sessionKey  []byte
	adminUser   string
	adminHash   string
	reload      func()
	restoreRoot string
	appCtx      context.Context
}

func (s *Server) sessionVer() string { return sessionVersion(s.adminHash) }

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie("session")
	if err != nil {
		return "", false
	}
	return verifySession(c.Value, s.sessionKey, time.Now().Unix(), s.sessionVer())
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentUser(r); !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		next(w, r)
	}
}

// checkCSRF validates the double-submit token plus a strict same-origin check.
func (s *Server) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie("csrf")
	if err != nil || c.Value == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(c.Value)) != 1 {
		return false
	}
	// Strict same-origin: exact Host match parsed from Origin, else Referer.
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && u.Host == r.Host
	}
	// No Origin and no Referer on a state-changing request: reject.
	return false
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/snapshots", s.requireAuth(s.handleSnapshots))
	mux.HandleFunc("/api/snapshot-ls", s.requireAuth(s.handleSnapshotLs))
	mux.HandleFunc("/api/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/api/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/api/restore", s.requireAuth(s.handleRestore))
	mux.Handle("/", http.FileServer(http.FS(uiFS())))
	return mux
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	var body struct{ User, Pass string }
	json.NewDecoder(r.Body).Decode(&body)
	if body.User != s.adminUser || s.adminHash == "" || !checkPassword(s.adminHash, body.Pass) {
		time.Sleep(time.Second)
		s.store.Audit(body.User, "login", "fail")
		http.Error(w, "invalid credentials", 401)
		return
	}
	tok := signSession(body.User, time.Now().Unix(), s.sessionVer(), s.sessionKey)
	http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	csrf := randToken()
	http.SetCookie(w, &http.Cookie{Name: "csrf", Value: csrf, Path: "/", Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	s.store.Audit(body.User, "login", "ok")
	s.writeJSON(w, 200, map[string]string{"csrf": csrf})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(204)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := map[string]any{"busy": s.runner.Busy()}
	if b, err := os.ReadFile("/state/last-success"); err == nil {
		st["last_success"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/state/last-failure"); err == nil {
		st["last_failure"] = strings.TrimSpace(string(b))
	}
	if n := nextRun(); n != "" {
		st["next_run"] = n
	}
	s.writeJSON(w, 200, st)
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := ListSnapshots(r.Context())
	if err != nil {
		s.writeJSON(w, 502, map[string]string{"error": Redact(err.Error())})
		return
	}
	s.writeJSON(w, 200, snaps)
}

func (s *Server) handleSnapshotLs(w http.ResponseWriter, r *http.Request) {
	snap := r.URL.Query().Get("id")
	path := r.URL.Query().Get("path")
	if !validSnapID(snap) || !safeResticPath(path) {
		http.Error(w, "bad params", 400)
		return
	}
	if path == "" {
		path = "/"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "restic", "ls", snap, path).Output()
	if err != nil {
		s.writeJSON(w, 502, map[string]string{"error": "ls failed"})
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 500 {
		lines = lines[:500]
	}
	s.writeJSON(w, 200, map[string]any{"entries": lines})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, e := strconv.Atoi(l); e == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	runs, err := s.store.ListRuns(limit, 0)
	if err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "db"})
		return
	}
	s.writeJSON(w, 200, runs)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if err != nil {
		http.Error(w, "bad run id", 400)
		return
	}
	run, err := s.store.GetRun(id)
	if err != nil || run.LogPath == "" {
		http.Error(w, "no log", 404)
		return
	}
	// Confine log path to the log dir (defense in depth).
	clean := filepath.Clean(run.LogPath)
	if !strings.HasPrefix(clean, s.logDir+string(os.PathSeparator)) {
		http.Error(w, "forbidden", 403)
		return
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		http.Error(w, "log expired", 404)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(Redact(string(b))))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		c, err := LoadConfig(s.cfgPath)
		if err != nil {
			s.writeJSON(w, 500, map[string]string{"error": "cfg"})
			return
		}
		s.writeJSON(w, 200, map[string]any{
			"keep_daily": c.KeepDaily, "upload_limit_kbps": c.UploadLimit,
			"backup_schedule": c.BackupSchedule, "check_schedule": c.CheckSchedule,
			"scheduler_enabled": c.SchedulerOn, "db_backup_enabled": c.DBBackupEnabled,
			"min_free_mb": c.MinFreeMB,
		})
		return
	}
	if r.Method == "PUT" {
		if !s.checkCSRF(r) {
			http.Error(w, "csrf", 403)
			return
		}
		c, err := LoadConfig(s.cfgPath)
		if err != nil {
			http.Error(w, "cfg", 500)
			return
		}
		var body struct {
			KeepDaily, UploadLimit, MinFreeMB     int
			BackupSchedule, CheckSchedule         string
			SchedulerEnabled, DBBackupEnabled bool
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		c.KeepDaily = body.KeepDaily
		c.UploadLimit = body.UploadLimit
		c.MinFreeMB = body.MinFreeMB
		c.BackupSchedule = body.BackupSchedule
		c.CheckSchedule = body.CheckSchedule
		c.SchedulerOn = body.SchedulerEnabled
		c.DBBackupEnabled = body.DBBackupEnabled
		if err := c.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := c.Save(s.cfgPath); err != nil {
			http.Error(w, "save", 500)
			return
		}
		user, _ := s.currentUser(r)
		s.store.Audit(user, "config-update", "ok")
		if s.reload != nil {
			s.reload()
		}
		s.writeJSON(w, 200, map[string]string{"status": "saved"})
		return
	}
	http.Error(w, "method", 405)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	user, _ := s.currentUser(r)
	if err := s.runner.RunBackup(s.appCtx, "manual"); err != nil {
		s.store.Audit(user, "backup", "busy")
		http.Error(w, "busy", 409)
		return
	}
	s.store.Audit(user, "backup", "started")
	s.writeJSON(w, 202, map[string]string{"status": "started"})
}

// handleRestore: dangerous action — requires CSRF + password re-auth + typed confirmation + target allowlist.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	user, _ := s.currentUser(r)
	var body struct {
		Snapshot string
		Target   string
		Includes []string
		Password string
		Confirm  string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if body.Confirm != "RESTORE" {
		http.Error(w, "confirmation phrase required", 400)
		return
	}
	if !checkPassword(s.adminHash, body.Password) {
		time.Sleep(time.Second)
		s.store.Audit(user, "restore", "reauth-fail")
		http.Error(w, "password re-auth failed", 401)
		return
	}
	if !validSnapID(body.Snapshot) {
		http.Error(w, "bad snapshot id", 400)
		return
	}
	target := body.Target
	if target == "" {
		target = s.restoreRoot
	}
	safe, err := safeRestoreTarget(s.restoreRoot, target)
	if err != nil {
		s.store.Audit(user, "restore", "bad-target")
		http.Error(w, "invalid target: "+err.Error(), 400)
		return
	}
	for _, inc := range body.Includes {
		if !safeResticPath(inc) {
			http.Error(w, "invalid include path", 400)
			return
		}
	}
	if err := s.runner.RunRestore(s.appCtx, body.Snapshot, safe, body.Includes); err != nil {
		s.store.Audit(user, "restore", "fail")
		s.writeJSON(w, 500, map[string]string{"error": Redact(err.Error())})
		return
	}
	s.store.Audit(user, "restore", "ok")
	s.writeJSON(w, 200, map[string]string{"status": "restored", "target": safe})
}
