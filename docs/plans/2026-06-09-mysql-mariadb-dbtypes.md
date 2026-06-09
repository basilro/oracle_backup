# DB 덤프 유형 추가: MySQL / MariaDB 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** DB 작업 목록 유형에 mysql·mariadb를 추가해, 대상 컨테이너 안에서 mysqldump/mariadb-dump로 일관 스냅샷을 만든다. ChromaDB는 추가하지 않는다(raw 백업으로 충분).

**Architecture:** 유형 3곳(dbjobs.go·app.js·db-dump.sh)에 mysql·mariadb 등록. db-dump.sh에 `_dump_mysql_family` 공용 헬퍼 + `dump_mysql`/`dump_mariadb` 진입점 추가. 자격증명은 `secrets/db-creds.env`의 `MYSQL_USER`(기본 root)·`MYSQL_PASS` 공용. 엔진 이미지 의존성 추가 없음.

**Tech Stack:** Go, bash, mysqldump/mariadb-dump(대상 컨테이너 내부), vanilla JS.

**설계 문서:** `docs/specs/2026-06-09-mysql-mariadb-dbtypes-design.md`

---

## 파일 구조

| 파일 | 변경 |
|------|------|
| `engine/web/dbjobs.go` | `dbJobTypes`에 mysql·mariadb |
| `engine/web/dbjobs_test.go` | 두 유형 허용 테스트 |
| `engine/scripts/lib/db-dump.sh` | `_dump_mysql_family`+`dump_mysql`+`dump_mariadb` + case |
| `engine/web/ui/app.js` | `DB_TYPES` + BUILD 스탬프 |
| `engine/web/ui/index.html` | DB 카드 안내 + 매뉴얼 보강 + 캐시버스트 |
| `README.md` | MYSQL 자격증명 문서화 |

---

## Task 1: Go — 유형 등록 + 테스트

**Files:**
- Modify: `engine/web/dbjobs.go`
- Modify: `engine/web/dbjobs_test.go`

- [ ] **Step 1: dbJobTypes에 mysql·mariadb 추가**

`engine/web/dbjobs.go`의:
```go
var dbJobTypes = map[string]bool{"postgres": true, "mongodb": true, "redis": true}
```
을 아래로 교체:
```go
var dbJobTypes = map[string]bool{"postgres": true, "mongodb": true, "redis": true, "mysql": true, "mariadb": true}
```

- [ ] **Step 2: 테스트에 두 유형 허용 케이스 추가**

`engine/web/dbjobs_test.go`의 `TestValidDBJob` 끝(마지막 `}` 앞)에 추가:
```go
	for _, ty := range []string{"mysql", "mariadb"} {
		if err := validDBJob(DBJob{Name: "x", Type: ty, Container: "c"}); err != nil {
			t.Errorf("%s should be valid: %v", ty, err)
		}
	}
```

- [ ] **Step 3: 빌드 + 테스트**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go test -run 'TestValidDBJob' -v && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: TestValidDBJob PASS, ALL_OK

- [ ] **Step 4: 커밋**

```bash
git add engine/web/dbjobs.go engine/web/dbjobs_test.go
git commit -m "feat(dbjobs): mysql·mariadb 유형 허용"
```

---

## Task 2: 엔진 — mysql/mariadb 덤프 함수 + case

**Files:**
- Modify: `engine/scripts/lib/db-dump.sh`

- [ ] **Step 1: dump_mysql/dump_mariadb + 공용 헬퍼 추가**

