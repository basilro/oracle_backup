# 원격 저장소 경로 변경 + 데이터 마이그레이션 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 대시보드에서 restic 저장소의 원격 하위 경로를 모달 폴더 브라우저로 바꾸고, 변경 시 기존 데이터를 새 경로로 이동(복사→검증→전환→원본 삭제)한다.

**Architecture:** 저장소 경로를 `/config/remote-path` 파일로 분리해 entrypoint가 읽게 한다. 런타임 전환은 Go가 `os.Setenv("RESTIC_REPOSITORY", …)` + 파일 기록으로 즉시 반영(무중단). 마이그레이션은 백업 게이트를 잡는 백그라운드 상태머신(`preflight→copy→verify→switch→cleanup→done`)으로 돌고, 모달이 `/state/migration-status.json` 기반 상태를 폴링한다. rclone/restic은 엔진 컨테이너 내 바이너리를 직접 호출(restic이 이미 그렇게 동작).

**Tech Stack:** Go(net/http, os/exec, encoding/json), bash(entrypoint), rclone 1.66, restic 0.16.5, vanilla JS/CSS UI.

**설계 문서:** `docs/specs/2026-06-08-remote-path-change-design.md`

---

## 파일 구조

| 파일 | 책임 |
|------|------|
| `engine/entrypoint.sh` (수정) | `/config/remote-path` 읽어 `RESTIC_REPOSITORY` 구성 |
| `engine/web/migrate.go` (신규) | 경로 헬퍼, 검증, 연결확인, 마이그레이션 상태머신, 3개 핸들러 |
| `engine/web/migrate_test.go` (신규) | 경로 검증·repoURL·preflight 결정 단위 테스트 |
| `engine/web/actions.go` (수정) | `Runner`에 유지보수 게이트 획득/해제 헬퍼 추가 |
| `engine/web/api.go` (수정) | `Server`에 `migrator` 필드 추가, 라우트 3개 등록 |
| `engine/web/main.go` (수정) | `NewMigrator(runner)`로 `srv.migrator` 초기화 |
| `engine/web/ui/index.html` (수정) | 목적지 탭에 경로 변경 카드 + 원격 브라우저/진행 모달 |
| `engine/web/ui/app.js` (수정) | 경로 로드·브라우저·마이그레이션 폴링 로직 |
| `engine/web/ui/style.css` (수정) | 진행 패널 스타일 |

---

## Task 1: 경로 영속화 (entrypoint + Go 헬퍼 + 단위 테스트)

**Files:**
- Modify: `engine/entrypoint.sh:24`
- Create: `engine/web/migrate.go`
- Create: `engine/web/migrate_test.go`

- [ ] **Step 1: entrypoint가 `/config/remote-path`를 읽도록 수정**

`engine/entrypoint.sh`의 24번 줄을 아래로 교체:

```bash
# 저장소 하위 경로: 런타임에 UI로 변경 가능(/config/remote-path). 없으면 기본값(하위호환).
REPO_PATH="backups/${HOST_TAG:?set HOST_TAG}"
if [ -s /config/remote-path ]; then
    REPO_PATH="$(head -n1 /config/remote-path | tr -d '[:space:]')"
fi
export RESTIC_REPOSITORY="rclone:${REMOTE_NAME:?set REMOTE_NAME}:${REPO_PATH}"
```

- [ ] **Step 2: migrate.go 경로 헬퍼 작성 (먼저 함수만, 상태머신은 Task 3)**

`engine/web/migrate.go` 생성 (import는 Task별로 점증한다 — Go는 unused import를 컴파일 에러로 처리하므로 지금은 `os`/`strings`만):

```go
package main

import (
	"os"
	"strings"
)

const (
	remotePathFile      = "/config/remote-path"
	migrationStatusFile = "/state/migration-status.json"
)

// activeRemote returns the configured rclone remote name (REMOTE_NAME env).
func activeRemote() string { return os.Getenv("REMOTE_NAME") }

// defaultRepoPath is the built-in subpath used when no override file exists.
func defaultRepoPath() string { return "backups/" + os.Getenv("HOST_TAG") }

// currentRepoPath returns the active repo subpath: the override file if present
// and non-empty, otherwise the default.
func currentRepoPath() string {
	if b, err := os.ReadFile(remotePathFile); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return defaultRepoPath()
}

// repoURL builds the full restic repo URL (rclone backend) for a subpath.
func repoURL(path string) string { return "rclone:" + activeRemote() + ":" + path }

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
```

