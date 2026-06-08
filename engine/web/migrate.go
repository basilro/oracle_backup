package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	remoteNameFile      = "/config/remote-name"
	remotePathFile      = "/config/remote-path"
	migrationStatusFile = "/state/migration-status.json"
)

// activeRemote returns the configured rclone remote name (REMOTE_NAME env).
func activeRemote() string { return os.Getenv("REMOTE_NAME") }

// defaultRepoPath is the built-in subpath used when no override file exists.
func defaultRepoPath() string { return "backups/" + os.Getenv("HOST_TAG") }

// currentRepoPath returns the active repo subpath using the standard override file.
func currentRepoPath() string { return currentRepoPathFrom(remotePathFile) }

// currentRepoPathFrom reads the override at the given path (testable seam):
// the file's contents if present and non-empty, otherwise the default.
func currentRepoPathFrom(file string) string {
	if b, err := os.ReadFile(file); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return defaultRepoPath()
}

// repoURLOn builds the full restic repo URL for an explicit remote + subpath.
func repoURLOn(remote, path string) string { return "rclone:" + remote + ":" + path }

// repoURL builds the repo URL for the active remote + subpath.
func repoURL(path string) string { return repoURLOn(activeRemote(), path) }

// validRemotePath validates a user-chosen rclone destination subpath. Rejects
// empty, leading "/" or "-", ".." traversal, control chars, and shell/rclone
// metacharacters (incl. ":" which would confuse the remote:path syntax).
func validRemotePath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '`', '$', ';', '|', '&', '<', '>', '"', '\'', '\\', '*', '?', ':':
			return false
		}
	}
	return true
}

// writeRemotePath atomically persists the active subpath (read by entrypoint).
func writeRemotePath(path string) error {
	tmp := remotePathFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(path+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, remotePathFile)
}

// writeRemoteName atomically persists the active remote name (read by entrypoint).
func writeRemoteName(name string) error {
	tmp := remoteNameFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, remoteNameFile)
}

// configuredRemoteNames parses rclone.conf section headers → remote names.
func configuredRemoteNames() []string {
	var names []string
	b, err := os.ReadFile(os.Getenv("RCLONE_CONFIG"))
	if err != nil {
		return names
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			names = append(names, strings.TrimSpace(t[1:len(t)-1]))
		}
	}
	return names
}

// remoteNameIn reports membership (pure, testable seam).
func remoteNameIn(name string, list []string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// validRemoteName accepts only a non-empty name configured in rclone.conf.
func validRemoteName(name string) bool {
	return name != "" && remoteNameIn(name, configuredRemoteNames())
}

// migrateMode decides move vs adopt from target reachability + repo presence.
func migrateMode(toReachable, toHasRepo bool) (string, error) {
	if !toReachable {
		return "", fmt.Errorf("대상 원격에 연결할 수 없습니다")
	}
	if toHasRepo {
		return "adopt", nil
	}
	return "move", nil
}

// remoteConnectedOn reports whether the given remote answers a shallow listing.
func remoteConnectedOn(ctx context.Context, remote string) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "rclone", "lsd", remote+":", "--max-depth", "1").Run() == nil
}

// remoteConnected reports whether the active remote is reachable.
func remoteConnected(ctx context.Context) bool { return remoteConnectedOn(ctx, activeRemote()) }

// handleRemotePath (GET): current remote + active subpath + connectivity.
func (s *Server) handleRemotePath(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, 200, map[string]any{
		"remote":    activeRemote(),
		"path":      currentRepoPath(),
		"connected": remoteConnected(r.Context()),
	})
}