`engine/scripts/lib/db-dump.sh`의 `dump_redis` 함수 정의가 끝나는 `}` 다음(즉 `# run_db_jobs:` 주석 바로 위)에 추가:
```bash
dump_mysql()   { _dump_mysql_family "$1" "$2" "$3" mysqldump; }
dump_mariadb() { _dump_mysql_family "$1" "$2" "$3" mariadb-dump; }

# _dump_mysql_family: $1=name $2=container $3=data $4=preferred dump binary.
# mysqldump/mariadb-dump 는 대상 컨테이너 안에서 실행한다(엔진 이미지에 불필요).
_dump_mysql_family() {
    local name="${1:-mysql}" c="${2:-}" data="${3:-}" prefer="${4:-mysqldump}"
    [ -n "$c" ] || { return 0; }
    container_exists "$c" || { db_summary_add "$name" ABSENT "no container"; return 0; }
    if ! container_running "$c"; then db_summary_add "$name" SKIPPED_STOPPED "raw data backed up"; return 0; fi
    local out="$STAGE/$name"; mkdir -p "$out"
    # 컨테이너 안에서 사용할 덤프 바이너리 선택: 선호 바이너리 → mysqldump 폴백.
    local bin
    bin=$(docker exec "$c" sh -c "command -v $prefer || command -v mysqldump" 2>/dev/null | head -n1)
    [ -n "$bin" ] || { db_summary_add "$name" DUMP_FAILED "no mysqldump/mariadb-dump in container"; die "mysql dump tool missing in $c"; }
    local user="${MYSQL_USER:-root}" pass="${MYSQL_PASS:-}"
    # 비밀번호가 있으면 -p"..."(공백 없이), 없으면 -p 생략. 자격증명은 stderr/argv 노출
    # 최소화를 위해 컨테이너 내부 셸에서 조립한다(엔진 로그엔 비번이 남지 않음).
    log "dump $name c=$c bin=$bin"
    local rc=0
    if [ -n "$pass" ]; then
        docker exec -e MYSQL_PWD="$pass" "$c" "$bin" -u"$user" --single-transaction --all-databases 2>/dev/null | gzip -1 > "$out/all.sql.gz" || rc=$?
    else
        docker exec "$c" "$bin" -u"$user" --single-transaction --all-databases 2>/dev/null | gzip -1 > "$out/all.sql.gz" || rc=$?
    fi
    [ "$rc" -eq 0 ] || { db_summary_add "$name" DUMP_FAILED "dump error rc=$rc"; die "$name dump failed"; }
    local sz; sz=$(stat -c%s "$out/all.sql.gz")
    [ "$sz" -gt 1000 ] || { db_summary_add "$name" DUMP_FAILED "too small ($sz)"; die "$name dump too small"; }
    db_summary_add "$name" DUMPED_OK "${sz}B"; [ -n "$data" ] && DYNAMIC_EXCLUDES+=("$data")
}
```

> 참고: `MYSQL_PWD` 환경변수로 비밀번호를 전달해 `-p` argv 노출을 피한다(컨테이너의 mysql
> 클라이언트가 표준 지원). `--single-transaction`은 InnoDB 일관 스냅샷.

- [ ] **Step 2: run_db_jobs case에 mysql·mariadb 추가**

`engine/scripts/lib/db-dump.sh`의:
```bash
                postgres) dump_postgres "$name" "$container" "$data" ;;
                mongodb)  dump_mongodb  "$name" "$container" "$data" ;;
                redis)    dump_redis    "$name" "$container" "$data" ;;
                *) db_summary_add "$name" BAD_TYPE "$type" ;;
```
을 아래로 교체:
```bash
                postgres) dump_postgres "$name" "$container" "$data" ;;
                mongodb)  dump_mongodb  "$name" "$container" "$data" ;;
                redis)    dump_redis    "$name" "$container" "$data" ;;
                mysql)    dump_mysql    "$name" "$container" "$data" ;;
                mariadb)  dump_mariadb  "$name" "$container" "$data" ;;
                *) db_summary_add "$name" BAD_TYPE "$type" ;;
```

- [ ] **Step 3: bash 문법 검사**

Run:
```bash
cd /home/ubuntu/backup-stack && bash -n engine/scripts/lib/db-dump.sh && echo "문법 OK"
```
Expected: 문법 OK

- [ ] **Step 4: 커밋**

```bash
git add engine/scripts/lib/db-dump.sh
git commit -m "feat(backup): mysql·mariadb 덤프(컨테이너 내 mysqldump/mariadb-dump, MYSQL_PWD)"
```

---

## Task 3: UI + 문서 + 캐시버스트 + 재기동

**Files:**
- Modify: `engine/web/ui/app.js`
- Modify: `engine/web/ui/index.html`
- Modify: `README.md`

- [ ] **Step 1: DB_TYPES에 mysql·mariadb 추가**

`engine/web/ui/app.js`의:
```javascript
const DB_TYPES = ["postgres", "mongodb", "redis"];
```
을 아래로 교체:
```javascript
const DB_TYPES = ["postgres", "mongodb", "redis", "mysql", "mariadb"];
```

- [ ] **Step 2: DB 카드 안내 문구에 mysql/mariadb·chroma 한 줄 추가**

`engine/web/ui/index.html`의 DB 작업 안내 문단(아래) 끝에 한 문장 추가:
```html
          <p class="dim" style="margin:0 0 10px;font-size:.82rem">덤프할 DB를 추가/삭제합니다. 컨테이너가 없거나 멈춰 있으면 자동으로 건너뜁니다. 데이터 경로는 덤프 성공 시 원본 이중 백업을 막기 위해 제외됩니다(비우면 제외 안 함). 인증정보는 <code>secrets/db-creds.env</code>에서 관리합니다. <b>모두 삭제하면 DB 덤프를 건너뜁니다.</b></p>
```
을 아래로 교체(끝에 문장 추가):
```html
          <p class="dim" style="margin:0 0 10px;font-size:.82rem">덤프할 DB를 추가/삭제합니다. 컨테이너가 없거나 멈춰 있으면 자동으로 건너뜁니다. 데이터 경로는 덤프 성공 시 원본 이중 백업을 막기 위해 제외됩니다(비우면 제외 안 함). 인증정보는 <code>secrets/db-creds.env</code>에서 관리합니다(postgres=PG_USER, mongo=MONGO_USER/PASS, redis=REDIS_PASS, mysql·mariadb=MYSQL_USER/MYSQL_PASS). <b>모두 삭제하면 DB 덤프를 건너뜁니다.</b> ChromaDB 등 덤프 도구가 없는 DB는 데이터 디렉터리가 그대로 백업되며, 완전 일관 스냅샷이 필요하면 백업 시간대에 해당 컨테이너를 잠시 멈추세요.</p>
```

