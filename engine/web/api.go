package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
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
	// static assets (no auth — markup/JS/CSS only, all data behind /api auth)
	mux.HandleFunc("/app.js", s.staticFile("app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/login.js", s.staticFile("login.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/style.css", s.staticFile("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/snapshots", s.requireAuth(s.handleSnapshots))
	mux.HandleFunc("/api/snapshot-ls", s.requireAuth(s.handleSnapshotLs))
	mux.HandleFunc("/api/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
	mux.HandleFunc("/api/excludes", s.requireAuth(s.handleExcludes))
	mux.HandleFunc("/api/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/api/restore", s.requireAuth(s.handleRestore))
	mux.HandleFunc("/api/restore-download", s.requireAuth(s.handleRestoreDownload))
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

// serveUI writes an embedded UI file with the given content type.
// no-store prevents stale cached assets after an upgrade.
func (s *Server) serveUI(w http.ResponseWriter, name, ctype string) {
	b, err := fs.ReadFile(uiFS(), name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *Server) staticFile(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.serveUI(w, name, ctype) }
}

// (serveUI already sets Cache-Control: no-store for all embedded assets.)

// handleRoot gates "/": the dashboard is served only to authenticated users,
// otherwise the browser is redirected to the standalone login page.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, ok := s.currentUser(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.serveUI(w, "index.html", "text/html; charset=utf-8")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// already logged in → go straight to the dashboard
		if _, ok := s.currentUser(r); ok {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		s.serveUI(w, "login.html", "text/html; charset=utf-8")
		return
	}
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
	st["host"] = os.Getenv("HOST_TAG")
	st["scheduler_enabled"] = schedulerEnabled()
	s.writeJSON(w, 200, st)
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	fresh := r.URL.Query().Get("fresh") == "1"
	snaps, err := ListSnapshotsCached(r.Context(), 15*time.Second, fresh)
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

// handleExcludes reads/writes /config/excludes.txt (restic --exclude-file).
// Changes apply on the next backup run; content is opaque exclude patterns.
func (s *Server) handleExcludes(w http.ResponseWriter, r *http.Request) {
	const path = "/config/excludes.txt"
	if r.Method == http.MethodGet {
		b, err := os.ReadFile(path)
		if err != nil {
			b = []byte("")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(b)
		return
	}
	if r.Method == http.MethodPut {
		if !s.checkCSRF(r) {
			http.Error(w, "csrf", 403)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
		if err != nil {
			http.Error(w, "read", 400)
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, body, 0644); err != nil {
			http.Error(w, "save", 500)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "save", 500)
			return
		}
		user, _ := s.currentUser(r)
		s.store.Audit(user, "excludes-update", "ok")
		w.WriteHeader(204)
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
	// Clear the scratch area so the result (and its download) reflects only this restore.
	os.RemoveAll(s.restoreRoot)
	os.MkdirAll(s.restoreRoot, 0755)
	if err := s.runner.RunRestore(s.appCtx, body.Snapshot, safe, body.Includes); err != nil {
		s.store.Audit(user, "restore", "fail")
		s.writeJSON(w, 500, map[string]string{"error": Redact(err.Error())})
		return
	}
	s.store.Audit(user, "restore", "ok")
	s.writeJSON(w, 200, map[string]string{"status": "restored", "target": safe})
}

// handleRestoreDownload streams the current /restore-out contents as a .tar.gz.
func (s *Server) handleRestoreDownload(w http.ResponseWriter, r *http.Request) {
	root := s.restoreRoot
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		http.Error(w, "복원 결과가 없습니다", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="restore-out.tar.gz"`)
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil || rel == "." {
			return nil
		}
		hdr, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, e := os.Open(p)
			if e != nil {
				return nil
			}
			defer f.Close()
			io.Copy(tw, f)
		}
		return nil
	})
	user, _ := s.currentUser(r)
	s.store.Audit(user, "restore-download", "ok")
}
