# DB 덤프 유형 추가: MySQL / MariaDB 설계

> 작성일: 2026-06-09
> 상태: 승인됨 (구현 계획 대기)
> 선행: `2026-06-09-configurable-sources-dbjobs-design.md` (DB 작업 목록 모델)

## 목표

DB 작업 목록의 선택 가능한 유형에 **mysql**·**mariadb**를 추가한다. 둘 다 대상 컨테이너
안에서 덤프 도구(mysqldump / mariadb-dump)를 실행해 트랜잭션 일관 스냅샷을 만든다.

## 결정 사항 (확정)

- **ChromaDB는 추가하지 않는다.** chroma 데이터는 볼륨/디렉터리로 restic이 이미 raw 백업하며,
  "단일 tar"는 raw 대비 일관성 이득이 미미하다(트랜잭션 원자성 아님). 진짜 일관성은 "백업 중
  컨테이너 정지"뿐이라 별도 기능이다. 매뉴얼에 "chroma는 raw로 백업, 완전 일관 원하면 백업창에
  정지"만 한 줄 안내한다.
- **MySQL·MariaDB 자격증명**: `secrets/db-creds.env`에 `MYSQL_USER`(기본 root)·`MYSQL_PASS`
  추가, mysql·mariadb **공용**. UI 비노출(기존 원칙).
- 엔진 이미지에 **새 의존성 없음**: mysqldump/mariadb-dump는 대상 컨테이너 안에서 실행,
  tar·gzip은 이미 있음.

## 배경 (현재 구조)

- 유형은 3곳에 정의: `engine/web/dbjobs.go`의 `dbJobTypes`, `engine/web/ui/app.js`의
  `DB_TYPES`, `engine/scripts/lib/db-dump.sh`의 `run_db_jobs` case.
- 덤프 함수는 `(name, container, data)` 인자. 덤프 성공 시 `data`(호스트 경로)를
  `DYNAMIC_EXCLUDES`에 더해 원본 이중 백업을 막는다. `data`는 선택(빈 값이면 제외 안 함).
- 컨테이너 없음 → ABSENT, 멈춤 → SKIPPED_STOPPED, 실행 중 → 덤프(기존 헬퍼 재사용).
- 자격증명은 `home-backup.sh`가 `/secrets/db-creds.env`를 source 해 env로 제공.

## 컴포넌트 설계

### 1) 유형 등록 (3곳)
- `dbJobTypes`에 `"mysql": true, "mariadb": true` 추가.
- `DB_TYPES` 배열에 `"mysql", "mariadb"` 추가(UI 드롭다운 자동 반영).
- `run_db_jobs` case에 `mysql)`·`mariadb)` 분기 추가.

### 2) 덤프 함수 (db-dump.sh)
공용 헬퍼 + 두 진입점:
```bash
dump_mysql()   { _dump_mysql_family "$1" "$2" "$3" mysqldump; }
dump_mariadb() { _dump_mysql_family "$1" "$2" "$3" mariadb-dump; }

_dump_mysql_family() {  # $1=name $2=container $3=data $4=preferred-bin
    name container data 판정(기존 패턴: 없음 ABSENT, 멈춤 SKIPPED_STOPPED)
    user="${MYSQL_USER:-root}", pass="${MYSQL_PASS:-}"
    bin 선택: 컨테이너 안에 $4 있으면 그걸, 없으면 mysqldump 폴백
    docker exec "$c" sh -c '<bin> -u<user> [-p<pass>] --single-transaction --all-databases' | gzip -1 > all.sql.gz
    크기 검증(>1000B) → DUMPED_OK, data 비어있지 않으면 DYNAMIC_EXCLUDES+=("$data")
}
```
- 비밀번호가 비어 있으면 `-p` 생략(소켓/trust 환경 대비). 있으면 `-p"$pass"`(공백 없이).
- `--single-transaction`으로 InnoDB 일관 스냅샷. (MyISAM은 일관성 보장 안 되지만 일반적
  사용엔 충분; 필요 시 후속.)
- bin 선택은 `docker exec "$c" sh -c 'command -v <bin>'`로 존재 확인 후 폴백.

### 3) 검증 (dbjobs.go)
- `dbJobTypes`에 두 유형 추가만으로 `validDBJob`이 자동 허용. `data` 규칙은 기존과 동일(선택).

### 4) UI
- `DB_TYPES`에 추가 → 드롭다운에 자동 노출. 카드 안내 문구에 "mysql·mariadb 추가됨" 반영.

### 5) 문서
- `/secrets/`는 .gitignore이므로 예시 파일을 추가할 수 없다. 대신 **README와 매뉴얼 탭**에
  `secrets/db-creds.env`의 `MYSQL_USER`(기본 root)·`MYSQL_PASS` 항목을 문서화한다.
- 매뉴얼 탭에 mysql/mariadb 자격증명 위치 + "chroma는 raw로 백업, 완전 일관 원하면 백업창에
  컨테이너 정지" 안내를 추가한다.

## 오류 처리

- 비밀번호 틀림/접속 실패: gzip 결과가 작거나 비어 덤프 검증(>1000B)에서 `die` → 백업 실패로
  표면화(부분 성공 방치 안 함).
- mariadb-dump·mysqldump 모두 없음: `command -v` 폴백 실패 → DUMP_FAILED + die.

## 테스트

- 단위(Go): `validDBJob`이 `mysql`·`mariadb` 유형을 통과, 미지원 유형은 여전히 거부.
- 수동: mysql/mariadb 컨테이너에 대해 작업 추가 → 백업 시 `all.sql.gz` 생성·요약 DUMPED_OK,
  컨테이너 없는 유형은 ABSENT로 건너뜀.

## 변경 파일

- `engine/web/dbjobs.go` — `dbJobTypes`에 mysql·mariadb
- `engine/web/dbjobs_test.go` — 두 유형 허용 테스트
- `engine/scripts/lib/db-dump.sh` — `dump_mysql`·`dump_mariadb`·`_dump_mysql_family` + case
- `engine/web/ui/app.js` — `DB_TYPES` + 캐시버스트
- `engine/web/ui/index.html` — 카드/매뉴얼 안내(mysql·mariadb·chroma) + 캐시버스트
- `README.md` — `secrets/db-creds.env`의 MYSQL 자격증명 문서화

## 범위 밖 (YAGNI)

- ChromaDB 유형(위 결정).
- 백업창 컨테이너 정지(pre-backup 훅) — 별도 기능.
- DB별 개별 자격증명(유형별 전역 공유 유지).
- 특정 DB만 덤프(--databases 일부) — 현재는 --all-databases.
