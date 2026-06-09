# 백업 소스 경로 설정 + DB 작업 목록(추가/삭제) 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 백업 소스 경로(`/home` 자리)와 DB 덤프 작업 목록을 UI에서 설정·추가·삭제할 수 있게 하되, 설정이 없으면 현재 동작 그대로 유지한다.

**Architecture:** `SOURCE_PATHS`(config.env)로 소스 경로를 설정하고 `/var/lib/docker/volumes`는 고정. DB는 `/config/db-jobs.json` 목록(내장 3종 유형)으로, 파일이 없으면 기존 3종 기본 호출로 폴백. `db-dump.sh` 덤프 함수를 인자화하고 bash는 `jq`로 목록을 순회한다.

**Tech Stack:** Go(net/http, os, encoding/json), bash, jq, vanilla JS/CSS.

**설계 문서:** `docs/specs/2026-06-09-configurable-sources-dbjobs-design.md`

---

## 파일 구조

| 파일 | 변경 |
|------|------|
| `engine/Dockerfile` | 런타임 apk에 `jq` 추가 |
| `engine/web/config.go` | `SourcePaths` 키 + 검증(`validSourcePaths`) |
| `engine/web/config_test.go` | `validSourcePaths` 단위테스트 |
| `engine/web/dbjobs.go` (신규) | DBJob 타입, 검증, 파일 read/write, GET/POST 핸들러 |
| `engine/web/dbjobs_test.go` (신규) | `validDBJob`/`validDBJobs` 단위테스트 |
| `engine/web/api.go` | `/api/db-jobs` 라우트 |
| `engine/scripts/lib/db-dump.sh` | 덤프 함수 인자화(name/container/data) |
| `engine/scripts/home-backup.sh` | `SOURCE_PATHS` + db-jobs 순회/폴백 |
| `engine/web/ui/{index.html,app.js,style.css}` | 소스경로 필드 + DB 작업 테이블 |
| `engine/config.env.example`, `config/config.env.example` | `SOURCE_PATHS` 문서화 |

---

## Task 1: Go — SOURCE_PATHS 영속화 + 전용 엔드포인트 (TDD)

> 설계 변경: 기존 `/api/config`(PUT, PascalCase body)에 끼우면 기존 설정 폼 로직과 충돌하므로, DB작업과 대칭으로 **전용 엔드포인트**로 분리한다. config.env가 아니라 전용 파일 `/config/source-paths`에 저장(공백 구분 경로 한 줄). 엔진은 그 파일 → `SOURCE_PATHS` env 순으로 읽는다.

**Files:**
- Create: `engine/web/srcpaths.go`
- Create: `engine/web/srcpaths_test.go`
- Modify: `engine/web/api.go`

- [ ] **Step 1: srcpaths.go 생성 (검증 + 파일 + 핸들러)**

`engine/web/srcpaths.go`:
```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const sourcePathsFile = "/config/source-paths"

// readSourcePaths returns configured source paths string (default "/home").
func readSourcePaths() string {
	b, err := os.ReadFile(sourcePathsFile)
	if err != nil {
		return "/home"
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "/home"
}

// validSourcePaths checks each whitespace-separated path is absolute, has no
// "..", and is not a dangerous root.
func validSourcePaths(s string) error {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return fmt.Errorf("소스 경로가 비어 있습니다")
	}
	dangerous := map[string]bool{"/": true, "/etc": true, "/root": true, "/var": true, "/boot": true, "/usr": true, "/bin": true, "/sbin": true, "/lib": true, "/proc": true, "/sys": true, "/dev": true}
	for _, p := range fields {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("절대경로여야 합니다: %q", p)
		}
		if strings.Contains(p, "..") {
			return fmt.Errorf(".. 포함 불가: %q", p)
		}
		clean := strings.TrimRight(p, "/")
		if clean == "" {
			clean = "/"
		}
		if dangerous[clean] {
			return fmt.Errorf("위험 경로 불가: %q", p)
		}
	}
	return nil
}

func writeSourcePaths(s string) error {
	tmp := sourcePathsFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(s)+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, sourcePathsFile)
}

// handleSourcePaths: GET → {paths}; POST {paths} → validate + save.
func (s *Server) handleSourcePaths(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]string{"paths": readSourcePaths()})
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
	var body struct {
		Paths string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if err := validSourcePaths(body.Paths); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := writeSourcePaths(body.Paths); err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "저장 실패"})
		return
	}
	s.store.Audit(user, "source-paths", "set")
	s.writeJSON(w, 200, map[string]string{"paths": strings.TrimSpace(body.Paths)})
}
```

