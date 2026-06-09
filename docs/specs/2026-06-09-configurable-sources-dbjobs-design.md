# 백업 소스 경로 설정 + DB 작업 목록(추가/삭제) 설계

> 작성일: 2026-06-09
> 상태: 승인됨 (구현 계획 대기)

## 목표

하드코딩된 두 가지를 설정 가능하게 만든다. 단, **기본값은 현재 동작 그대로**이고 새
설정 파일이 없으면 기존과 똑같이 동작한다(하위호환).

1. **백업 소스 경로**: 지금 `/home` 고정 → 사용자가 바꿀 수 있게(`/opt/docker` 등). 단
   `/var/lib/docker/volumes`는 항상 포함(고정).
2. **DB 덤프 작업 목록**: 지금 postgres·mongo·redis 3종 고정 호출 → 사용자가 **추가·삭제·
   편집·on/off** 할 수 있는 목록으로. DB 없는 서버도 지원.

## 결정 사항 (확정)

- DB 유형은 **내장 3종(postgres/mongodb/redis)으로 제한**. 임의 유형·커스텀 덤프명령은
  범위 밖. 목록의 각 항목이 이 3종 중 하나를 `type`으로 고른다(같은 유형 여러 개 가능).
- 소스 경로는 `/home` 자리만 설정, `/var/lib/docker/volumes`는 고정.
- DB **인증정보**(PG_USER, MONGO_USER/PASS, REDIS_PASS)는 지금처럼 `secrets/db-creds.env`
  파일에 두고 UI 비노출. 같은 유형 작업들은 그 유형의 자격증명을 공유.

## 배경 (현재 구조)

- `home-backup.sh`: `BACKUP_PATHS=(/home /var/lib/docker/volumes)` 고정, Phase1에서
  `dump_postgres; dump_mongodb; dump_redis` 3종 고정 호출.
- `db-dump.sh`: 각 함수가 `<X>_CONTAINER` env로 컨테이너를 찾고, 덤프 성공 시
  `/home/docker/<db>` 경로를 `DYNAMIC_EXCLUDES`에 넣어 원본 이중백업을 막음(경로 하드코딩).
- 상태 판정: 컨테이너 없으면 `ABSENT`, 멈춤이면 `SKIPPED_STOPPED`, 실행 중이면 덤프.
- `/config` 쓰기 가능, `/secrets` 읽기 전용. 설정은 `/config/config.env`.

## 컴포넌트 설계

### 1) 백업 소스 경로 — `SOURCE_PATHS`

- 새 설정키 `SOURCE_PATHS`(config.env), 기본 `/home`. 공백 구분 여러 경로 허용.
- `home-backup.sh`:
  ```bash
  read -ra _src <<< "${SOURCE_PATHS:-/home}"
  BACKUP_PATHS=("${_src[@]}" /var/lib/docker/volumes)
  ```
- 안전가드: 각 경로는 `/`로 시작(절대경로), `..` 없음, `/`·`/etc`·`/root` 등 위험 루트 금지.
  잘못되면 `die`. (Go 검증 + bash 재확인 이중)

### 2) DB 작업 목록 — `/config/db-jobs.json`

스키마(배열):
```json
[
  { "name": "pg-main", "type": "postgres", "container": "postgres",
    "data": "/home/docker/postgres/data", "enabled": true }
]
```
- `type` ∈ {postgres, mongodb, redis}. `name`은 식별/요약용(고유, `[A-Za-z0-9_.-]`).
- `data`는 덤프 성공 시 제외할 원본 경로(이중백업 방지). 빈 값이면 제외 안 함.
- `container`는 대상 컨테이너 이름.

**하위호환**: 파일이 **없으면** 현재 동작 그대로 — `home-backup.sh`가 기존
`dump_postgres; dump_mongodb; dump_redis`(기존 env·기본 제외경로)를 호출. 파일이 **있으면**
목록을 순회한다.

`db-dump.sh` 리팩터: 각 덤프 함수를 인자화.
```bash
dump_postgres <name> <container> <data>
dump_mongodb  <name> <container> <data>
dump_redis    <name> <container> <data>
```
- 컨테이너 없음/멈춤 판정·요약(`db_summary_add <name> …`)은 그대로. 요약 키가 고정
  "postgres" 대신 작업 `name`이 된다.
- 덤프 성공 시 `data`가 비어 있지 않으면 `DYNAMIC_EXCLUDES+=("$data")`.
- 인증은 유형별 전역 env 그대로 사용(PG_USER 등).