- [ ] **Step 3: 단위 테스트 작성 (실패하도록)**

`engine/web/migrate_test.go` 생성:

```go
package main

import (
	"os"
	"testing"
)

func TestValidRemotePath(t *testing.T) {
	ok := []string{"backups/host", "a", "내백업/server1", "x/y/z"}
	bad := []string{"", "/abs", "-flag", "a/../b", "a:b", "a;b", "a b\tc\n", "a|b", "a$b"}
	for _, p := range ok {
		if !validRemotePath(p) {
			t.Errorf("expected valid: %q", p)
		}
	}
	for _, p := range bad {
		if validRemotePath(p) {
			t.Errorf("expected invalid: %q", p)
		}
	}
}

func TestRepoURL(t *testing.T) {
	os.Setenv("REMOTE_NAME", "gdrive")
	if got := repoURL("backups/h"); got != "rclone:gdrive:backups/h" {
		t.Errorf("repoURL = %q", got)
	}
}

func TestCurrentRepoPath(t *testing.T) {
	os.Setenv("HOST_TAG", "myhost")
	// no override file → default
	os.Remove(remotePathFileTest(t))
	if got := currentRepoPathFrom(remotePathFileTest(t)); got != "backups/myhost" {
		t.Errorf("default path = %q", got)
	}
	// with override
	os.WriteFile(remotePathFileTest(t), []byte("custom/path\n"), 0644)
	if got := currentRepoPathFrom(remotePathFileTest(t)); got != "custom/path" {
		t.Errorf("override path = %q", got)
	}
}
```

> 참고: `currentRepoPath()`는 상수 경로(`/config/remote-path`)를 읽어 테스트가 어려우므로,
> 파일 경로를 인자로 받는 내부 함수 `currentRepoPathFrom(path string)`로 분리하고
> `currentRepoPath()`는 `currentRepoPathFrom(remotePathFile)`를 호출하도록 한다.
> 테스트 헬퍼 `remotePathFileTest`는 임시 파일 경로를 돌려준다.

- [ ] **Step 4: 테스트가 컴파일되도록 헬퍼 분리 리팩터**

`migrate.go`의 `currentRepoPath`를 아래로 교체:

```go
// currentRepoPath returns the active repo subpath using the standard override file.
func currentRepoPath() string { return currentRepoPathFrom(remotePathFile) }

// currentRepoPathFrom reads the override at the given path (testable seam).
func currentRepoPathFrom(file string) string {
	if b, err := os.ReadFile(file); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return defaultRepoPath()
}
```

`migrate_test.go` 맨 아래에 테스트 헬퍼 추가:

```go
func remotePathFileTest(t *testing.T) string {
	return t.TempDir() + "/remote-path"
}
```

- [ ] **Step 5: 테스트 실행 (통과 확인)**

Run: `cd engine/web && go test -run 'TestValidRemotePath|TestRepoURL|TestCurrentRepoPath' -v`
Expected: PASS (3 tests)

- [ ] **Step 6: 커밋**

```bash
git add engine/entrypoint.sh engine/web/migrate.go engine/web/migrate_test.go
git commit -m "feat(repo): /config/remote-path 기반 저장소 경로 분리 + 검증 헬퍼"
```

---

## Task 2: 연결 확인 + 원격 폴더 목록 엔드포인트

**Files:**
- Modify: `engine/web/migrate.go` (핸들러 추가)
- Modify: `engine/web/api.go:112` (라우트 등록)

- [ ] **Step 1: import 블록 확장 후 연결 확인 + 두 핸들러 추가**

