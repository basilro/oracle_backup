# 원격(remote) 선택·전환 + 채택 전환 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 대시보드에서 활성 rclone 원격을 다른 원격으로 전환하고, 대상에 저장소가 있으면 채택(adopt), 없으면 교차 원격 이동(move)한다.

**Architecture:** `/config/remote-name`으로 원격을 경로와 같은 패턴으로 영속화(entrypoint 폴백 + `os.Setenv` 무중단). 마이그레이션 엔진을 `(remote,path)` 쌍 → `(remote,path)` 쌍으로 일반화하고 대상 저장소 유무로 move/adopt 분기. 별도 원격 드롭다운 컨트롤 + 시작 전 미리보기로 파괴적 동작을 사전 고지.

**Tech Stack:** Go(net/http, os/exec), bash(entrypoint), rclone 1.66, restic 0.16.5, vanilla JS/CSS.

**설계 문서:** `docs/specs/2026-06-08-remote-select-design.md`

---

## 파일 구조

| 파일 | 변경 |
|------|------|
| `engine/entrypoint.sh` | `/config/remote-name` 읽어 `REMOTE_NAME` 재export |
| `engine/web/migrate.go` | `repoURLOn`/`remoteConnectedOn`/`writeRemoteName`/`configuredRemoteNames`/`remoteNameIn`/`validRemoteName`/`migrateMode`, `MigrationStatus` 확장, `Start`/`run` 일반화, `handleRemoteTarget`, `handleRemoteMigrate` 바디 확장 |
| `engine/web/migrate_test.go` | `remoteNameIn`/`migrateMode`/`repoURLOn` 단위테스트 |
| `engine/web/api.go` | `/api/remote-target` 라우트 |
| `engine/web/ui/index.html` | 원격 드롭다운 + "원격 전환…" 버튼, 확인 단계 모드 문구, 84행 안내 갱신 |
| `engine/web/ui/app.js` | 원격 목록 채우기, 미리보기, 전환 흐름 |
| `engine/web/ui/style.css` | (필요 시 소폭) |

---

## Task 1: 원격 이름 영속화 + Go 헬퍼 (TDD)

**Files:**
- Modify: `engine/entrypoint.sh`
- Modify: `engine/web/migrate.go`
- Modify: `engine/web/migrate_test.go`

- [ ] **Step 1: entrypoint가 `/config/remote-name`도 읽도록 수정**

`engine/entrypoint.sh`의 현재 블록:
```bash
# 저장소 하위 경로: 런타임에 UI로 변경 가능(/config/remote-path). 없으면 기본값(하위호환).
REPO_PATH="backups/${HOST_TAG:?set HOST_TAG}"
if [ -s /config/remote-path ]; then
    REPO_PATH="$(head -n1 /config/remote-path | tr -d '[:space:]')"
fi
export RESTIC_REPOSITORY="rclone:${REMOTE_NAME:?set REMOTE_NAME}:${REPO_PATH}"
```
을 아래로 교체:
```bash
# 활성 원격·하위 경로: 런타임에 UI로 변경 가능(/config/remote-name, /config/remote-path).
# 없으면 기본값(하위호환). REMOTE_NAME을 재export 해 엔진(os.Getenv)·UI가 오버라이드를 본다.
REMOTE="${REMOTE_NAME:?set REMOTE_NAME}"
if [ -s /config/remote-name ]; then
    REMOTE="$(head -n1 /config/remote-name | tr -d '[:space:]')"
fi
REPO_PATH="backups/${HOST_TAG:?set HOST_TAG}"
if [ -s /config/remote-path ]; then
    REPO_PATH="$(head -n1 /config/remote-path | tr -d '[:space:]')"
fi
export REMOTE_NAME="$REMOTE"
export RESTIC_REPOSITORY="rclone:${REMOTE}:${REPO_PATH}"
```

- [ ] **Step 2: migrate.go 상수 + 원격 헬퍼 추가**

`engine/web/migrate.go`의 const 블록:
```go
const (
	remotePathFile      = "/config/remote-path"
	migrationStatusFile = "/state/migration-status.json"
)
```
을 아래로 교체(`remoteNameFile` 추가):
```go
const (
	remoteNameFile      = "/config/remote-name"
	remotePathFile      = "/config/remote-path"
	migrationStatusFile = "/state/migration-status.json"
)
```