- [ ] **Step 2: srcpaths_test.go 생성**

`engine/web/srcpaths_test.go`:
```go
package main

import "testing"

func TestValidSourcePaths(t *testing.T) {
	ok := []string{"/home", "/home /srv/data", "/opt/docker"}
	bad := []string{"", "home", "/home/../etc", "/etc", "/", "/var"}
	for _, s := range ok {
		if err := validSourcePaths(s); err != nil {
			t.Errorf("expected valid %q: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validSourcePaths(s); err == nil {
			t.Errorf("expected invalid: %q", s)
		}
	}
}
```

- [ ] **Step 3: 라우트 등록**

`engine/web/api.go`의 `mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))` 줄 다음에 추가:
```go
	mux.HandleFunc("/api/source-paths", s.requireAuth(s.handleSourcePaths))
```

- [ ] **Step 4: 빌드 + 테스트**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go test -run TestValidSourcePaths -v && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: TestValidSourcePaths PASS, ALL_OK

- [ ] **Step 5: 커밋**

```bash
git add engine/web/srcpaths.go engine/web/srcpaths_test.go engine/web/api.go
git commit -m "feat(config): 소스 경로 전용 엔드포인트(/api/source-paths) + 검증(기본 /home)"
```

---

## Task 2: Go — DB 작업 목록 엔드포인트 (TDD)

**Files:**
- Create: `engine/web/dbjobs.go`
- Create: `engine/web/dbjobs_test.go`
- Modify: `engine/web/api.go`

- [ ] **Step 1: dbjobs.go 생성 (타입 + 검증 + 파일 + 핸들러)**

`engine/web/dbjobs.go`:
```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const dbJobsFile = "/config/db-jobs.json"

type DBJob struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Container string `json:"container"`
	Data      string `json:"data"`
	Enabled   bool   `json:"enabled"`
}

var dbJobNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var dbJobTypes = map[string]bool{"postgres": true, "mongodb": true, "redis": true}

// defaultDBJobs mirrors the legacy hardcoded 3-type behavior (shown when no file).
func defaultDBJobs() []DBJob {
	return []DBJob{
		{Name: "postgres", Type: "postgres", Container: "postgres", Data: "/home/docker/postgres/data", Enabled: true},
		{Name: "mongodb", Type: "mongodb", Container: "mongodb", Data: "/home/docker/mongodb", Enabled: true},
		{Name: "redis", Type: "redis", Container: "redis", Data: "/home/docker/redis", Enabled: true},
	}
}

// validDBJob validates one job. dataOK: empty or absolute, no "..", no control chars.
func validDBJob(j DBJob) error {
	if !dbJobNameRe.MatchString(j.Name) {
		return fmt.Errorf("이름 형식 오류: %q", j.Name)
	}
	if !dbJobTypes[j.Type] {
		return fmt.Errorf("지원하지 않는 유형: %q", j.Type)
	}
	if !dbJobNameRe.MatchString(j.Container) {
		return fmt.Errorf("컨테이너 이름 형식 오류: %q", j.Container)
	}
	if j.Data != "" {
		if !strings.HasPrefix(j.Data, "/") || strings.Contains(j.Data, "..") {
			return fmt.Errorf("데이터 경로 오류: %q", j.Data)
		}
		for _, r := range j.Data {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("데이터 경로에 제어문자")
			}
		}
	}
	return nil
}

// validDBJobs validates the whole list (each job + unique names).
func validDBJobs(jobs []DBJob) error {
	seen := map[string]bool{}
	for _, j := range jobs {
		if err := validDBJob(j); err != nil {
			return err
		}
		if seen[j.Name] {
			return fmt.Errorf("이름 중복: %q", j.Name)
		}
		seen[j.Name] = true
	}
	return nil
}

func readDBJobs() ([]DBJob, bool) {
	b, err := os.ReadFile(dbJobsFile)
	if err != nil {
		return defaultDBJobs(), true // defaults flag
	}
	var jobs []DBJob
	if json.Unmarshal(b, &jobs) != nil {
		return defaultDBJobs(), true
	}
	return jobs, false
}

func writeDBJobs(jobs []DBJob) error {
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := dbJobsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dbJobsFile)
}

// handleDBJobs: GET → {jobs, defaults}; POST {jobs:[...]} → validate + save.
func (s *Server) handleDBJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jobs, defaults := readDBJobs()
		s.writeJSON(w, 200, map[string]any{"jobs": jobs, "defaults": defaults})
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
	var body struct {
		Jobs []DBJob `json:"jobs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if err := validDBJobs(body.Jobs); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := writeDBJobs(body.Jobs); err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "저장 실패"})
		return
	}
	s.store.Audit(user, "db-jobs", fmt.Sprintf("save:%d", len(body.Jobs)))
	s.writeJSON(w, 200, map[string]any{"jobs": body.Jobs, "defaults": false})
}
```

- [ ] **Step 2: dbjobs_test.go 생성**

`engine/web/dbjobs_test.go`:
```go
package main

