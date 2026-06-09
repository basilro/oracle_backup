# backup-stack

`/home` + Docker named volume을 **암호화·중복제거** 백업하고, **웹 대시보드**(설정·이력 조회 + 즉시 백업/복원)를 제공하는 self-contained Docker 스택. 다른 서버에 "이미지 + 설정"만 떨궈 빠르게 이식하기 위한 것.

- 백업 엔진: 검증된 bash 스크립트(restic 0.16.5) — DB 일관성 덤프 + 파일 백업
- 저장소: rclone(stdio) 경유 Google Drive — restic이 rclone을 자식 프로세스로 직접 실행(`rclone:` 백엔드)
- 웹: Go 단일 바이너리(로그인 + 조회 + 백업/복원), 내장 cron 스케줄러
- 구성: **단일 컨테이너** `engine` (restic + rclone + bash 엔진 + Go 웹/스케줄러)

```
engine 컨테이너 (docker.sock + /home:ro 마운트, 웹 UI :8088)
  restic --(stdio)--> rclone --> Google Drive
  rclone.conf(ro 마운트)에만 Drive 토큰 존재
```

> **이 README는 설치·초기 세팅 가이드입니다.** 대시보드 사용법(백업/복원/설정/목적지/원격 경로·전환/DB 작업/알림 등)은 설치 후 웹 대시보드의 **"매뉴얼" 탭**에 들어 있습니다.

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
| `web-admin.hash` | 웹 관리자 비밀번호의 bcrypt 해시 |
| `db-creds.env` | DB 자격증명(`PG_USER`,`MONGO_USER/PASS`,`REDIS_PASS`,`MYSQL_USER/MYSQL_PASS`) — DB 백업 시 |
| `discord-webhook` | 실패/성공 알림 webhook URL (선택) |

> `rclone/rclone.conf`(Drive 토큰)는 `secrets/`가 아니라 별도로 두고 ro 마운트됩니다.

---

## 2. 새 서버 이식 (처음부터)

```bash
git clone https://github.com/basilro/oracle_backup.git backup-stack && cd backup-stack
cp .env.example .env
mkdir -p secrets rclone config

# 1) 이미지 준비 (rclone 내장)
#    (A) Docker Hub에서 받기 — arm64/amd64 멀티아치, 빌드 불필요 (권장)
docker compose pull
#    (B) 또는 직접 빌드
docker compose build

# 2) rclone 목적지 설정 — 이 스택의 내장 rclone 으로 바로 (별도 설치 불필요)
#    rclone.conf 가 없는 새 서버라면 여기서 만든다. 만든 remote 이름을 기억.
docker run --rm -it -v "$PWD/rclone:/etc/rclone" bcjang/cloud_backup:v1.2.1 rclone-config
#    (이미 다른 곳의 rclone.conf 가 있으면: cp <인증된 rclone.conf> rclone/rclone.conf 로 대체 가능)
#    Google Drive 등 OAuth 는 브라우저 있는 PC에서 `rclone authorize "drive"` 토큰을 붙여넣는다 (§3)

# 3) .env 편집: WEB_PORT / WEB_ADMIN_USER / WEB_ADMIN_PASSWORD / REMOTE_NAME(=만든 remote 이름) / HOST_TAG
$EDITOR .env

# 4) 시크릿
openssl rand -base64 48 > secrets/repo-pass            # 신규 저장소면 새로 생성, 기존 재사용이면 동일 값
# (DB 백업 시) secrets/db-creds.env, (알림 시) secrets/discord-webhook 작성
chmod -R go-rwx secrets

# 5) 연결 점검 → (신규 저장소만) 초기화 → 기동
docker run --rm -v "$PWD/rclone:/etc/rclone" bcjang/cloud_backup:v1.2.1 rclone lsd <REMOTE_NAME>:   # Drive 접근 확인
# 신규 저장소: .env 에서 ALLOW_REPO_INIT=true 후
#   docker compose run --rm engine init      ; 다시 false 로 되돌림
docker compose up -d

# 6) 웹 접속(http://<서버IP>:<WEB_PORT>, 기본 8088) → 로그인 → "지금 백업" 으로 첫 백업 확인
#    이후 사용법(설정/목적지/복원 등)은 대시보드 "매뉴얼" 탭 참고
```