`repoURL` 함수:
```go
// repoURL builds the full restic repo URL (rclone backend) for a subpath.
func repoURL(path string) string { return "rclone:" + activeRemote() + ":" + path }
```
을 아래로 교체(`repoURLOn` 추가 + 위임):
```go
// repoURLOn builds the full restic repo URL for an explicit remote + subpath.
func repoURLOn(remote, path string) string { return "rclone:" + remote + ":" + path }

// repoURL builds the repo URL for the active remote + subpath.
func repoURL(path string) string { return repoURLOn(activeRemote(), path) }
```

`writeRemotePath` 함수 바로 아래에 추가:
```go
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
```

- [ ] **Step 3: remoteConnected를 임의 원격으로 일반화**

`engine/web/migrate.go`의:
```go
// remoteConnected reports whether the active remote answers a shallow listing.
func remoteConnected(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "rclone", "lsd", activeRemote()+":", "--max-depth", "1").Run() == nil
}
```
을 아래로 교체:
```go
// remoteConnectedOn reports whether the given remote answers a shallow listing.
func remoteConnectedOn(ctx context.Context, remote string) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "rclone", "lsd", remote+":", "--max-depth", "1").Run() == nil
}

// remoteConnected reports whether the active remote is reachable.
func remoteConnected(ctx context.Context) bool { return remoteConnectedOn(ctx, activeRemote()) }
```

- [ ] **Step 4: 단위 테스트 추가**

`engine/web/migrate_test.go`의 `TestPreflightDecision` 함수 바로 아래에 추가:
```go
func TestRemoteNameIn(t *testing.T) {
	list := []string{"gdrive", "onedrive"}
	if !remoteNameIn("gdrive", list) {
		t.Error("gdrive should be in list")
	}
	if remoteNameIn("dropbox", list) {
		t.Error("dropbox should not be in list")
	}
	if remoteNameIn("", list) {
		t.Error("empty should not be in list")
	}
}

func TestMigrateMode(t *testing.T) {
	if _, err := migrateMode(false, false); err == nil {
		t.Error("unreachable must error")
	}
	if m, _ := migrateMode(true, true); m != "adopt" {
		t.Errorf("hasRepo→adopt, got %q", m)
	}
	if m, _ := migrateMode(true, false); m != "move" {
		t.Errorf("empty→move, got %q", m)
	}
}

func TestRepoURLOn(t *testing.T) {
	if got := repoURLOn("onedrive", "backups/h"); got != "rclone:onedrive:backups/h" {
		t.Errorf("repoURLOn = %q", got)
	}
}
```

- [ ] **Step 5: 테스트 실행**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go test -run 'TestRemoteNameIn|TestMigrateMode|TestRepoURLOn' -v"
```
Expected: 3개 PASS

- [ ] **Step 6: 커밋**

```bash
git add engine/entrypoint.sh engine/web/migrate.go engine/web/migrate_test.go
git commit -m "feat(remote): /config/remote-name 영속화 + 원격 검증/모드결정 헬퍼"
```

---

## Task 2: 마이그레이션 엔진 일반화 (move/adopt, 교차원격) + 미리보기

**Files:**
- Modify: `engine/web/migrate.go`
- Modify: `engine/web/api.go`

- [ ] **Step 1: MigrationStatus에 mode/원격 필드 추가**

`engine/web/migrate.go`의:
```go
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
```
을 아래로 교체:
```go
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
```

- [ ] **Step 2: setMode 헬퍼 추가**

`engine/web/migrate.go`의 `setStats` 함수 바로 아래에 추가:
```go
func (m *Migrator) setMode(mode string) {
	m.mu.Lock()
	m.status.Mode = mode
	m.status.Updated = time.Now().Unix()
	m.persistLocked()
	m.mu.Unlock()
}
```

- [ ] **Step 3: Start를 (remote,path)로 일반화**

`engine/web/migrate.go`의 `Start` 함수 전체:
```go
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
```
을 아래로 교체:
```go
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
```

- [ ] **Step 4: run을 교차원격 + move/adopt로 일반화**

`engine/web/migrate.go`의 `run` 함수 전체:
```go
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
을 아래로 교체:
```go
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
```

> 참고: 이제 `preflightDecision`은 `run`에서 직접 호출하지 않고 `migrateMode`로 대체된다. `preflightDecision` 함수와 그 단위테스트(`TestPreflightDecision`)는 그대로 둔다(다른 곳에서 참조하지 않아 컴파일에 무해; 향후 재사용 가능). 동일성 검사(to==from)는 `Start`로 이동했다.

- [ ] **Step 5: handleRemoteMigrate 바디에 Remote 추가**

`engine/web/migrate.go`의 `handleRemoteMigrate` 내 바디 디코드/검증/시작 부분:
```go
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
```
을 아래로 교체:
```go
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
```