import "testing"

func TestValidDBJob(t *testing.T) {
	ok := DBJob{Name: "pg-main", Type: "postgres", Container: "postgres", Data: "/home/docker/pg", Enabled: true}
	if err := validDBJob(ok); err != nil {
		t.Errorf("expected valid: %v", err)
	}
	if validDBJob(DBJob{Name: "x", Type: "mysql", Container: "c"}) == nil {
		t.Error("unknown type must fail")
	}
	if validDBJob(DBJob{Name: "bad name", Type: "redis", Container: "c"}) == nil {
		t.Error("bad name must fail")
	}
	if validDBJob(DBJob{Name: "x", Type: "redis", Container: "c", Data: "../etc"}) == nil {
		t.Error("relative data must fail")
	}
	if err := validDBJob(DBJob{Name: "x", Type: "redis", Container: "c", Data: ""}); err != nil {
		t.Errorf("empty data should be allowed: %v", err)
	}
}

func TestValidDBJobsUnique(t *testing.T) {
	dup := []DBJob{
		{Name: "a", Type: "redis", Container: "c1"},
		{Name: "a", Type: "redis", Container: "c2"},
	}
	if validDBJobs(dup) == nil {
		t.Error("duplicate names must fail")
	}
	if err := validDBJobs([]DBJob{}); err != nil {
		t.Errorf("empty list must be allowed (DB-less server): %v", err)
	}
}
```

- [ ] **Step 3: 라우트 등록**

`engine/web/api.go`의 `mux.HandleFunc("/api/alert-webhook-test", ...)` 줄 다음에 추가:
```go
	mux.HandleFunc("/api/db-jobs", s.requireAuth(s.handleDBJobs))