// handleRemoteLs (GET ?path=): list folders (dirs only) under the active remote.
func (s *Server) handleRemoteLs(w http.ResponseWriter, r *http.Request) {
	if activeRemote() == "" {
		s.writeJSON(w, 400, map[string]string{"error": "원격이 설정되지 않았습니다"})
		return
	}
	path := strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
	if path != "" && !validRemotePath(path) {
		http.Error(w, "bad path", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rclone", "lsjson", activeRemote()+":"+path, "--dirs-only").Output()
	if err != nil {
		s.writeJSON(w, 502, map[string]string{"error": "원격 조회 실패 (연결을 확인하세요)"})
		return
	}
	var raw []struct {
		Name  string `json:"Name"`
		IsDir bool   `json:"IsDir"`
	}
	json.Unmarshal(out, &raw)
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	entries := []entry{}
	for _, e := range raw {
		if !e.IsDir {
			continue
		}
		child := e.Name
		if path != "" {
			child = path + "/" + e.Name
		}
		entries = append(entries, entry{Name: e.Name, Path: child})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	s.writeJSON(w, 200, map[string]any{"path": path, "entries": entries})
}

// preflightDecision is the pure decision: may migration proceed?
func preflightDecision(from, to string, toExists, connected bool) error {
	if to == from {
		return fmt.Errorf("새 경로가 현재 경로와 같습니다")
	}
	if !connected {
		return fmt.Errorf("원격에 연결할 수 없습니다")
	}
	if toExists {
		return fmt.Errorf("대상 경로에 이미 저장소가 있습니다")
	}
	return nil
}

// MigrationStatus is the polled state of an in-flight (or last) migration.
type MigrationStatus struct {
	Active     bool   `json:"active"`
	Phase      string `json:"phase"` // idle|preflight|copy|verify|switch|cleanup|done|failed
	Mode       string `json:"mode"`  // move|adopt
	FromRemote string `json:"fromRemote"`
	From       string `json:"from"`
	ToRemote   string `json:"toRemote"`
	To         string `json:"to"`
	Stats      string `json:"stats"`
	Error      string `json:"error"`
	Started    int64  `json:"started"`
	Updated    int64  `json:"updated"`
}

type Migrator struct {
	mu         sync.Mutex
	status     MigrationStatus
	runner     *Runner
	statusFile string
}

func NewMigrator(runner *Runner) *Migrator {
	return &Migrator{runner: runner, status: MigrationStatus{Phase: "idle"}, statusFile: migrationStatusFile}
}

func (m *Migrator) Snapshot() MigrationStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Migrator) persistLocked() {
	b, _ := json.Marshal(m.status)
	tmp := m.statusFile + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, m.statusFile)
	}
}

func (m *Migrator) setPhase(phase string) {
	m.mu.Lock()
	m.status.Phase = phase
	m.status.Updated = time.Now().Unix()
	if phase == "done" || phase == "failed" {
		m.status.Active = false
	}
	m.persistLocked()
	m.mu.Unlock()
}

func (m *Migrator) setStats(line string) {
	if line == "" {
		return
	}
	m.mu.Lock()
	m.status.Stats = line
	m.status.Updated = time.Now().Unix()
	m.persistLocked()
	m.mu.Unlock()
}

func (m *Migrator) setMode(mode string) {
	m.mu.Lock()
	m.status.Mode = mode
	m.status.Updated = time.Now().Unix()
	m.persistLocked()
	m.mu.Unlock()
}

func (m *Migrator) fail(format string, args ...any) {
	m.mu.Lock()
	m.status.Error = fmt.Sprintf(format, args...)
	m.status.Phase = "failed"
	m.status.Active = false
	m.status.Updated = time.Now().Unix()
	m.persistLocked()
	m.mu.Unlock()
}

// repoExists reports whether a restic repo is openable at the given repo URL.
func repoExists(ctx context.Context, repo string) bool {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "restic", "--repo", repo, "cat", "config").Run() == nil
}

// snapshotCount returns the number of snapshots in the given repo URL.
func snapshotCount(ctx context.Context, repo string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "restic", "--repo", repo, "snapshots", "--json").Output()
	if err != nil {
		return 0, err
	}
	var snaps []json.RawMessage
	if err := json.Unmarshal(out, &snaps); err != nil {
		return 0, err
	}
	return len(snaps), nil
}

func (m *Migrator) rcloneCopy(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "rclone", "copy", src, dst,
		"--stats", "3s", "--stats-one-line", "--stats-log-level", "NOTICE")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		m.setStats(strings.TrimSpace(sc.Text()))
	}
	return cmd.Wait()
}