- [ ] **Step 6: handleRemoteTarget 미리보기 핸들러 추가**

`engine/web/migrate.go`의 `handleRemoteMigrate` 함수 바로 위에 추가:
```go
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
```

- [ ] **Step 7: /api/remote-target 라우트 등록**

`engine/web/api.go`의:
```go
	mux.HandleFunc("/api/remote-ls", s.requireAuth(s.handleRemoteLs))
	mux.HandleFunc("/api/remote-migrate", s.requireAuth(s.handleRemoteMigrate))
```
을 아래로 교체:
```go
	mux.HandleFunc("/api/remote-ls", s.requireAuth(s.handleRemoteLs))
	mux.HandleFunc("/api/remote-target", s.requireAuth(s.handleRemoteTarget))
	mux.HandleFunc("/api/remote-migrate", s.requireAuth(s.handleRemoteMigrate))
```

- [ ] **Step 8: 빌드 + vet + 테스트**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test ./... && echo ALL_OK"
```
Expected: ALL_OK, 모든 테스트 PASS

- [ ] **Step 9: 커밋**

```bash
git add engine/web/migrate.go engine/web/api.go
git commit -m "feat(remote): 교차원격 마이그레이션(move/adopt) + 대상 미리보기(/api/remote-target)"
```

---

## Task 3: UI — 원격 전환 컨트롤

**Files:**
- Modify: `engine/web/ui/index.html`
- Modify: `engine/web/ui/app.js`

- [ ] **Step 1: 카드에 원격 드롭다운 + 버튼 추가**

`engine/web/ui/index.html`의 "백업 저장 위치" 카드에서:
```html
        <div class="row-actions" style="margin-top:6px">
          <button id="rpChange" class="btn-primary" disabled>경로 변경…</button>
          <span id="rpMsg" class="msg"></span>
        </div>
      </div>
```
을 아래로 교체(원격 전환 필드 추가):
```html
        <div class="row-actions" style="margin-top:6px">
          <button id="rpChange" class="btn-primary" disabled>경로 변경…</button>
          <span id="rpMsg" class="msg"></span>
        </div>
        <div class="field" style="margin-top:14px;border-top:1px solid var(--border-soft);padding-top:14px"><div class="lab">원격 전환<small>다른 목적지로 전환 · 경로는 현재 유지</small></div><div class="ctl"><select id="remoteSel" style="min-width:160px"></select><button id="remoteSwitchBtn" class="btn-ghost" style="margin-left:8px" disabled>원격 전환…</button></div></div>
      </div>
```

- [ ] **Step 2: 확인 단계 문구를 동적(mode)으로 변경**

`engine/web/ui/index.html`의 확인 단계:
```html
          <p>현재 <code id="rmcFrom" class="mono"></code> → <code id="rmcTo" class="mono"></code> 로 이동합니다.</p>
          <p class="dim" style="font-size:.85rem">복사·검증 후 원본은 삭제됩니다. 진행하려면 관리자 비밀번호와 <code>MIGRATE</code>를 입력하세요.</p>
```
을 아래로 교체:
```html
          <p>현재 <code id="rmcFrom" class="mono"></code> → <code id="rmcTo" class="mono"></code></p>
          <p id="rmcMode" class="dim" style="font-size:.85rem"></p>
          <p class="dim" style="font-size:.85rem">진행하려면 관리자 비밀번호와 <code>MIGRATE</code>를 입력하세요.</p>
```

- [ ] **Step 3: index.html:84 안내 문구 갱신**

`engine/web/ui/index.html`에서 아래 문장:
```html
활성 목적지 변경은 <code>.env</code>의 <code>REMOTE_NAME</code> 수정 후 재시작.
```
을 아래로 교체:
```html
활성 목적지(원격) 변경은 아래 "백업 저장 위치" 카드의 <b>원격 전환</b>으로(재시작 불필요).
```

- [ ] **Step 4: app.js — 현재 원격 추적 + 드롭다운 채우기**

`engine/web/ui/app.js`의 `loadRepoPath` 함수에서 줄:
```javascript
    $("#rpRemote").textContent = d.remote || "—";
```
을 아래로 교체(모듈 변수에 보관 + 드롭다운 갱신):
```javascript
    rpCurRemote = d.remote || "";
    $("#rpRemote").textContent = d.remote || "—";
    loadRemoteSelect();
