# backup-stack

`/home` + Docker named volume을 **암호화·중복제거** 백업하고, **웹 대시보드**(설정·이력 조회 + 즉시 백업/복원)를 제공하는 self-contained Docker 스택. 다른 서버에 "이미지 + 설정"만 떨궈 빠르게 이식하기 위한 것.

- 백업 엔진: 검증된 bash 스크립트(restic 0.16.5) — DB 일관성 덤프 + 파일 백업
- 저장소: rclone `serve restic`(HTTP) 경유 Google Drive, restic `rest:` 백엔드로 접근
- 웹: Go 단일 바이너리(로그인 + 조회 + 백업/복원), 내장 cron 스케줄러
- 구성: 컨테이너 2개(`rclone`, `engine`), 전용 내부 네트워크

```
┌─ rclone   : rclone serve restic (Drive 토큰은 여기에만)
└─ engine   : restic + bash 엔진 + Go 웹/스케줄러 (docker.sock + /home:ro 마운트)
              └─ 웹 UI :8088  (로그인)
```

---

## 1. 구성 요소

| 경로 | 설명 |
|---|---|
| `docker-compose.yml` | 두 서비스 정의 |
| `.env` | 비시크릿 설정(포트·관리자ID·REMOTE_NAME·HOST_TAG). `.env.example` 복사 |
| `config/config.env` | 운영설정(스케줄·보존·대역폭·DB컨테이너명). **UI 편집 가능** |
| `config/excludes.txt` | 백업 제외 목록. UI 편집 가능 |
| `secrets/` | 시크릿(아래). **git 제외, ro 마운트, UI 비취급** |
| `rclone/rclone.conf` | Google Drive 토큰 |

### secrets/ 파일
| 파일 | 내용 |
|---|---|
| `repo-pass` | restic 저장소 비밀번호 (**분실 시 복구 불가**) |
| `rclone-rest.env` | `RCLONE_REST_USER` / `RCLONE_REST_PASS` (rclone↔restic 내부 인증) |
| `web-admin.hash` | 웹 관리자 비밀번호의 bcrypt 해시 |
| `db-creds.env` | DB 자격증명(`PG_USER`,`MONGO_USER/PASS`,`REDIS_PASS`) — DB 백업 시 |
| `discord-webhook` | 실패/성공 알림 webhook URL (선택) |

---

## 2. 새 서버 이식 (처음부터)

```bash
# 0) 사전: rclone 토큰 발급 (브라우저 있는 PC에서)
#    rclone authorize "drive"  → 출력 토큰을 rclone.conf 의 [REMOTE_NAME] 섹션에 기입

git clone <repo> backup-stack && cd backup-stack
cp .env.example .env
$EDITOR .env          # WEB_PORT / WEB_ADMIN_USER / REMOTE_NAME / HOST_TAG

mkdir -p secrets rclone config
cp <인증된 rclone.conf> rclone/rclone.conf

# 시크릿 생성
openssl rand -base64 48 > secrets/repo-pass            # 신규 저장소면 새로 생성, 기존 재사용이면 동일 값
printf 'RCLONE_REST_USER=restuser\nRCLONE_REST_PASS=%s\n' \
  "$(tr -dc 'A-Za-z0-9_-' </dev/urandom | head -c 32)" > secrets/rclone-rest.env
docker run --rm httpd:2.4-alpine htpasswd -nbBC 12 admin '원하는관리자비번' | cut -d: -f2 > secrets/web-admin.hash
# (DB 백업 시) secrets/db-creds.env 작성, (알림 시) secrets/discord-webhook 작성
chmod -R go-rwx secrets

# 연결 점검 → (신규 저장소만) 초기화 → 기동
docker compose run --rm engine preflight      # PREFLIGHT: PASS 확인
# 신규 저장소: .env 에서 ALLOW_REPO_INIT=true 후
#   docker compose run --rm engine init      ; 다시 false 로 되돌림
docker compose up -d --build

# 웹 접속 → 로그인 → "지금 백업" 으로 첫 백업 확인
```

> **제약**
> - `REMOTE_NAME` == `rclone.conf` 의 `[섹션]` 이름.
> - `HOST_TAG` == 저장소 하위 경로(`backups/<HOST_TAG>`). 기존 저장소 재사용 시 그 이름과 일치시킬 것.
> - `repo-pass` 는 **반드시 비밀번호 관리자에 별도 보관**. 분실 시 백업 복구 영구 불가.
> - `/state`, `backupstack_logs` 볼륨은 **서버별 고유** — 서버 간 복사 금지.
> - x86_64 서버는 `engine/Dockerfile` 의 `TARGETARCH` 기본값(arm64)을 amd64로 빌드(`docker buildx`/`--build-arg TARGETARCH=amd64`).

---

## 3. 웹 대시보드

- 주소: `http://<서버IP>:<WEB_PORT>` (기본 8088). 외부 노출 시 리버스 프록시+TLS 권장.
- 로그인 후: 상태 / 설정(편집·저장) / 스냅샷 목록 / 실행 이력 / "지금 백업".
- 운영설정(보존일·대역폭·스케줄·제외목록 등) 저장 시 스케줄러 자동 reload.
- 시크릿은 UI에서 다루지 않음(파일 전용).