- [ ] **Step 3: 매뉴얼 탭 3번(설정 탭) 카드에 자격증명 한 줄 보강**

`engine/web/ui/index.html`의 매뉴얼 3번 카드에서:
```html
          <li><b>백업 대상 경로 · DB</b> — 소스 경로(기본 <code>/home</code>, <code>/var/lib/docker/volumes</code>는 항상 포함)와 DB 덤프 작업 목록(추가·삭제·on/off).</li>
```
을 아래로 교체:
```html
          <li><b>백업 대상 경로 · DB</b> — 소스 경로(기본 <code>/home</code>, <code>/var/lib/docker/volumes</code>는 항상 포함)와 DB 덤프 작업 목록(추가·삭제·on/off). 유형: postgres·mongodb·redis·mysql·mariadb. 자격증명은 <code>secrets/db-creds.env</code>(mysql·mariadb는 <code>MYSQL_USER</code>·<code>MYSQL_PASS</code>). ChromaDB 등은 데이터 디렉터리로 그대로 백업됩니다(완전 일관은 백업창에 컨테이너 정지).</li>
```

- [ ] **Step 4: README에 MYSQL 자격증명 문서화**

`README.md`에서 `secrets/db-creds.env` 또는 DB 자격증명을 설명하는 부분을 찾아 mysql·mariadb 항목을 추가한다. 해당 설명이 있는 위치(예: `PG_USER`, `MONGO_USER` 등을 나열한 곳)에 다음을 추가:
```
MYSQL_USER=root        # mysql·mariadb 공용 (기본 root)
MYSQL_PASS=...         # 비우면 -p 생략
```
> README에 db-creds 설명 블록이 없으면, "secrets/db-creds.env" 를 언급하는 가장 가까운 위치에 위 두 줄과 한 문장 설명을 추가한다(없으면 이 Step은 건너뛰고 매뉴얼 탭 안내로 충분).

- [ ] **Step 5: 캐시버스트 + JS 문법**

`engine/web/ui/app.js` 1번 줄을 `const BUILD = "ui-2026-06-09c";`로 바꾸고:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && sed -i 's/v=20260609b/v=20260609c/g' index.html && echo "count=$(grep -c 20260609c index.html)" && node --check app.js && echo "JS OK"
```
Expected: count=5, JS OK

- [ ] **Step 6: 빌드·vet·테스트 + 재기동 + 검증**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 6 && \
  curl -fsS http://localhost:8088/healthz && echo " healthz" && \
  echo "BUILD: $(curl -s 'http://localhost:8088/app.js?v=20260609c' | head -1)" && \
  echo "DB_TYPES: $(curl -s 'http://localhost:8088/app.js?v=20260609c' | grep -o 'DB_TYPES = \[[^]]*\]')"
```
Expected: ALL_OK, `ok healthz`, BUILD ui-2026-06-09c, DB_TYPES에 mysql·mariadb 포함

- [ ] **Step 7: 수동 검증 (브라우저)**

설정 탭 → DB 작업 → "DB 추가" → 유형 드롭다운에 postgres·mongodb·redis·**mysql·mariadb** 노출. mysql 작업 추가·저장 → 백업 시 컨테이너 있으면 `all.sql.gz` 생성/DUMPED_OK, 없으면 ABSENT.

- [ ] **Step 8: 커밋**

```bash
cd /home/ubuntu/backup-stack && git add engine/web/ui/app.js engine/web/ui/index.html README.md
git commit -m "feat(ui): DB 유형 드롭다운에 mysql·mariadb + 자격증명 안내 + 캐시버스트"
```

---

## 검증 체크리스트 (spec 대비)

- [x] mysql·mariadb 유형 등록(3곳) — dbjobs.go / app.js / db-dump.sh
- [x] 컨테이너 내 mysqldump/mariadb-dump + 폴백 — `_dump_mysql_family` `command -v`
- [x] 자격증명 MYSQL_USER(root)/MYSQL_PASS 공용, MYSQL_PWD로 비번 비노출
- [x] --single-transaction 일관 + 크기 검증
- [x] ChromaDB 미추가 + raw 백업 안내(카드·매뉴얼)
- [x] 엔진 이미지 의존성 추가 없음