```

- [ ] **Step 4: 빌드 + 테스트**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test -run 'TestValidDBJob|TestValidDBJobsUnique' -v && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: 2개 PASS, ALL_OK

- [ ] **Step 5: 커밋**

```bash
git add engine/web/dbjobs.go engine/web/dbjobs_test.go engine/web/api.go
git commit -m "feat(dbjobs): DB 작업 목록 엔드포인트(/api/db-jobs) + 검증"
```

---

## Task 3: 엔진 스크립트 — 덤프 함수 인자화 + 목록 순회 + SOURCE_PATHS + jq

**Files:**
- Modify: `engine/Dockerfile`
- Modify: `engine/scripts/lib/db-dump.sh`
- Modify: `engine/scripts/home-backup.sh`

- [ ] **Step 1: Dockerfile 런타임에 jq 추가**

`engine/Dockerfile`의:
```dockerfile
RUN apk add --no-cache bash docker-cli gzip curl wget tzdata coreutils util-linux findutils ca-certificates fuse3
```
을 아래로 교체(`jq` 추가):
```dockerfile
RUN apk add --no-cache bash docker-cli gzip curl wget tzdata coreutils util-linux findutils ca-certificates fuse3 jq
```

- [ ] **Step 2: db-dump.sh 덤프 함수 인자화**

`engine/scripts/lib/db-dump.sh`의 `dump_postgres`/`dump_mongodb`/`dump_redis` 세 함수를 아래로 교체:
```bash
dump_postgres() {  # $1=name $2=container $3=data
    local name="${1:-postgres}" c="${2:-postgres}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local user="${PG_USER:-postgres}" out="$STAGE/$name"; mkdir -p "$out"
    log "dump postgres name=$name c=$c user=$user"
    if docker exec -u postgres "$c" pg_dumpall -U "$user" | gzip -1 > "$out/all.sql.gz"; then
        local sz; sz=$(stat -c%s "$out/all.sql.gz")
        [ "$sz" -gt 1000 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "postgres dump too small"; }
        db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
    else db_summary_add "$name" DUMP_FAILED "pg_dumpall error"; die "postgres dump failed"; fi
}

dump_mongodb() {  # $1=name $2=container $3=data
    local name="${1:-mongodb}" c="${2:-mongodb}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"; local a=()
    [ -n "${MONGO_USER:-}" ] && [ -n "${MONGO_PASS:-}" ] && a=(-u "$MONGO_USER" -p "$MONGO_PASS" --authenticationDatabase admin)
    log "dump mongodb name=$name c=$c"
    if docker exec "$c" sh -c "rm -f /tmp/mongo.archive" \
       && docker exec "$c" mongodump --quiet "${a[@]}" --gzip --archive=/tmp/mongo.archive \
       && docker cp "$c:/tmp/mongo.archive" "$out/mongo.archive"; then
        docker exec "$c" rm -f /tmp/mongo.archive || true
        local sz; sz=$(stat -c%s "$out/mongo.archive")
        [ "$sz" -gt 100 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "mongo dump too small"; }
        db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
    else db_summary_add "$name" DUMP_FAILED "mongodump error"; die "mongodb dump failed"; fi
}

dump_redis() {  # $1=name $2=container $3=data
    local name="${1:-redis}" c="${2:-redis}" data="${3:-}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"; local a=()
    [ -n "${REDIS_PASS:-}" ] && a=(-a "$REDIS_PASS" --no-auth-warning)
    local dp="${REDIS_DUMP_PATH:-/data/dump.rdb}"
    log "dump redis name=$name c=$c"
    local before; before=$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE) || { db_summary_add "$name" DUMP_FAILED "auth/conn"; die "redis conn"; }
    docker exec "$c" redis-cli "${a[@]}" BGSAVE >/dev/null
    local w=0; while [ "$w" -lt 60 ]; do sleep 2; w=$((w+2)); [ "$(docker exec "$c" redis-cli "${a[@]}" LASTSAVE)" != "$before" ] && break; done
    [ "$w" -lt 60 ] || { db_summary_add "$name" DUMP_FAILED "BGSAVE timeout"; die "redis BGSAVE timeout"; }
    docker cp "$c:$dp" "$out/dump.rdb"
    db_summary_add "$name" DUMPED_OK "$(stat -c%s "$out/dump.rdb")B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
}