### 위험 액션(복원)
복원은 API 기준 **세션 + CSRF + 관리자 비밀번호 재입력 + 확인문구("RESTORE")** 가 필요하며, 대상은 `/restore-out`(전용 볼륨)으로만 제한된다(경로 이탈·심볼릭링크 차단). 라이브 데이터를 직접 덮어쓰지 않으므로, 복원물을 검토 후 수동 반영한다.

---

## 4. 백업 동작

- 스케줄(`BACKUP_SCHEDULE`, TZ=Asia/Seoul) 또는 수동 트리거.
- DB: 컨테이너 상태를 `docker inspect`로 판정 — 실행 중이면 일관성 덤프(+라이브 디렉터리 동적 제외), 정지면 raw 백업, 실행 중 덤프 실패면 **런 실패**(라이브 파일 폴백 금지).
- `restic backup --json` 요약을 파싱해 추가 용량/스냅샷ID를 이력에 기록.
- 보존: `restic forget --keep-daily N --tag auto --prune`. 모든 스냅샷은 `auto` 태그(트리거 출처는 이력 DB에만).
- 주간 무결성 검증(`CHECK_SCHEDULE`): `restic check --read-data-subset`.
- 운영 무영향: `ionice -c3 nice -n19` + `--limit-upload`.

---

## 5. 복원 (CLI)

웹 UI 외에 컨테이너 내부 CLI로도 가능:
```bash
# 스냅샷 목록
docker compose exec engine restic snapshots

# 특정 경로를 /restore-out 으로
docker compose exec engine /opt/backup/scripts/home-restore.sh restore latest /restore-out /home/docker/gitea

# DB 덤프만 복원 후 수동 import
docker compose exec engine /opt/backup/scripts/home-restore.sh dbs latest /restore-out
#   postgres: gunzip -c <ts>/postgres/all.sql.gz | docker exec -i postgres psql -U <user>
#   mongodb : docker exec -i mongodb mongorestore -u <u> -p <p> --authenticationDatabase admin --gzip --archive < <ts>/mongodb/mongo.archive
#   redis   : redis 정지 → dump.rdb 복사 → 시작
```

### 새 서버 전체 복구
1. 위 "새 서버 이식" 으로 스택 구성(같은 `HOST_TAG`/저장소, 같은 `repo-pass`)
2. `restic restore latest --target /restore-out` 또는 호스트로 직접 복원
3. DB 덤프 import → `docker compose up -d`(원래 서비스 스택)

---

## 6. 이 서버: 호스트 cron → 컨테이너 전환 (단계적·가역적)

기존 호스트 백업(`/etc/cron.d/home-backup`)이 있는 서버에서 컨테이너로 옮길 때:
```bash
# A. 호스트 cron 비활성 (rename — 롤백 가능)
sudo mv /etc/cron.d/home-backup /etc/cron.d/home-backup.disabled && sudo systemctl restart cron

# B. 컨테이너는 수동 검증 (SCHEDULER_ENABLED=false 유지) — 백업/복원 2회 이상 성공 확인

# C. 컨테이너 스케줄러 ON
sed -i 's/^SCHEDULER_ENABLED=.*/SCHEDULER_ENABLED=true/' config/config.env
docker compose restart engine     # 또는 웹 UI에서 저장
#   /api/status 의 next_run 이 다음 03:00 KST 인지 확인

# 롤백: cron 파일 복원 + SCHEDULER_ENABLED=false (compose down 불필요)
```
> 전환 전까지는 **호스트 cron과 컨테이너 스케줄러를 동시에 켜지 말 것**(이중 실행·prune 충돌 방지).

---

## 7. 트러블슈팅

| 증상 | 조치 |
|---|---|
| rclone unhealthy | `docker compose logs rclone` — rclone.conf/REMOTE_NAME/토큰 확인. `docker compose run --rm engine preflight` |
| `repo unreachable` 로 백업 거부 | 네트워크/토큰 문제. 저장소가 실제 없을 때만 `ALLOW_REPO_INIT=true`로 init |
| 로그인 실패 | `secrets/web-admin.hash` 가 bcrypt 해시인지, `WEB_ADMIN_USER` 일치 확인 |
| `busy`(409) | 다른 백업/복원 진행 중 |
| config 저장 400 | cron 식/숫자 범위/컨테이너명 검증 실패 — 메시지 확인 |
| 비밀번호 변경 후 로그아웃됨 | 정상(해시 변경 시 기존 세션 무효화) |

## 8. 보안 메모
- engine은 `docker.sock` 보유 = 사실상 root. 웹은 LAN+로그인 전제, 위험 액션 재인증. 외부 노출 시 프록시+TLS.
- rclone REST는 전용 내부망 + user/pass + restic 암호화(이중). 내부 HTTP 평문은 격리망 한정으로 허용.
- 모든 시크릿은 ro 파일, git 제외, 로그 마스킹.