```

그리고 `let rmPath = "", rmTarget = "", rmPoll = null;` 줄을 아래로 교체(상태 변수 추가):
```javascript
let rmPath = "", rmTarget = "", rmPoll = null, rpCurRemote = "";
async function loadRemoteSelect() {
  const sel = $("#remoteSel"); if (!sel) return;
  try {
    const rs = await (await api("/api/rclone-remotes")).json();
    const list = Array.isArray(rs) ? rs : [];
    sel.innerHTML = list.map(x => `<option value="${esc(x.name)}"${x.name === rpCurRemote ? " selected" : ""}>${esc(x.name)}${x.name === rpCurRemote ? " (현재)" : ""}</option>`).join("");
    // 전환 가능: 현재 원격 외에 다른 원격이 하나라도 있어야 의미 있음
    $("#remoteSwitchBtn").disabled = list.filter(x => x.name !== rpCurRemote).length === 0;
  } catch (e) {}
}
```

- [ ] **Step 5: app.js — rmGoConfirm을 미리보기 기반(async)으로 교체**

`engine/web/ui/app.js`의 `rmGoConfirm` 함수 전체:
```javascript
function rmGoConfirm(to) {
  $("#rmcFrom").textContent = $("#rpPath").textContent;
  $("#rmcTo").textContent = to;
  $("#rmcPass").value = ""; $("#rmcPhrase").value = "";
  $("#rmcMsg").textContent = "";
  $("#rmBrowse").hidden = true;
  $("#rmConfirmStep").hidden = false;
  $("#rmConfirmStep").dataset.to = to;
}
```
을 아래로 교체:
```javascript
async function rmGoConfirm(remote, path) {
  const r = remote || rpCurRemote;
  $("#rmcFrom").textContent = rpCurRemote + ":" + $("#rpPath").textContent;
  $("#rmcTo").textContent = r + ":" + path;
  $("#rmcMode").textContent = "대상 점검 중…";
  $("#rmcPass").value = ""; $("#rmcPhrase").value = "";
  $("#rmcMsg").textContent = "";
  $("#rmBrowse").hidden = true;
  $("#rmConfirmStep").hidden = false;
  $("#rmConfirmStep").dataset.remote = remote || "";
  $("#rmConfirmStep").dataset.to = path;
  try {
    const t = await (await api(`/api/remote-target?remote=${encodeURIComponent(r)}&path=${encodeURIComponent(path)}`)).json();
    if (!t.reachable) { $("#rmcMode").textContent = "⚠ 대상 원격에 연결할 수 없습니다."; }
    else if (t.hasRepo) { $("#rmcMode").textContent = "채택 전환: 데이터 이동 없이 이 저장소로 전환합니다(원본 유지)."; }
    else { $("#rmcMode").textContent = "이동: 복사·검증 후 원본은 삭제됩니다."; }
  } catch (e) { $("#rmcMode").textContent = ""; }
}
```

- [ ] **Step 6: app.js — rmStartMigrate가 Remote도 전송하도록 수정**

`engine/web/ui/app.js`의 `rmStartMigrate` 함수에서:
```javascript
  const to = $("#rmConfirmStep").dataset.to;
  const m = $("#rmcMsg"); m.className = "msg"; m.textContent = "시작 중…";
  try {
    const r = await api("/api/remote-migrate", { method: "POST", body: JSON.stringify({ Path: to, Password: $("#rmcPass").value, Confirm: $("#rmcPhrase").value }) });
```
을 아래로 교체:
```javascript
  const to = $("#rmConfirmStep").dataset.to;
  const toRemote = $("#rmConfirmStep").dataset.remote || "";
  const m = $("#rmcMsg"); m.className = "msg"; m.textContent = "시작 중…";
  try {
    const r = await api("/api/remote-migrate", { method: "POST", body: JSON.stringify({ Remote: toRemote, Path: to, Password: $("#rmcPass").value, Confirm: $("#rmcPhrase").value }) });
```

- [ ] **Step 7: app.js — 원격 전환 진입 함수 + renderMigrate 모드 표시**

`engine/web/ui/app.js`의 `function renderMigrate(st) {` 함수 전체:
```javascript
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
을 아래로 교체(모드 라벨 추가 + 전환 진입 함수):
```javascript
function renderMigrate(st) {
  const info = MP_PHASES[st.phase] || [st.phase || "—", 0];
  const modeLbl = st.mode === "adopt" ? "[채택] " : st.mode === "move" ? "[이동] " : "";
  $("#mpPhase").textContent = modeLbl + info[0];
  $("#mpFill").style.width = info[1] + "%";
  $("#mpFill").classList.toggle("fail", st.phase === "failed");
  $("#mpStats").textContent = st.stats || "";
  const err = $("#mpErr");
  if (st.error) { err.hidden = false; err.textContent = st.error; err.className = "mp-err" + (st.phase === "failed" ? " fatal" : ""); }
  else err.hidden = true;
}
function remoteSwitchOpen() {
  const remote = $("#remoteSel").value;
  if (!remote || remote === rpCurRemote) return;
  const path = $("#rpPath").textContent;
  $("#remoteModal").hidden = false;
  $("#rmBrowse").hidden = true;
  $("#rmProgress").hidden = true;
  $("#rmConfirmStep").hidden = false;
  rmGoConfirm(remote, path === "—" ? "" : path);
}
```

- [ ] **Step 8: app.js — 와이어링 갱신(rmNext 인자 + 원격 전환 버튼)**

`engine/web/ui/app.js`의:
```javascript
$("#rmNext") && ($("#rmNext").onclick = () => {
  if (!rmTarget) { return; }
  rmGoConfirm(rmTarget);
});
```
을 아래로 교체:
```javascript
$("#rmNext") && ($("#rmNext").onclick = () => {
  if (!rmTarget) { return; }
  rmGoConfirm("", rmTarget);
});
$("#remoteSwitchBtn") && ($("#remoteSwitchBtn").onclick = remoteSwitchOpen);
```

- [ ] **Step 9: app.js 문법 검사 + 커밋**

Run: `cd engine/web/ui && node --check app.js && echo OK`
Expected: OK

```bash
git add engine/web/ui/index.html engine/web/ui/app.js
git commit -m "feat(ui): 원격 전환 드롭다운 + move/adopt 미리보기"
```

---

## Task 4: 빌드·검증·캐시버스트·최종커밋

**Files:**
- Modify: `engine/web/ui/app.js`, `engine/web/ui/index.html`

- [ ] **Step 1: 캐시버스트 스탬프 갱신**

`engine/web/ui/app.js`의 1번 줄을:
```javascript
const BUILD = "ui-2026-06-08b";
```
로 바꾸고, 자산 버전 교체:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && sed -i 's/v=20260608a/v=20260608b/g' index.html && echo "count=$(grep -c 20260608b index.html)"
```
Expected: count=5

- [ ] **Step 2: Go 빌드·vet·테스트 (최종)**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: ALL_OK

- [ ] **Step 3: 이미지 재빌드 + 기동 + 헬스 + 라우트 확인**

Run:
```bash
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 6 && \
  curl -fsS http://localhost:8088/healthz && echo " healthz" && \
  for p in remote-target remote-migrate; do echo "/api/$p -> $(curl -s -o /dev/null -w '%{http_code}' http://localhost:8088/api/$p)"; done && \
  docker logs --tail 3 backupstack_engine 2>&1 | grep -i repo
```
Expected: `ok healthz`, 두 라우트 모두 `401`(등록됨), 로그에 `repo=rclone:...`

- [ ] **Step 4: 수동 검증 (브라우저)**

1. 목적지 탭 "백업 저장 위치" 카드에 원격 드롭다운 + "원격 전환…" 버튼 표시(설정된 원격이 2개 이상일 때 활성).
2. 다른 원격 선택 → "원격 전환…" → 확인창에 **이동/채택** 중 무엇인지 표시.
3. 비번 + `MIGRATE` → 진행 모달에 `[이동]`/`[채택]` 라벨 + 단계 진행.
4. 완료 후 카드의 현재 원격/경로 갱신.

- [ ] **Step 5: 최종 커밋**

```bash
git add engine/web/ui/app.js engine/web/ui/index.html
git commit -m "chore(ui): 캐시버스트 ui-2026-06-08b (원격 전환)"
```

---

## 검증 체크리스트 (spec 대비)

- [x] 원격 전용 별도 컨트롤 — 카드 드롭다운 + "원격 전환…" 버튼
- [x] 채택 전환 — `migrateMode`가 대상 저장소 있으면 adopt(복사·삭제 없이 switch)
- [x] 교차 원격 이동 — `run`이 `from원격:from경로 → to원격:to경로` 복사
- [x] 원격 영속화 — `/config/remote-name` + entrypoint 재export + `os.Setenv`
- [x] 원격 이름 검증 — `validRemoteName`(rclone.conf 멤버십)
- [x] 정보 동의 미리보기 — `/api/remote-target` + 확인창 모드 문구
- [x] 경로는 현재 유지 — `remoteSwitchOpen`이 현재 경로를 대상 경로로
- [x] 안내 문구 갱신 — index.html:84