# run_db_jobs: iterate /config/db-jobs.json if present, else legacy 3-type defaults.
run_db_jobs() {
    if [ -s /config/db-jobs.json ]; then
        local name type container data enabled
        while IFS=$'\t' read -r name type container data enabled; do
            [ -n "$name" ] || continue
            if [ "$enabled" != "true" ]; then db_summary_add "$name" DISABLED "off"; continue; fi
            case "$type" in
                postgres) dump_postgres "$name" "$container" "$data" ;;
                mongodb)  dump_mongodb  "$name" "$container" "$data" ;;
                redis)    dump_redis    "$name" "$container" "$data" ;;
                *) db_summary_add "$name" BAD_TYPE "$type" ;;
            esac
        done < <(jq -r '.[] | [.name,.type,.container,(.data//""),(.enabled|tostring)] | @tsv' /config/db-jobs.json)
    else
        dump_postgres postgres "${PG_CONTAINER:-postgres}" /home/docker/postgres/data
        dump_mongodb  mongodb  "${MONGO_CONTAINER:-mongodb}" /home/docker/mongodb
        dump_redis    redis    "${REDIS_CONTAINER:-redis}" /home/docker/redis
    fi
}
```

> 참고: `db_summary_add`에 새 상태 `DISABLED`/`BAD_TYPE`가 추가되지만, 그 함수는 임의 문자열을
> 받으므로 변경 불필요. 산출물 디렉터리가 고정 `$STAGE/postgres` 대신 `$STAGE/$name`이 되어
> 같은 유형 여러 작업도 충돌하지 않는다.

- [ ] **Step 3: home-backup.sh — SOURCE_PATHS + run_db_jobs**

`engine/scripts/home-backup.sh`의:
```bash
BACKUP_PATHS=(/home /var/lib/docker/volumes)
```
를 아래로 교체:
```bash
# 소스 경로: /config/source-paths(UI 설정) → SOURCE_PATHS env → 기본 /home.
# 그 뒤에 도커 볼륨(고정)을 항상 더한다.
_SRC_RAW="${SOURCE_PATHS:-/home}"
[ -s /config/source-paths ] && _SRC_RAW="$(head -n1 /config/source-paths)"
read -ra _SRC <<< "$_SRC_RAW"
BACKUP_PATHS=("${_SRC[@]}" /var/lib/docker/volumes)

같은 파일 Phase1 블록의:
```bash
    dump_postgres; dump_mongodb; dump_redis
```
을 아래로 교체:
```bash
    run_db_jobs
```

- [ ] **Step 4: bash 문법 검사**

Run:
```bash
cd /home/ubuntu/backup-stack && bash -n engine/scripts/lib/db-dump.sh && bash -n engine/scripts/home-backup.sh && echo "문법 OK"
```
Expected: 문법 OK

- [ ] **Step 5: jq 순회 로직 로컬 검증 (가짜 목록)**

Run:
```bash
printf '%s' '[{"name":"pg1","type":"postgres","container":"pg","data":"/d/pg","enabled":true},{"name":"r1","type":"redis","container":"rc","data":"","enabled":false}]' | \
  jq -r '.[] | [.name,.type,.container,(.data//""),(.enabled|tostring)] | @tsv'
```
Expected (탭 구분 2행):
```
pg1	postgres	pg	/d/pg	true
r1	redis	rc		false
```

- [ ] **Step 6: 커밋**

```bash
git add engine/Dockerfile engine/scripts/lib/db-dump.sh engine/scripts/home-backup.sh
git commit -m "feat(backup): SOURCE_PATHS + DB 작업 목록 순회(db-jobs.json) + 덤프 함수 인자화"
```

---

## Task 4: UI — 소스 경로 필드 + DB 작업 테이블

**Files:**
- Modify: `engine/web/ui/index.html`
- Modify: `engine/web/ui/app.js`
- Modify: `engine/web/ui/style.css`
- Modify: `engine/config.env.example`, `config/config.env.example`

- [ ] **Step 1: index.html — "백업 대상 · DB" 카드 추가**

