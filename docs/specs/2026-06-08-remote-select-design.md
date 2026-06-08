# 원격(remote) 선택·전환 + 채택 전환 설계

> 작성일: 2026-06-08
> 상태: 승인됨 (구현 계획 대기)
> 선행: `2026-06-08-remote-path-change-design.md` (경로 변경 + 이동 마이그레이션)

## 목표

대시보드에서 활성 rclone **원격(remote)** 자체를 바꿀 수 있게 한다. 기존 "경로 변경"이
같은 원격 내 경로만 바꿨다면, 이번엔 다른 원격으로 전환한다. 전환 대상에 따라:
- 대상이 비어 있으면 **이동(move)**: 교차 원격 복사 → 검증 → 전환 → 원본 삭제
- 대상에 유효한 저장소가 있으면 **채택(adopt)**: 복사·삭제 없이 포인터만 전환(원본 유지)

## 결정 사항 (확정)

- **UI**: 원격 전용 별도 컨트롤(드롭다운 + "원격 전환…" 버튼). 경로 변경과 분리.
- **기존 저장소 대상**: "채택 전환" 옵션 추가(이동 없이 그 저장소로 전환).
- **원격 전환 시 경로**: 현재 경로를 그대로 따라감(원격만 바뀜). 경로까지 바꾸려면 기존
  "경로 변경" 흐름 사용.

## 배경 (현재 구조)

- `activeRemote()` = `os.Getenv("REMOTE_NAME")` (migrate.go). entrypoint가 `REMOTE_NAME`으로
  `RESTIC_REPOSITORY` 구성. `handleRcloneRemotes`가 `REMOTE_NAME`으로 활성 표시.
- 경로는 이미 `/config/remote-path`로 런타임 변경 가능(선행 기능).
- 마이그레이션 엔진은 단일 원격 내 `rclone copy`만 함 → 교차 원격으로 일반화 필요.

## 컴포넌트 설계

### 1) 원격 이름 영속화 (경로와 동일 패턴)

- `/config/remote-name` 파일 신설. 없으면 `REMOTE_NAME` env(기본값).
- `entrypoint.sh`:
  ```bash
  REMOTE="${REMOTE_NAME:?set REMOTE_NAME}"
  [ -s /config/remote-name ] && REMOTE="$(head -n1 /config/remote-name | tr -d '[:space:]')"
  REPO_PATH="backups/${HOST_TAG:?set HOST_TAG}"
  [ -s /config/remote-path ] && REPO_PATH="$(head -n1 /config/remote-path | tr -d '[:space:]')"
  export REMOTE_NAME="$REMOTE"                 # 엔진 os.Getenv·handleRcloneRemotes 반영
  export RESTIC_REPOSITORY="rclone:${REMOTE}:${REPO_PATH}"
  ```
- 런타임 전환: `os.Setenv("REMOTE_NAME", new)` + `os.Setenv("RESTIC_REPOSITORY", repoURLOn(new,path))`
  + `/config/remote-name`·`/config/remote-path` 기록(원자적 temp+rename).

### 2) 통합 마이그레이션 엔진 (모드 자동 감지)

`Start(appCtx, toRemote, toPath)` / `run(ctx, fromRemote, fromPath, toRemote, toPath)`.
`repoURLOn(remote, path)`로 임의 원격의 repo URL 구성.

| 단계 | move (대상 비어 있음) | adopt (대상에 저장소 있음) |
|------|----------------------|---------------------------|
| preflight | to≠from / 대상 원격 도달 / 대상 비어 있음 확인 | to≠from / 대상 원격 도달 / 대상에 유효 저장소 확인 |
| copy | `rclone copy from원격:from경로 to원격:to경로` | 건너뜀 |
| verify | 대상 `restic --repo cat config` + 스냅샷 개수 = 원본 | 건너뜀(preflight에서 이미 열림 확인) |
| switch | remote-name+remote-path 기록 + os.Setenv 2개 | 동일 |
| cleanup | 원본 `rclone purge from원격:from경로` | 건너뜀(원본 유지) |

모드는 preflight에서 `repoExists(toRepo)`로 결정. 원본이 비어 있고 대상도 비어 있으면
copy/cleanup 없이 switch만(빈 저장소 단순 전환).

### 3) 원격 이름 검증

전환 대상 원격은 **rclone.conf에 설정된 remote 목록**에 있어야 한다(임의 문자열·옵션류 차단).
목록은 기존 `handleRcloneRemotes`와 동일하게 rclone.conf 파싱으로 얻는다.

### 4) 정보 동의용 미리보기

`GET /api/remote-target?remote=<r>&path=<p>` → `{reachable, hasRepo}`. 확인 화면이 시작 전에
**"이동(원본 삭제)" vs "채택(원본 유지)"** 중 무엇이 일어날지 명시한다.

### 5) UI — 별도 원격 전환 컨트롤

"백업 저장 위치" 카드에 추가:
- 원격 드롭다운(`/api/rclone-remotes`로 채움; 현재 원격 선택 표시) + "원격 전환…" 버튼
- 클릭 → 대상=(선택 원격, **현재 경로**) 미리보기(`/api/remote-target`) → 모드·결과 표시 →
  재인증(`MIGRATE`+비번) → 진행 모달(기존 상태머신 폴링 재사용)

## API 변경

- `POST /api/remote-migrate` 바디: `{Remote, Path, Password, Confirm}`로 확장.
  `Remote` 비면 현재 원격(경로만 변경 = 선행 기능 그대로).
- `GET /api/remote-target?remote=&path=` 신설(미리보기).
- `MigrationStatus`에 `Mode`(move|adopt) 필드 추가 → 진행 표시에 사용.

## 오류 처리

- 대상 원격 미도달: preflight 거부, 원본 활성 유지.
- 미설정 원격 이름: 400 거부.
- adopt에서 대상 열기 실패: preflight에서 move로 떨어지지 않고, 대상이 손상된 저장소면
  거부(열리지도 비어 있지도 않은 모호 상태 → "대상 저장소를 열 수 없습니다").
- switch 전 모든 실패 → 원본 그대로(데이터 안전 불변식 유지).

## 테스트

- 단위: `validRemoteName`(설정목록 멤버십 가짜 주입), 모드 결정 함수
  `migrateMode(toReachable, toHasRepo) (mode, error)`.
- 수동: 빈 대상 원격으로 이동, 기존 저장소 있는 대상으로 채택, 미설정 원격 거부,
  전환 후 백업/복원이 새 원격 사용 확인.

## 변경 파일

- `engine/entrypoint.sh` — `/config/remote-name` 읽기 + `REMOTE_NAME` 재export
- `engine/web/migrate.go` — `repoURLOn`, `validRemoteName`, `migrateMode`, `Start`/`run` 확장,
  `handleRemoteTarget`, `handleRemoteMigrate` 바디 확장
- `engine/web/api.go` — `/api/remote-target` 라우트
- `engine/web/ui/index.html` — 원격 드롭다운 + 버튼, 84행 안내 갱신
- `engine/web/ui/app.js` — 원격 목록 채우기, 미리보기, 전환 흐름
- `engine/web/ui/style.css` — 필요 시 소폭
- 테스트: 검증·모드결정 단위테스트

## 범위 밖 (YAGNI)

- 원격+경로를 한 모달에서 동시 선택(별도 컨트롤로 분리하기로 결정).
- 마이그레이션 일시정지/재개.
- 동시 다중 마이그레이션(게이트로 단일 보장).