먼저 `engine/web/migrate.go`의 import 블록을 아래로 교체(이 Task에서 쓰는 것만):

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)
```

그다음 `engine/web/migrate.go` 끝에 추가:

```go
// remoteConnected reports whether the active remote answers a shallow listing.
func remoteConnected(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "rclone", "lsd", activeRemote()+":", "--max-depth", "1").Run() == nil
}

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
```

- [ ] **Step 2: 라우트 등록**

`engine/web/api.go`의 112번 줄(`mux.HandleFunc("/api/rclone-remotes", …)`) 바로 위에 추가:

```go
	mux.HandleFunc("/api/remote-path", s.requireAuth(s.handleRemotePath))
	mux.HandleFunc("/api/remote-ls", s.requireAuth(s.handleRemoteLs))
```

- [ ] **Step 3: 컴파일 확인**

Run: `cd engine/web && go build ./...`
Expected: 성공(에러 없음). `migrator` 미정의 에러가 없어야 함(아직 Server에 필드 미추가 — 이 Task에서는 핸들러가 `s.migrator`를 안 쓰므로 OK).

- [ ] **Step 4: 커밋**

```bash
git add engine/web/migrate.go engine/web/api.go
git commit -m "feat(repo): 원격 연결확인 + 폴더목록 엔드포인트(/api/remote-path, /api/remote-ls)"
```

---

## Task 3: 마이그레이션 상태머신 + 게이트 헬퍼 (+ preflight 단위 테스트)

**Files:**
- Modify: `engine/web/actions.go:106` (Runner 헬퍼)
- Modify: `engine/web/migrate.go` (상태머신)
- Modify: `engine/web/migrate_test.go` (preflight 테스트)

- [ ] **Step 1: Runner에 유지보수 게이트 헬퍼 추가**

`engine/web/actions.go`의 `NewRunner` 함수(106번 줄 끝) 바로 아래에 추가:

```go
// AcquireForMaintenance grabs the run gate for a non-backup maintenance task
// (e.g. repo migration), marking the runner busy. Returns false if already busy.
func (r *Runner) AcquireForMaintenance() bool {
	if !r.gate.TryLock() {
		return false
	}
	r.running.Store(true)
	return true
}

// ReleaseMaintenance releases the gate acquired by AcquireForMaintenance.
func (r *Runner) ReleaseMaintenance() {
	r.running.Store(false)
	r.gate.Unlock()
}
```

- [ ] **Step 2: import에 fmt 추가 후 preflight 결정 함수(순수) + 테스트 작성**

먼저 `engine/web/migrate.go`의 import 블록에 `"fmt"`를 추가(`encoding/json`과 `net/http` 사이, 알파벳 순):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)
```

그다음 `engine/web/migrate.go` 끝에 추가:

```go
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
```

`engine/web/migrate_test.go`에 추가:

```go
func TestPreflightDecision(t *testing.T) {
	if preflightDecision("a", "a", false, true) == nil {
		t.Error("same path must fail")
	}
	if preflightDecision("a", "b", false, false) == nil {
		t.Error("disconnected must fail")
	}
	if preflightDecision("a", "b", true, true) == nil {
		t.Error("existing target must fail")
	}
	if err := preflightDecision("a", "b", false, true); err != nil {
		t.Errorf("valid case must pass: %v", err)
	}
}
```

- [ ] **Step 3: 테스트 실행 (통과 확인)**

Run: `cd engine/web && go test -run TestPreflightDecision -v`
Expected: PASS

- [ ] **Step 4: import 블록 확장 후 상태머신(Migrator) 본체 작성**

먼저 `engine/web/migrate.go`의 import 블록을 최종 형태로 교체(`bufio`/`fmt`/`sync` 추가):

```go
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
```

그다음 `engine/web/migrate.go` 끝에 추가:

```go
// MigrationStatus is the polled state of an in-flight (or last) migration.
type MigrationStatus struct {
	Active  bool   `json:"active"`
	Phase   string `json:"phase"` // idle|preflight|copy|verify|switch|cleanup|done|failed
	From    string `json:"from"`
	To      string `json:"to"`
	Stats   string `json:"stats"`
	Error   string `json:"error"`
	Started int64  `json:"started"`
	Updated int64  `json:"updated"`
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
func (m *Migrator) Start(appCtx context.Context, to string) error {
	to = strings.Trim(strings.TrimSpace(to), "/")
	if !validRemotePath(to) {
		return fmt.Errorf("잘못된 경로")
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
	from := currentRepoPath()
	now := time.Now().Unix()
	m.mu.Lock()
	m.status = MigrationStatus{Active: true, Phase: "preflight", From: from, To: to, Started: now, Updated: now}
	m.persistLocked()
	m.mu.Unlock()
	go func() {
		defer m.runner.ReleaseMaintenance()
		m.run(appCtx, from, to)
	}()
	return nil
}

// run executes the migration state machine. Invariant: never switch the active
// path or delete the source before copy+verify succeed.
func (m *Migrator) run(ctx context.Context, from, to string) {
	remote := activeRemote()
	src := remote + ":" + from
	dst := remote + ":" + to
	fromRepo := repoURL(from)
	toRepo := repoURL(to)

	m.setPhase("preflight")
	connected := remoteConnected(ctx)
	toExists := repoExists(ctx, toRepo)
	if err := preflightDecision(from, to, toExists, connected); err != nil {
		m.fail("%v", err)
		return
	}
	oldHasData := repoExists(ctx, fromRepo)

	if oldHasData {
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
	}

	m.setPhase("switch")
	if err := writeRemotePath(to); err != nil {
		m.fail("경로 전환 실패: %v", err)
		return
	}
	os.Setenv("RESTIC_REPOSITORY", toRepo)

	if oldHasData {
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
```

- [ ] **Step 5: 컴파일 + 단위 테스트 확인**

Run: `cd engine/web && go build ./... && go test -run 'TestValidRemotePath|TestRepoURL|TestCurrentRepoPath|TestPreflightDecision' -v`
Expected: 빌드 성공, 4개 테스트 PASS

- [ ] **Step 6: 커밋**

```bash
git add engine/web/actions.go engine/web/migrate.go engine/web/migrate_test.go
git commit -m "feat(repo): 마이그레이션 상태머신(preflight→copy→verify→switch→cleanup)"
```

---

## Task 4: 마이그레이션 핸들러 + Server/main 와이어링

**Files:**
- Modify: `engine/web/api.go:33` (Server 필드), `api.go:113` (라우트)
- Modify: `engine/web/migrate.go` (핸들러)
- Modify: `engine/web/main.go:164-168` (초기화)

- [ ] **Step 1: Server 구조체에 migrator 필드 추가**

`engine/web/api.go`의 `Server` 구조체(33번 줄 `appCtx context.Context` 위)에 추가:

```go
	migrator    *Migrator
```

- [ ] **Step 2: 마이그레이션 핸들러 작성**

`engine/web/migrate.go` 끝에 추가:

```go
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
	var body struct{ Path, Password, Confirm string }
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
	if !validRemotePath(strings.Trim(strings.TrimSpace(body.Path), "/")) {
		http.Error(w, "invalid path", 400)
		return
	}
	if err := s.migrator.Start(s.appCtx, body.Path); err != nil {
		s.store.Audit(user, "remote-migrate", "start-fail")
		s.writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	s.store.Audit(user, "remote-migrate", "started")
	s.writeJSON(w, 200, s.migrator.Snapshot())
}
```

- [ ] **Step 3: 라우트 등록**

`engine/web/api.go`의 Task 2에서 추가한 두 줄 아래에 추가:

```go
	mux.HandleFunc("/api/remote-migrate", s.requireAuth(s.handleRemoteMigrate))
```

- [ ] **Step 4: main.go에서 migrator 초기화**

`engine/web/main.go`의 `srv := &Server{…}` 블록(164-168번 줄)에서 `appCtx: appCtx,` 다음 줄에 필드를 추가하는 대신, runner가 이미 있으므로 구조체 리터럴에 추가:

```go
	srv := &Server{
		store: store, runner: runner, cfgPath: cfgPath, logDir: "/var/log/backup",
		sessionKey: loadSessionKey(), adminUser: adminUser, adminHash: adminHash,
		restoreRoot: "/restore-out", appCtx: appCtx,
		migrator: NewMigrator(runner),
	}
```

- [ ] **Step 5: 빌드 + 전체 테스트 + vet**