`engine/web/ui/index.html`의 "백업 대상 · 제외 규칙" 카드 닫는 부분을 찾는다. 현재:
```html
        <div class="row-actions"><button id="saveExcludes" class="btn-primary">제외 규칙 저장</button><span id="exMsg" class="msg"></span></div>
      </div>
      <div class="card">
        <h2>알림 (Discord/Slack webhook)</h2>
```
이 중 `</div>`(제외 카드 닫음)와 `<div class="card">`(알림 카드) 사이에 새 카드 삽입 → 결과:
```html
        <div class="row-actions"><button id="saveExcludes" class="btn-primary">제외 규칙 저장</button><span id="exMsg" class="msg"></span></div>
      </div>
      <div class="card">
        <h2>백업 대상 경로 · 데이터베이스</h2>
        <p class="dim" style="margin:0 0 12px;font-size:.86rem">백업할 소스 경로입니다. <code>/var/lib/docker/volumes</code>는 항상 포함됩니다. 여러 경로는 공백으로 구분하세요.</p>
        <div class="field"><div class="lab">소스 경로<small>기본 /home</small></div><div class="ctl"><input id="srcPaths" type="text" placeholder="/home" spellcheck="false" autocomplete="off" style="width:min(360px,65vw)"></div></div>
        <div class="row-actions"><button id="saveSrc" class="btn-primary">소스 경로 저장</button><span id="srcMsg" class="msg"></span></div>
        <div style="margin-top:18px;border-top:1px solid var(--border-soft);padding-top:14px">
          <div style="display:flex;align-items:center;gap:10px;margin-bottom:8px">
            <span class="dim" style="font-size:.72rem;letter-spacing:.12em;text-transform:uppercase">데이터베이스 덤프 작업</span>
            <span id="dbDefaults" class="st" style="display:none">기본값(미저장)</span>
          </div>
          <p class="dim" style="margin:0 0 10px;font-size:.82rem">덤프할 DB를 추가/삭제합니다. 컨테이너가 없거나 멈춰 있으면 자동으로 건너뜁니다. 데이터 경로는 덤프 성공 시 원본 이중 백업을 막기 위해 제외됩니다(비우면 제외 안 함). 인증정보는 <code>secrets/db-creds.env</code>에서 관리합니다. <b>모두 삭제하면 DB 덤프를 건너뜁니다.</b></p>
          <table id="dbTable" class="db-table"></table>
          <div class="row-actions" style="margin-top:10px"><button id="dbAdd" class="btn-ghost">DB 추가</button><button id="dbSave" class="btn-primary">DB 작업 저장</button><span id="dbMsg" class="msg"></span></div>
        </div>
      </div>
      <div class="card">
        <h2>알림 (Discord/Slack webhook)</h2>
```

- [ ] **Step 2: app.js — 소스 경로 로드/저장**