> **제약**
> - `REMOTE_NAME` == `rclone.conf` 의 `[섹션]` 이름.
> - `HOST_TAG` == 저장소 하위 경로(`backups/<HOST_TAG>`). 기존 저장소 재사용 시 그 이름과 일치시킬 것.
> - `repo-pass` 는 **반드시 비밀번호 관리자에 별도 보관**. 분실 시 백업 복구 영구 불가.
> - `/state`, `backupstack_logs` 볼륨은 **서버별 고유** — 서버 간 복사 금지.
> - 공개 이미지 `bcjang/cloud_backup` 는 **arm64·amd64 멀티아치**라 `docker compose pull` 이 양쪽에서 그대로 동작. 직접 빌드해도 `docker compose build` 가 호스트 아키텍처를 자동 감지.

---

## 3. 목적지(rclone remote) 연결 설정 — Google Drive / WebDAV / Synology / FTP

백업 **목적지**는 `rclone/rclone.conf` 의 한 `[섹션]`(=remote)으로 정의하고, `.env` 의 `REMOTE_NAME` 으로 어느 remote를 쓸지 고른다. 자격증명(Drive 토큰·WebDAV 비번 등)은 이 파일에만 두며 웹 UI에서는 다루지 않는다.

### 방법 A — 내장 rclone 설정 마법사 (권장, 모든 백엔드, 별도 설치 불필요)
이 스택 이미지에 들어있는 rclone 으로 대화식 설정. `./rclone/rclone.conf` 에 바로 기록된다.
(이미지가 필요하므로 먼저 `docker compose build`.)
```bash
docker run --rm -it -v "$PWD/rclone:/etc/rclone" bcjang/cloud_backup:v1.2.1 rclone-config
#  n) New remote → 이름 입력(이게 REMOTE_NAME) → 백엔드 선택 → 안내대로 진행
#  Google Drive 등 OAuth: 브라우저가 없으면 rclone이 다른 PC에서 실행할
#  `rclone authorize "drive"` 명령을 출력 → 그 PC에서 실행 후 토큰을 붙여넣기
#  설정 확인: docker run --rm -v "$PWD/rclone:/etc/rclone" bcjang/cloud_backup:v1.2.1 rclone listremotes
# (공식 rclone 이미지를 선호하면: docker run --rm -it -v "$PWD/rclone:/config/rclone" rclone/rclone:1.66 config)
```

### 방법 B — 비대화식 한 줄 생성 (WebDAV/Synology/FTP/SFTP 등 비-OAuth)
비밀번호는 rclone이 자동 obscure 한다.
```bash
R="docker run --rm -v $PWD/rclone:/etc/rclone bcjang/cloud_backup:v1.2.1 rclone config create"

# WebDAV (Nextcloud 등)
$R mydav webdav url=https://dav.example.com/remote.php/dav vendor=nextcloud user=USER pass=PASS

# Synology — DSM의 WebDAV Server 패키지 설치/활성(보통 https 5006) 후 WebDAV로 연결
$R mynas webdav url=https://nas.example.com:5006 vendor=other user=USER pass=PASS
#  또는 Synology SFTP(제어판 SFTP 활성, 보통 22):
$R mynas sftp host=nas.example.com user=USER pass=PASS port=22

# FTP / SFTP (일반)
$R myftp  ftp  host=ftp.example.com user=USER pass=PASS
$R mysftp sftp host=ssh.example.com user=USER pass=PASS port=22
```

### 방법 C — 직접 편집
`rclone/rclone.conf` 에 섹션을 손으로 추가(비밀번호는 `rclone obscure '평문'` 결과를 넣음).

### 설정 후
```bash
# 1) 연결 확인 (remote 이름으로 최상위 목록이 보이면 성공)
docker run --rm -v "$PWD/rclone:/etc/rclone" bcjang/cloud_backup:v1.2.1 rclone lsd <REMOTE_NAME>:
# 2) .env 의 REMOTE_NAME 을 그 섹션 이름으로 설정
# 3) 새 저장소면: ALLOW_REPO_INIT=true → docker compose run --rm engine init → 다시 false
# 4) docker compose up -d   (엔진은 rclone.conf 를 ro 로 읽어 restic 백엔드로 사용)
```

> 참고: 실행 중 엔진은 `rclone.conf` 를 **읽기 전용**으로 마운트한다(보안). 설정 변경은 위 `docker run ... config` 처럼 별도 명령으로 `./rclone/rclone.conf` 를 고친 뒤 `docker compose restart engine`.
> 백엔드 종류와 무관하게 저장소 경로는 `<REMOTE_NAME>:backups/<HOST_TAG>` 이며, restic 암호화는 백엔드와 독립이다(어디에 두든 내용은 암호화됨).