Run: `cd engine/web && go build ./... && go vet ./... && go test ./...`
Expected: 빌드/vet 성공, 모든 테스트 PASS

- [ ] **Step 6: 커밋**

```bash
git add engine/web/api.go engine/web/migrate.go engine/web/main.go
git commit -m "feat(repo): 마이그레이션 핸들러 + Server/main 와이어링(/api/remote-migrate)"
```

---

## Task 5: UI — 경로 변경 카드 + 원격 브라우저/진행 모달

**Files:**
- Modify: `engine/web/ui/index.html` (목적지 탭 카드 + 모달)
- Modify: `engine/web/ui/app.js` (로직 + 와이어링)
- Modify: `engine/web/ui/style.css` (진행 패널)

- [ ] **Step 1: 목적지 탭에 "백업 저장 위치" 카드 추가**

`engine/web/ui/index.html`의 96번 줄(`설정된 목적지` 카드의 닫는 `</div>`)과 98번 줄(`rclone CLI` 카드) 사이에 새 카드 삽입:

```html
      <div class="card">
        <h2>백업 저장 위치 (원격 경로)</h2>
        <p class="dim" style="margin:0 0 12px;font-size:.86rem">현재 원격(<code id="rpRemote">—</code>) 안에서 저장소가 위치한 경로입니다. 변경하면 기존 백업 데이터를 새 경로로 <b>이동</b>(복사→검증→전환→원본 삭제)합니다. <b>원격이 연결된 상태에서만</b> 변경할 수 있습니다.</p>
        <div class="field"><div class="lab">현재 경로</div><div class="ctl"><code id="rpPath" class="mono">—</code> <span id="rpConn" class="st" style="margin-left:8px"></span></div></div>
        <div class="row-actions" style="margin-top:6px">
          <button id="rpChange" class="btn-primary" disabled>경로 변경…</button>
          <span id="rpMsg" class="msg"></span>
        </div>
      </div>
```

- [ ] **Step 2: 원격 브라우저 + 진행 모달 추가**

`engine/web/ui/index.html`의 `browseModal` 닫는 `</div>`(167번 줄) 다음, `rgModal`(169번 줄) 앞에 삽입:

```html
  <div id="remoteModal" class="modal" hidden>
    <div class="modal-box" style="height:min(80vh,760px)">
      <div class="modal-head">
        <b>백업 저장 위치 변경</b>
        <span class="dim" id="rmRemoteLbl" style="font-size:.8rem"></span>
        <span class="spacer"></span>
        <button id="rmCancel" class="btn-ghost">취소</button>
      </div>

      <div id="rmBrowse">
        <div class="br-crumb" id="rmCrumb"></div>
        <div class="br-list" id="rmList"></div>
        <div class="rm-bar">
          <input id="rmNewName" type="text" placeholder="새 폴더 이름(선택)" spellcheck="false" autocomplete="off">
          <button id="rmMkPath" class="btn-ghost">하위 경로로 추가</button>
          <span class="spacer"></span>
          <span class="dim" style="font-size:.82rem">대상:</span>
          <code id="rmTarget" class="mono">—</code>
          <button id="rmNext" class="btn-primary">이 위치로 변경</button>
        </div>
      </div>

      <div id="rmConfirmStep" hidden>
        <div class="rm-confirm">
          <p>현재 <code id="rmcFrom" class="mono"></code> → <code id="rmcTo" class="mono"></code> 로 이동합니다.</p>
          <p class="dim" style="font-size:.85rem">복사·검증 후 원본은 삭제됩니다. 진행하려면 관리자 비밀번호와 <code>MIGRATE</code>를 입력하세요.</p>
          <div class="field"><div class="lab">관리자 비밀번호</div><div class="ctl"><input id="rmcPass" type="password" placeholder="••••••••" style="width:180px"></div></div>
          <div class="field"><div class="lab">확인 문구<small>대문자 MIGRATE</small></div><div class="ctl"><input id="rmcPhrase" type="text" placeholder="MIGRATE" autocomplete="off" style="width:180px"></div></div>
          <div class="row-actions"><button id="rmcBack" class="btn-ghost">뒤로</button><button id="rmcStart" class="btn-primary">이동 시작</button><span id="rmcMsg" class="msg"></span></div>
        </div>
      </div>

      <div id="rmProgress" hidden>
        <div class="mp">
          <div class="mp-phase" id="mpPhase">준비 중…</div>
          <div class="mp-bar"><div class="mp-fill" id="mpFill"></div></div>
          <pre class="mp-stats" id="mpStats"></pre>
          <div class="mp-err" id="mpErr" hidden></div>
          <div class="row-actions"><button id="mpClose" class="btn-primary" hidden>닫기</button></div>
        </div>
      </div>
    </div>
  </div>
```

