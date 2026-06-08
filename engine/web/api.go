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
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	migrator    *Migrator
	appCtx      context.Context
}

func (s *Server) sessionVer() string { return sessionVersion(s.adminHash) }

// isHTTPS reports whether the request arrived over TLS, directly or via a
// trusted reverse proxy setting X-Forwarded-Proto=https. Lets cookies be
// marked Secure when fronted by TLS without mandating it on a LAN homelab.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

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
	mux.HandleFunc("/api/rclone-gui", s.requireAuth(s.handleRcloneGUI))
	mux.HandleFunc("/api/remote-path", s.requireAuth(s.handleRemotePath))
	mux.HandleFunc("/api/remote-ls", s.requireAuth(s.handleRemoteLs))
	mux.HandleFunc("/api/remote-migrate", s.requireAuth(s.handleRemoteMigrate))
	mux.HandleFunc("/api/rclone-remotes", s.requireAuth(s.handleRcloneRemotes))
	mux.HandleFunc("/api/rclone-add", s.requireAuth(s.handleRcloneAdd))
	mux.HandleFunc("/api/rclone-cli", s.requireAuth(s.handleRcloneCLI))
	mux.HandleFunc("/rclone-gui/", s.requireAuth(s.handleRcloneGUIProxy))
	mux.HandleFunc("/ws/terminal", s.requireAuth(s.handleTerminalWS))
	mux.HandleFunc("/vendor/", s.requireAuth(s.handleVendor))
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

// handleVendor serves embedded third-party assets (xterm.js) under /vendor/.
func (s *Server) handleVendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.HasPrefix(name, "vendor/") || strings.Contains(name, "..") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ct := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".js"):
		ct = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		ct = "text/css; charset=utf-8"
	}
	s.serveUI(w, name, ct)
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
	secure := isHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: "session", Value: tok, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	csrf := randToken()
	http.SetCookie(w, &http.Cookie{Name: "csrf", Value: csrf, Path: "/", Secure: secure, SameSite: http.SameSiteStrictMode})
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

// lsCache memoizes snapshot-ls results. Snapshots are immutable and the picker
// always passes a concrete short_id (never "latest"), so a directory's listing
// never changes — caching for the process lifetime makes re-navigating (and
// walking back up the breadcrumb) instant despite each restic ls costing ~10s
// over rclone→Drive.
var (
	lsCacheMu sync.Mutex
	lsCache   = map[string][]byte{}
)

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
	cacheKey := snap + "\x00" + path
	lsCacheMu.Lock()
	cached, ok := lsCache[cacheKey]
	lsCacheMu.Unlock()
	if ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// `restic ls` is non-recursive by default → exactly one directory level,
	// which is what the path-picker browses. --json gives dir/file + size.
	out, err := exec.CommandContext(ctx, "restic", "ls", "--json", snap, path).Output()
	if err != nil {
		s.writeJSON(w, 502, map[string]string{"error": "ls failed"})
		return
	}
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var entries []entry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var n struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			Path       string `json:"path"`
			Size       int64  `json:"size"`
			StructType string `json:"struct_type"`
		}
		if json.Unmarshal([]byte(line), &n) != nil || n.StructType != "node" {
			continue // skip the leading snapshot object
		}
		if n.Path == path {
			continue // restic lists the directory itself; show only its children
		}
		entries = append(entries, entry{Name: n.Name, Type: n.Type, Path: n.Path, Size: n.Size})
		if len(entries) >= 1000 {
			break
		}
	}
	// directories first, then files; each alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Name < entries[j].Name
	})
	resp, _ := json.Marshal(map[string]any{"path": path, "entries": entries})
	lsCacheMu.Lock()
	lsCache[cacheKey] = resp
	lsCacheMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
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

// handleRcloneAdd creates a non-OAuth rclone remote from the Korean form via a
// one-off `rclone config create`. Validates name + type + param keys; values are
// passed as argv (no shell), passwords obscured by rclone.
func (s *Server) handleRcloneAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	var body struct {
		Name, Type string
		Params     map[string]string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if body.Name == "" || !identRe.MatchString(body.Name) || len(body.Name) > 64 {
		http.Error(w, "이름은 영문/숫자/_-. 만 가능", 400)
		return
	}
	allowed, ok := rcloneBackends[body.Type]
	if !ok {
		http.Error(w, "지원하지 않는 유형", 400)
		return
	}
	inAllowed := func(k string) bool {
		for _, a := range allowed {
			if a == k {
				return true
			}
		}
		return false
	}
	var params [][2]string
	for k, v := range body.Params {
		if !inAllowed(k) {
			http.Error(w, "허용되지 않은 항목: "+k, 400)
			return
		}
		if strings.ContainsAny(v, "\n\r") {
			http.Error(w, "값에 줄바꿈 불가", 400)
			return
		}
		params = append(params, [2]string{k, v})
	}
	user, _ := s.currentUser(r)
	if err := createRemote(body.Name, body.Type, params); err != nil {
		s.store.Audit(user, "rclone-add", "fail")
		s.writeJSON(w, 500, map[string]string{"error": Redact(err.Error())})
		return
	}
	s.store.Audit(user, "rclone-add", "ok")
	s.writeJSON(w, 200, map[string]string{"status": "created", "name": body.Name})
}