`engine/web/ui/app.js`의 `loadAlertWebhook` 함수 정의 바로 위(즉 `/* ---------- alert webhook ---------- */` 줄 앞)에 추가:
```javascript
/* ---------- source paths + db jobs ---------- */
async function loadSrcPaths() {
  const el = $("#srcPaths"); if (!el) return;
  try { const d = await (await api("/api/source-paths")).json(); el.value = d.paths || "/home"; } catch (e) {}
}
async function saveSrcPaths() {
  const m = $("#srcMsg"); m.className = "msg"; m.textContent = "저장 중…";
  try {
    const r = await api("/api/source-paths", { method: "POST", body: JSON.stringify({ paths: $("#srcPaths").value.trim() || "/home" }) });
    if (!r.ok) { m.className = "msg fail"; m.textContent = "✕ " + (await r.text()); return; }
    m.className = "msg ok"; m.textContent = "✓ 저장됨 (다음 백업부터 적용)";
  } catch (e) { if (String(e.message) !== "unauthorized") { m.className = "msg fail"; m.textContent = "✕ " + e.message; } }
}
let dbJobs = [];
const DB_TYPES = ["postgres", "mongodb", "redis"];
async function loadDBJobs() {
  if (!$("#dbTable")) return;
  try {
    const d = await (await api("/api/db-jobs")).json();
    dbJobs = d.jobs || [];
    $("#dbDefaults").style.display = d.defaults ? "" : "none";
    renderDBJobs();
  } catch (e) {}
}
function renderDBJobs() {
  const t = $("#dbTable");
  const head = "<thead><tr><th>이름</th><th>유형</th><th>컨테이너</th><th>데이터 경로</th><th>사용</th><th></th></tr></thead>";
  const rows = dbJobs.map((j, i) => `<tr>
    <td><input data-i="${i}" data-k="name" value="${esc(j.name)}" style="width:90px"></td>
    <td><select data-i="${i}" data-k="type">${DB_TYPES.map(t => `<option${t === j.type ? " selected" : ""}>${t}</option>`).join("")}</select></td>
    <td><input data-i="${i}" data-k="container" value="${esc(j.container)}" style="width:100px"></td>
    <td><input data-i="${i}" data-k="data" value="${esc(j.data || "")}" placeholder="(없으면 제외 안 함)" style="width:160px"></td>
    <td style="text-align:center"><input type="checkbox" data-i="${i}" data-k="enabled"${j.enabled ? " checked" : ""}></td>
    <td><button class="btn-ghost" data-del="${i}" style="padding:2px 8px">삭제</button></td></tr>`).join("");
  const empty = dbJobs.length ? "" : `<tr><td colspan="6" class="empty">DB 작업 없음 — DB를 사용하지 않으면 이대로 저장하세요</td></tr>`;
  t.innerHTML = head + "<tbody>" + (rows || empty) + "</tbody>";
}
function dbReadField(el) {
  const i = +el.getAttribute("data-i"), k = el.getAttribute("data-k");
  if (!dbJobs[i]) return;
  dbJobs[i][k] = k === "enabled" ? el.checked : el.value;
}
async function saveDBJobs() {
  const m = $("#dbMsg"); m.className = "msg"; m.textContent = "저장 중…";
  try {
    const r = await api("/api/db-jobs", { method: "POST", body: JSON.stringify({ jobs: dbJobs }) });
    if (!r.ok) { m.className = "msg fail"; m.textContent = "✕ " + (await r.text()); return; }
    const d = await r.json();
    dbJobs = d.jobs || []; $("#dbDefaults").style.display = "none"; renderDBJobs();
    m.className = "msg ok"; m.textContent = "✓ 저장됨 (다음 백업부터 적용)";
  } catch (e) { if (String(e.message) !== "unauthorized") { m.className = "msg fail"; m.textContent = "✕ " + e.message; } }
}
```

- [ ] **Step 3: app.js — 와이어링 + 부팅 로드**

`engine/web/ui/app.js`의 `$("#alertSave") && (...)` 줄 바로 위에 추가:
```javascript
$("#saveSrc") && ($("#saveSrc").onclick = saveSrcPaths);
$("#dbAdd") && ($("#dbAdd").onclick = () => { dbJobs.push({ name: "db" + (dbJobs.length + 1), type: "postgres", container: "", data: "", enabled: true }); renderDBJobs(); });
$("#dbSave") && ($("#dbSave").onclick = saveDBJobs);
$("#dbTable") && $("#dbTable").addEventListener("input", e => { if (e.target.matches("input,select")) dbReadField(e.target); });
$("#dbTable") && $("#dbTable").addEventListener("change", e => { if (e.target.matches("input[type=checkbox],select")) dbReadField(e.target); });
$("#dbTable") && $("#dbTable").addEventListener("click", e => { const d = e.target.closest("button[data-del]"); if (d) { dbJobs.splice(+d.getAttribute("data-del"), 1); renderDBJobs(); } });
```

부팅 IIFE의 `loadAlertWebhook();` 줄 다음에 추가:
```javascript
  loadSrcPaths();
  loadDBJobs();
```

- [ ] **Step 4: style.css — db-table 스타일**

`engine/web/ui/style.css` 끝에 추가:
```css
.db-table { width:100%; border-collapse:collapse; font-size:.84rem; }
.db-table th { text-align:left; font-size:.72rem; color:var(--dim,#8b949e); text-transform:uppercase; letter-spacing:.06em; padding:4px 6px; }
.db-table td { padding:4px 6px; border-top:1px solid var(--border-soft); }
.db-table input[type=text], .db-table input:not([type]), .db-table select { width:100%; }
```

- [ ] **Step 5: config.env.example 두 곳에 SOURCE_PATHS 문서화**