- [ ] **Step 3: app.js — 경로 로드 + 브라우저 + 마이그레이션 로직 추가**

`engine/web/ui/app.js`의 `browseClose` 관련 블록이 끝나는 곳(498번 줄, `$("#brSel").addEventListener(...)` 다음) 뒤에 추가:

```javascript
/* ---------- remote repo path change + migration ---------- */
let rmPath = "", rmTarget = "", rmPoll = null;
const MP_PHASES = { preflight: ["사전 점검", 10], copy: ["복사 중", 45], verify: ["검증 중", 75], switch: ["경로 전환", 90], cleanup: ["원본 정리", 96], done: ["완료", 100], failed: ["실패", 100] };

async function loadRepoPath() {
  try {
    const d = await (await api("/api/remote-path")).json();
    $("#rpRemote").textContent = d.remote || "—";
    $("#rpPath").textContent = d.path || "—";
    const conn = $("#rpConn");
    conn.textContent = d.connected ? "연결됨" : "연결 안 됨";
    conn.className = "st " + (d.connected ? "ok" : "fail");
    $("#rpChange").disabled = !d.connected;
    $("#rpMsg").textContent = d.connected ? "" : "원격에 연결되어야 경로를 변경할 수 있습니다.";
    $("#rpMsg").className = "msg" + (d.connected ? "" : " fail");
    $("#rmRemoteLbl").textContent = d.remote ? "원격: " + d.remote : "";
  } catch (e) {}
}

function remoteOpen() {
  $("#remoteModal").hidden = false;
  $("#rmBrowse").hidden = false;
  $("#rmConfirmStep").hidden = true;
  $("#rmProgress").hidden = true;
  rmLoad("");
}
function remoteClose() {
  if (rmPoll) { clearInterval(rmPoll); rmPoll = null; }
  $("#remoteModal").hidden = true;
  loadRepoPath();
}
function rmSetTarget(p) { rmTarget = p; $("#rmTarget").textContent = p || "(원격 루트)"; }

async function rmLoad(path) {
  rmPath = path;
  rmSetTarget(path);
  rmRenderCrumb();
  const list = $("#rmList");
  list.innerHTML = `<div class="br-empty">불러오는 중… (원격 조회, 처음 여는 폴더는 수 초 걸릴 수 있습니다)</div>`;
  try {
    const d = await (await api("/api/remote-ls?path=" + encodeURIComponent(path))).json();
    if (d.error) { list.innerHTML = `<div class="br-empty" style="color:var(--fail)">${esc(d.error)}</div>`; return; }
    rmPath = d.path;
    rmSetTarget(d.path);
    const rows = (d.entries || []).map(e =>
      `<div class="br-row"><span class="nm dir" data-dir="${esc(e.path)}">📁 ${esc(e.name)}</span></div>`).join("");
    list.innerHTML = rows || `<div class="br-empty">하위 폴더 없음 · 이 위치 또는 새 폴더를 대상으로 지정하세요</div>`;
  } catch (e) {
    list.innerHTML = `<div class="br-empty" style="color:var(--fail)">조회 실패: ${esc(e.message || e)}</div>`;
  }
}
function rmRenderCrumb() {
  const parts = rmPath ? rmPath.split("/") : [];
  let acc = "";
  const segs = [`<a data-p="">루트</a>`];
  for (const seg of parts) { acc = acc ? acc + "/" + seg : seg; segs.push(`<a data-p="${esc(acc)}">${esc(seg)}</a>`); }
  $("#rmCrumb").innerHTML = segs.join(" / ");
}

function rmGoConfirm(to) {
  $("#rmcFrom").textContent = $("#rpPath").textContent;
  $("#rmcTo").textContent = to;
  $("#rmcPass").value = ""; $("#rmcPhrase").value = "";
  $("#rmcMsg").textContent = "";
  $("#rmBrowse").hidden = true;
  $("#rmConfirmStep").hidden = false;
  $("#rmConfirmStep").dataset.to = to;
}

async function rmStartMigrate() {
  const to = $("#rmConfirmStep").dataset.to;
  const m = $("#rmcMsg"); m.className = "msg"; m.textContent = "시작 중…";
  try {
    const r = await api("/api/remote-migrate", { method: "POST", body: JSON.stringify({ Path: to, Password: $("#rmcPass").value, Confirm: $("#rmcPhrase").value }) });
    if (!r.ok) { m.className = "msg fail"; m.textContent = "✕ " + (await r.text()); return; }
    $("#rmConfirmStep").hidden = true;
    $("#rmProgress").hidden = false;
    $("#mpClose").hidden = true;
    renderMigrate(await r.json());
    rmPoll = setInterval(pollMigrate, 2000);
  } catch (e) { if (String(e.message) !== "unauthorized") { m.className = "msg fail"; m.textContent = "✕ " + e.message; } }
}
async function pollMigrate() {
  try {
    const st = await (await api("/api/remote-migrate")).json();
    renderMigrate(st);
    if (!st.active) { if (rmPoll) { clearInterval(rmPoll); rmPoll = null; } $("#mpClose").hidden = false; }
  } catch (e) {}
}
function renderMigrate(st) {
  const info = MP_PHASES[st.phase] || [st.phase || "—", 0];
  $("#mpPhase").textContent = info[0];
  $("#mpFill").style.width = info[1] + "%";
  $("#mpFill").classList.toggle("fail", st.phase === "failed");
  $("#mpStats").textContent = st.stats || "";
  const err = $("#mpErr");
  if (st.error) { err.hidden = false; err.textContent = st.error; err.className = "mp-err" + (st.phase === "failed" ? " fatal" : ""); }
  else err.hidden = true;
}
```