// handleRcloneCLI runs a whitelisted rclone subcommand and returns its output.
func (s *Server) handleRcloneCLI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	var body struct{ Cmd string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	args := splitArgs(strings.TrimSpace(body.Cmd))
	if len(args) == 0 {
		http.Error(w, "명령을 입력하세요", 400)
		return
	}
	sub := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			sub = a
			break
		}
	}
	if !rcloneCLIAllowed[sub] {
		http.Error(w, "허용되지 않은 명령: "+sub+" (조회·설정 명령만 가능; 삭제/전송류 차단)", 400)
		return
	}
	// Bare/interactive `config` needs step-by-step input which a one-shot runner
	// can't provide — guide the user to the non-interactive subcommands / GUI.
	if sub == "config" {
		csub := ""
		seen := false
		for _, a := range args {
			if a == "config" {
				seen = true
				continue
			}
			if seen && !strings.HasPrefix(a, "-") {
				csub = a
				break
			}
		}
		if csub == "" || csub == "edit" || csub == "reconnect" {
			http.Error(w, "대화형 'rclone config' 마법사는 단계별 입력이 필요해 이 CLI에서는 실행할 수 없습니다.\n한 줄 명령을 쓰세요: config create / update / delete / show / dump / redacted / providers\n예) config create mynas webdav url=https://nas:5006 vendor=other user=U pass=P\n또는 '목적지 추가' 폼(간편) 이나 'rclone 설정 열기'(GUI)를 이용하세요.", 400)
			return
		}
	}
	user, _ := s.currentUser(r)
	out, _ := runRcloneCLI(r.Context(), args)
	s.store.Audit(user, "rclone-cli:"+sub, "run")
	s.writeJSON(w, 200, map[string]string{"output": out})
}

// handleRcloneGUIProxy reverse-proxies /rclone-gui/* to the GUI container on the
// docker network, injecting its basic-auth so the browser (already authenticated
// to the dashboard) never sees the rclone password. Same-origin → embeddable in a
// modal iframe. Returns 503 when the GUI is not running.
func (s *Server) handleRcloneGUIProxy(w http.ResponseWriter, r *http.Request) {
	if !rcloneGUIActive() {
		http.Error(w, "rclone 설정 화면이 실행 중이 아닙니다", http.StatusServiceUnavailable)
		return
	}
	target, _ := url.Parse("http://" + rgName + ":5572")
	px := httputil.NewSingleHostReverseProxy(target)
	base := px.Director
	px.Director = func(req *http.Request) {
		base(req)
		req.Host = target.Host
	}
	px.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		resp.Header.Del("Content-Security-Policy")
		return nil
	}
	px.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "rclone 설정 화면 준비 중입니다… 잠시 후 새로고침", http.StatusBadGateway)
	}
	px.ServeHTTP(w, r)
}

// handleRcloneRemotes lists configured rclone remotes (name + type, no secrets)
// from rclone.conf, flagging the one currently active (REMOTE_NAME).
func (s *Server) handleRcloneRemotes(w http.ResponseWriter, r *http.Request) {
	type remote struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Active bool   `json:"active"`
	}
	out := []remote{}
	b, err := os.ReadFile(os.Getenv("RCLONE_CONFIG"))
	if err != nil {
		s.writeJSON(w, 200, out)
		return
	}
	active := os.Getenv("REMOTE_NAME")
	idx := -1
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			name := strings.TrimSpace(t[1 : len(t)-1])
			out = append(out, remote{Name: name, Active: name == active})
			idx = len(out) - 1
		} else if idx >= 0 && strings.HasPrefix(t, "type") {
			if i := strings.Index(t, "="); i >= 0 {
				out[idx].Type = strings.TrimSpace(t[i+1:])
			}
		}
	}
	s.writeJSON(w, 200, out)
}

// handleRcloneGUI controls the on-demand rclone Web GUI sibling container.
// GET → {running}; POST {action:"start"|"stop"} (CSRF) → start returns {port,user,pass}.
func (s *Server) handleRcloneGUI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]any{"running": rcloneGUIRunning()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	var body struct{ Action string }
	json.NewDecoder(r.Body).Decode(&body)
	user, _ := s.currentUser(r)
	switch body.Action {
	case "start":
		if err := startRcloneGUI(); err != nil {
			s.store.Audit(user, "rclone-gui-start", "fail")
			s.writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.store.Audit(user, "rclone-gui-start", "ok")
		s.writeJSON(w, 200, map[string]any{"running": true})
	case "stop":
		if err := stopRcloneGUI(); err != nil {
			s.store.Audit(user, "rclone-gui-stop", "fail")
			s.writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.store.Audit(user, "rclone-gui-stop", "ok")
		s.writeJSON(w, 200, map[string]any{"running": false})
	default:
		http.Error(w, "bad action", 400)
	}
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