func (m *Migrator) rclonePurge(ctx context.Context, target string) error {
	out, err := exec.CommandContext(ctx, "rclone", "purge", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Start validates, acquires the gate, and launches the migration goroutine.
// An empty toRemote means "keep the current remote" (path-only change).
func (m *Migrator) Start(appCtx context.Context, toRemote, toPath string) error {
	toRemote = strings.TrimSpace(toRemote)
	if toRemote == "" {
		toRemote = activeRemote()
	}
	toPath = strings.Trim(strings.TrimSpace(toPath), "/")
	if !validRemotePath(toPath) {
		return fmt.Errorf("잘못된 경로")
	}
	if !validRemoteName(toRemote) {
		return fmt.Errorf("설정되지 않은 원격: %s", toRemote)
	}
	fromRemote := activeRemote()
	fromPath := currentRepoPath()
	if toRemote == fromRemote && toPath == fromPath {
		return fmt.Errorf("현재 위치와 동일합니다")
	}
	m.mu.Lock()
	if m.status.Active {
		m.mu.Unlock()
		return fmt.Errorf("이미 진행 중")
	}
	m.mu.Unlock()
	if !m.runner.AcquireForMaintenance() {
		return fmt.Errorf("백업/복원이 실행 중입니다")
	}
	now := time.Now().Unix()
	m.mu.Lock()
	m.status = MigrationStatus{Active: true, Phase: "preflight",
		FromRemote: fromRemote, From: fromPath, ToRemote: toRemote, To: toPath,
		Started: now, Updated: now}
	m.persistLocked()
	m.mu.Unlock()
	go func() {
		defer m.runner.ReleaseMaintenance()
		m.run(appCtx, fromRemote, fromPath, toRemote, toPath)
	}()
	return nil
}

// run executes the migration state machine. Invariant: never switch the active
// remote/path or delete the source before copy+verify succeed.
func (m *Migrator) run(ctx context.Context, fromRemote, fromPath, toRemote, toPath string) {
	src := fromRemote + ":" + fromPath
	dst := toRemote + ":" + toPath
	fromRepo := repoURLOn(fromRemote, fromPath)
	toRepo := repoURLOn(toRemote, toPath)

	m.setPhase("preflight")
	connected := remoteConnectedOn(ctx, toRemote)
	toExists := repoExists(ctx, toRepo)
	mode, err := migrateMode(connected, toExists)
	if err != nil {
		m.fail("%v", err)
		return
	}
	m.setMode(mode)

	copied := false
	if mode == "move" && repoExists(ctx, fromRepo) {
		oldCount, _ := snapshotCount(ctx, fromRepo)
		m.setPhase("copy")
		if err := m.rcloneCopy(ctx, src, dst); err != nil {
			m.fail("복사 실패: %v", err)
			return
		}
		m.setPhase("verify")
		if !repoExists(ctx, toRepo) {
			m.fail("검증 실패: 새 위치에서 저장소를 열 수 없습니다")
			return
		}
		newCount, err := snapshotCount(ctx, toRepo)
		if err != nil {
			m.fail("검증 실패: 스냅샷 조회 오류: %v", err)
			return
		}
		if newCount != oldCount {
			m.fail("검증 실패: 스냅샷 개수 불일치 (원본 %d, 사본 %d)", oldCount, newCount)
			return
		}
		copied = true
	}

	m.setPhase("switch")
	if err := writeRemoteName(toRemote); err != nil {
		m.fail("원격 전환 실패: %v", err)
		return
	}
	if err := writeRemotePath(toPath); err != nil {
		m.fail("경로 전환 실패: %v", err)
		return
	}
	os.Setenv("REMOTE_NAME", toRemote)
	os.Setenv("RESTIC_REPOSITORY", toRepo)

	if copied {
		m.setPhase("cleanup")
		if err := m.rclonePurge(ctx, src); err != nil {
			m.mu.Lock()
			m.status.Error = "원본 삭제 경고(전환은 완료됨): " + err.Error()
			m.persistLocked()
			m.mu.Unlock()
		}
	}
	m.setPhase("done")
}

// handleRemoteTarget (GET ?remote=&path=): preview a candidate destination —
// is it reachable, and does it already hold a repo? Drives the move/adopt notice.
func (s *Server) handleRemoteTarget(w http.ResponseWriter, r *http.Request) {
	remote := strings.TrimSpace(r.URL.Query().Get("remote"))
	if remote == "" {
		remote = activeRemote()
	}
	path := strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
	if !validRemoteName(remote) || (path != "" && !validRemotePath(path)) {
		http.Error(w, "bad params", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reachable := remoteConnectedOn(ctx, remote)
	hasRepo := reachable && repoExists(ctx, repoURLOn(remote, path))
	s.writeJSON(w, 200, map[string]any{"reachable": reachable, "hasRepo": hasRepo})
}

// handleRemoteMigrate: GET → status poll; POST → start (CSRF + password re-auth +
// "MIGRATE" confirmation). Dangerous: moves data and deletes the old copy.
func (s *Server) handleRemoteMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, s.migrator.Snapshot())
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
	user, _ := s.currentUser(r)
	var body struct{ Remote, Path, Password, Confirm string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if body.Confirm != "MIGRATE" {
		http.Error(w, "confirmation phrase required", 400)
		return
	}
	if !checkPassword(s.adminHash, body.Password) {
		time.Sleep(time.Second)
		s.store.Audit(user, "remote-migrate", "reauth-fail")
		http.Error(w, "password re-auth failed", 401)
		return
	}
	if body.Remote != "" && !validRemoteName(strings.TrimSpace(body.Remote)) {
		http.Error(w, "invalid remote", 400)
		return
	}
	if !validRemotePath(strings.Trim(strings.TrimSpace(body.Path), "/")) {
		http.Error(w, "invalid path", 400)
		return
	}
	if err := s.migrator.Start(s.appCtx, body.Remote, body.Path); err != nil {
		s.store.Audit(user, "remote-migrate", "start-fail")
		s.writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	s.store.Audit(user, "remote-migrate", "started")
	s.writeJSON(w, 200, s.migrator.Snapshot())
}