목록 순회(파일 있을 때), `home-backup.sh`:
```bash
if [ -s /config/db-jobs.json ]; then
  while IFS=$'\t' read -r name type container data enabled; do
    [ "$enabled" = "true" ] || { db_summary_add "$name" DISABLED "off"; continue; }
    case "$type" in
      postgres) dump_postgres "$name" "$container" "$data" ;;
      mongodb)  dump_mongodb  "$name" "$container" "$data" ;;
      redis)    dump_redis    "$name" "$container" "$data" ;;
      *) db_summary_add "$name" BAD_TYPE "$type" ;;
    esac
  done < <(jq -r '.[] | [.name,.type,.container,.data,(.enabled|tostring)] | @tsv' /config/db-jobs.json)
else
  dump_postgres postgres "${PG_CONTAINER:-postgres}" /home/docker/postgres/data
  dump_mongodb  mongodb  "${MONGO_CONTAINER:-mongodb}" /home/docker/mongodb
  dump_redis    redis    "${REDIS_CONTAINER:-redis}" /home/docker/redis
fi
```
- **`jq` 필요** → 런타임 이미지 apk에 추가.

### 3) Go — `engine/web/dbjobs.go` (신규)

- `GET /api/db-jobs` → 현재 목록. 파일 없으면 **기본 3개**를 합성해 반환(`enabled:true`,
  기본 컨테이너·경로) + `{defaults:true}` 플래그(UI가 "아직 저장 안 됨" 표시).
- `POST /api/db-jobs` (CSRF) → 검증 후 `/config/db-jobs.json` 원자적 기록. 빈 배열도 허용
  (DB 없는 서버 = 빈 목록).
- 검증 `validDBJob`: `type`∈3종, `name` 고유·`[A-Za-z0-9_.-]`·1~64, `container` 동일 규칙,
  `data`는 빈 값 또는 절대경로+`..` 없음+제어문자 없음.

### 4) Go — config.go에 `SOURCE_PATHS` 추가

- `Config`에 `SourcePaths string` 필드, known key로 로드/저장(기본 `/home`).
- `Validate()`: 각 토큰 절대경로·`..` 금지·위험 루트(`/`,`/etc`,`/root`,`/var`,`/boot` 등) 금지.

### 5) UI — 설정 탭 "백업 대상 · DB" 카드

- **소스 경로** 입력(`SOURCE_PATHS`) + "`/var/lib/docker/volumes`는 항상 포함" 안내.
  (기존 `/api/config` 저장 흐름에 필드 추가.)
- **DB 작업 테이블**: 행마다 [이름 · 유형(select) · 컨테이너 · 데이터경로 · 사용(toggle) ·
  삭제]. **"DB 추가"** 버튼으로 빈 행 추가. **저장**으로 `/api/db-jobs` POST. 빈 목록 저장 가능.
  `defaults:true`면 "기본값(미저장)" 배지.

## 데이터 흐름

```
[설정 탭]
  소스경로 → /api/config (config.env: SOURCE_PATHS)
  DB 테이블 → GET/POST /api/db-jobs (/config/db-jobs.json)
[백업]
  home-backup.sh: SOURCE_PATHS + /var/lib/docker/volumes 백업
    Phase1: db-jobs.json 있으면 목록 순회, 없으면 기존 3종 기본
```

## 오류 처리

- 잘못된 소스경로/위험 루트: config 저장 400, 백업 스크립트도 `die`로 2차 차단.
- 잘못된 DB 작업(유형·이름·경로): POST 400 + 사유.
- `jq` 파싱 실패(손상 JSON): 백업 스크립트가 `die`(빈 저장소 생성 방지). UI는 항상 유효
  JSON만 쓰므로 정상 경로에선 발생 안 함.
- 같은 유형 여러 작업이 같은 컨테이너를 가리켜도 동작은 함(덤프 2회) — 막지 않음(사용자 책임).

## 테스트

- 단위(Go): `validDBJob`(유형/이름/컨테이너/경로 정상·거부), `SOURCE_PATHS` 검증
  (절대경로 통과, `/etc`·`..`·상대경로 거부).
- 수동: 기본값(파일 없음)에서 백업 → 기존과 동일; DB 1개 삭제 후 저장 → 그 DB 덤프 안 함;
  DB 전부 삭제(빈 목록) → DB 단계 건너뜀; 소스경로 `/home /srv` → 둘 다 백업.

## 변경 파일

- `engine/Dockerfile` — 런타임 apk에 `jq` 추가
- `engine/scripts/home-backup.sh` — `SOURCE_PATHS`, db-jobs 순회/폴백
- `engine/scripts/lib/db-dump.sh` — 덤프 함수 인자화(name/container/data) + DISABLED 처리
- `engine/web/config.go` — `SourcePaths` 키 + 검증
- `engine/web/dbjobs.go` (신규) — 목록 CRUD + 검증
- `engine/web/dbjobs_test.go` (신규) — 검증 단위테스트
- `engine/web/api.go` — `/api/db-jobs` 라우트
- `engine/web/ui/{index.html,app.js,style.css}` — 소스경로 필드 + DB 테이블
- `engine/config.env.example`, `config/config.env.example` — `SOURCE_PATHS` 문서화

## 범위 밖 (YAGNI)

- 내장 3종 외 DB 유형(MySQL 등)·커스텀 덤프 명령.
- 작업별 개별 자격증명(유형별 전역 공유 유지).
- DB 데이터 경로 자동 탐지.