- [ ] **Step 4: app.js — 버튼/이벤트 와이어링 추가**

같은 파일에서 Step 3 코드 바로 뒤에 추가:

```javascript
$("#rpChange") && ($("#rpChange").onclick = remoteOpen);
$("#rmCancel") && ($("#rmCancel").onclick = remoteClose);
$("#rmCrumb") && $("#rmCrumb").addEventListener("click", e => {
  const a = e.target.closest("a[data-p]"); if (a) rmLoad(a.getAttribute("data-p"));
});
$("#rmList") && $("#rmList").addEventListener("click", e => {
  const d = e.target.closest(".nm.dir"); if (d) rmLoad(d.getAttribute("data-dir"));
});
$("#rmMkPath") && ($("#rmMkPath").onclick = () => {
  const name = ($("#rmNewName").value || "").trim().replace(/^\/+|\/+$/g, "");
  if (!name) return;
  rmSetTarget(rmPath ? rmPath + "/" + name : name);
  $("#rmNewName").value = "";
});
$("#rmNext") && ($("#rmNext").onclick = () => {
  if (!rmTarget) { return; }
  rmGoConfirm(rmTarget);
});
$("#rmcBack") && ($("#rmcBack").onclick = () => { $("#rmConfirmStep").hidden = true; $("#rmBrowse").hidden = false; });
$("#rmcStart") && ($("#rmcStart").onclick = rmStartMigrate);
$("#mpClose") && ($("#mpClose").onclick = remoteClose);
```

- [ ] **Step 5: app.js — 부팅 시 경로 로드**

`engine/web/ui/app.js`의 부팅 IIFE(519번 줄 `loadRemotes();` 다음)에 추가:

```javascript
  loadRepoPath();
```

- [ ] **Step 6: style.css — 진행 패널/바 스타일 추가**

`engine/web/ui/style.css` 끝에 추가:

```css
.rm-bar { display:flex; align-items:center; gap:8px; padding:10px 0 2px; flex-wrap:wrap; }
.rm-bar input { width:min(200px,45vw); }
.rm-confirm { padding:8px 2px; }
.mp { padding:12px 4px; }
.mp-phase { font-weight:600; margin-bottom:8px; }
.mp-bar { height:10px; border-radius:6px; background:var(--border-soft); overflow:hidden; }
.mp-fill { height:100%; width:0; background:var(--ok,#3fb950); transition:width .4s ease; }
.mp-fill.fail { background:var(--fail,#f85149); }
.mp-stats { margin:10px 0 0; font-size:.78rem; color:var(--dim,#8b949e); white-space:pre-wrap; word-break:break-all; }
.mp-err { margin-top:10px; font-size:.85rem; color:var(--fail,#f85149); }
.mp-err.fatal { font-weight:600; }
```

- [ ] **Step 7: 커밋**

```bash
git add engine/web/ui/index.html engine/web/ui/app.js engine/web/ui/style.css
git commit -m "feat(ui): 백업 저장 위치 변경 카드 + 원격 브라우저/진행 모달"
```

---

## Task 6: 빌드·캐시버스트·검증·최종 커밋

**Files:**
- Modify: `engine/web/ui/app.js:1` (BUILD), `engine/web/ui/index.html` (asset `?v=`)

- [ ] **Step 1: 캐시버스트 스탬프 갱신**

`engine/web/ui/app.js`의 1번 줄:
```javascript
const BUILD = "ui-2026-06-08a";
```

`engine/web/ui/index.html`에서 `?v=20260603b`를 모두 `?v=20260608a`로 교체(10번 줄 style.css, 11-13번 줄 xterm, 183번 줄 app.js).

Run: `cd engine/web/ui && sed -i 's/v=20260603b/v=20260608a/g' index.html && grep -c "20260608a" index.html`
Expected: `5`

- [ ] **Step 2: Go 빌드 · vet · 테스트 (최종)**

Run: `cd engine/web && gofmt -w migrate.go && go build ./... && go vet ./... && go test ./...`
Expected: 모두 성공, 전체 테스트 PASS

- [ ] **Step 3: 이미지 빌드 (로컬 arm64) + 컨테이너 기동 + 헬스 확인**

Run:
```bash
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 5 && \
  curl -fsS http://localhost:8088/healthz && echo " OK"
```
Expected: `ok OK`

- [ ] **Step 4: 수동 검증 (브라우저)**

다음을 확인:
1. 목적지 탭 → "백업 저장 위치" 카드에 현재 원격/경로/연결 상태 표시.
2. 원격 연결 시 "경로 변경…" 활성화, 끊김 시 비활성화 + 안내.
3. "경로 변경…" → 모달에서 폴더 탐색, 새 폴더 이름 추가, 대상 표시 확인.
4. "이 위치로 변경" → 비밀번호 + `MIGRATE` 입력 → "이동 시작" → 진행 바가 preflight→copy→…→done 진행.
5. 완료 후 카드의 현재 경로가 새 경로로 갱신.

- [ ] **Step 5: 최종 커밋**

```bash
git add engine/web/ui/app.js engine/web/ui/index.html
git commit -m "chore(ui): 캐시버스트 스탬프 ui-2026-06-08a"
```

---

## 검증 체크리스트 (spec 대비)

- [x] 현재 원격 내 경로만 변경 — `validRemotePath` + `activeRemote` 고정
- [x] 이동(복사+검증 후 원본 삭제) — `run()`의 copy→verify→switch→cleanup(purge)
- [x] 즉시 반영(무중단) — `os.Setenv` + `writeRemotePath`, entrypoint 폴백
- [x] 원격 연결 시에만 변경 — `remoteConnected` 게이트(버튼 disabled + preflight)
- [x] 모달 경로 선택 — `remoteModal` + `/api/remote-ls`
- [x] 안전 불변식 — copy/verify 전 전환·삭제 금지(`run` 순서)
- [x] 재인증 — `MIGRATE` 문구 + 비밀번호(`checkPassword`)
- [x] 동시 실행 차단 — `AcquireForMaintenance`(백업 게이트 공유)