`engine/config.env.example`와 `config/config.env.example` 각각에서 `STAGING_ROOT=...` 줄 위에 추가:
```
# 백업 소스 경로(공백 구분, 기본 /home). /var/lib/docker/volumes 는 항상 포함됩니다.
# UI(설정 탭)에서 바꾸면 /config/source-paths 파일로 저장되며 이 env보다 우선합니다.
SOURCE_PATHS=/home
```

- [ ] **Step 6: 캐시버스트 + JS 문법 검사**

`engine/web/ui/app.js` 1번 줄을 `const BUILD = "ui-2026-06-09a";`로 바꾸고:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && sed -i 's/v=20260608c/v=20260609a/g' index.html && echo "count=$(grep -c 20260609a index.html)" && node --check app.js && echo "JS OK"
```
Expected: count=5, JS OK

- [ ] **Step 7: 커밋**

```bash
cd /home/ubuntu/backup-stack && git add engine/web/ui/index.html engine/web/ui/app.js engine/web/ui/style.css engine/config.env.example config/config.env.example
git commit -m "feat(ui): 소스 경로 필드 + DB 작업 테이블(추가/삭제/사용) + 캐시버스트"
```

---

## Task 5: 빌드·검증·재기동

**Files:** (없음 — 통합 검증)

- [ ] **Step 1: Go 빌드·vet·테스트 (최종)**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: ALL_OK

- [ ] **Step 2: 이미지 재빌드(jq 포함) + 기동 + 라우트 + jq 확인**

Run:
```bash
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 6 && \
  curl -fsS http://localhost:8088/healthz && echo " healthz" && \
  echo "/api/db-jobs -> $(curl -s -o /dev/null -w '%{http_code}' http://localhost:8088/api/db-jobs)" && \
  docker exec backupstack_engine jq --version && \
  echo "BUILD: $(curl -s 'http://localhost:8088/app.js?v=20260609a' | head -1)"
```
Expected: `ok healthz`, `/api/db-jobs -> 401`, `jq-1.x`, `BUILD: const BUILD = "ui-2026-06-09a";`

- [ ] **Step 3: 수동 검증 (브라우저)**

1. 설정 탭 "백업 대상 경로 · 데이터베이스" 카드 표시. 소스 경로에 `/home`, DB 테이블에 기본 3행 + "기본값(미저장)" 배지.
2. 소스 경로 `/home /srv` 저장 → "✓ 저장됨".
3. DB 한 행 삭제 + "DB 작업 저장" → 배지 사라지고 그 DB 제외됨. `/config/db-jobs.json` 생성 확인.
4. DB 전부 삭제 후 저장 → 빈 목록 저장됨(DB 없는 서버).
5. "DB 추가"로 행 추가 → 유형 선택·이름 입력 → 저장.

- [ ] **Step 4: db-jobs.json 동작 확인 (컨테이너 내부)**

Run:
```bash
docker exec backupstack_engine sh -c 'cat /config/db-jobs.json 2>/dev/null && echo "---" && jq -r ".[] | [.name,.type,.container,(.data//\"\"),(.enabled|tostring)] | @tsv" /config/db-jobs.json 2>/dev/null || echo "no file yet (defaults active)"'
```
Expected: 저장했으면 JSON + tsv 출력, 안 했으면 "no file yet"

---

## 검증 체크리스트 (spec 대비)

- [x] 소스 경로 설정(기본 /home) + volumes 고정 — srcpaths.go(/api/source-paths) + home-backup.sh
- [x] DB 작업 목록 추가·삭제·on/off — dbjobs.go + UI 테이블
- [x] 내장 3종 유형 제한 — `dbJobTypes` + UI select
- [x] 하위호환(파일 없으면 기존 3종) — `run_db_jobs` else 분기 + readDBJobs defaults
- [x] DB 없는 서버(빈 목록) — `validDBJobs([])` 허용 + 순회 0회
- [x] 데이터 경로로 이중백업 제외(빈 값 허용) — 덤프 함수 `[ -n "$data" ]`
- [x] 인증정보 secrets 유지 — 덤프 함수 env 그대로
- [x] jq 의존성 — Dockerfile apk
- [x] 안전가드(소스경로 위험 루트) — validSourcePaths
