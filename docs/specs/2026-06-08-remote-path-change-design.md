# 원격 저장소 경로 변경 + 데이터 마이그레이션 설계

> 작성일: 2026-06-08
> 상태: 승인됨 (구현 계획 대기)

## 목표

대시보드에서 restic 저장소의 **원격 경로**를 바꿀 수 있게 하고, 변경 시 기존 백업
데이터를 새 경로로 **이동**(복사 → 검증 → 전환 → 원본 삭제)한다. 경로 변경은 원격이
연결된 상태에서만 가능하며, 경로는 **모달 폴더 브라우저**로 선택한다.

## 결정 사항 (확정)

- **변경 범위**: 현재 원격(`REMOTE_NAME`) 내 경로만. 원격 자체는 바꾸지 않는다.
- **데이터 처리**: 이동 — `rclone copy` 후 `restic check` 검증, 활성 경로 전환, 그 다음
  원본 `rclone purge`로 삭제.
- **적용 방식**: 즉시 반영(무중단). Go 프로세스가 `RESTIC_REPOSITORY`를 `os.Setenv`로
  갱신해 이후 spawn되는 모든 restic 서브프로세스가 새 경로를 사용한다. 재시작 불필요.

## 현재 아키텍처 (배경)

- `engine/entrypoint.sh`가 컨테이너 시작 시
  `RESTIC_REPOSITORY="rclone:${REMOTE_NAME}:backups/${HOST_TAG}"`를 export하고
  `serve`(Go 웹앱)를 exec한다. 경로가 시작 시 한 번 고정된다.
- 웹앱과 모든 restic 작업(백업 스크립트 `home-backup.sh`, 복원, 스냅샷 수집기
  `collector.go`, `snapshot-ls`)은 이 env를 **상속**한다.
- 백업/복원은 `lowPriorityCmd`(ionice/nice 래퍼)로 스크립트를 서브프로세스 실행하며
  부모(Go) 프로세스의 env를 물려받는다. 따라서 Go에서 `os.Setenv`하면 이후 서브프로세스가
  즉시 새 값을 본다.
- 설정은 `/config/config.env`(쓰기 가능 볼륨)에 영속화. rclone 원격 목록은
  `rclone.conf` 파싱으로 노출(`handleRcloneRemotes`).

## 컴포넌트 설계

### 1) 저장소 경로 영속화

- 경로 부분만 분리해 `/config/remote-path` 파일에 저장한다. 없으면 기존 기본값 사용
  (하위호환).
- `entrypoint.sh`:
  ```bash
  REPO_PATH="backups/${HOST_TAG}"
  [ -s /config/remote-path ] && REPO_PATH="$(cat /config/remote-path)"
  export RESTIC_REPOSITORY="rclone:${REMOTE_NAME:?set REMOTE_NAME}:${REPO_PATH}"
  ```
- 런타임 전환은 Go가 `os.Setenv("RESTIC_REPOSITORY", "rclone:"+remote+":"+newPath)` +
  `/config/remote-path`에 기록. `config.env`는 건드리지 않는다(책임 분리).

### 2) 원격 연결 확인

- `GET /api/remote-path` → `{remote, path, connected}`.
- `connected` = `rclone lsd <remote>: --max-depth 1`(짧은 타임아웃) 성공 여부.
- UI: 연결 안 되면 "경로 변경" 버튼 비활성화 + 안내 문구.

### 3) 모달 경로 선택기 (원격 폴더 브라우저)

- 기존 복원 모달(`browseModal`) 패턴 재사용. 데이터 소스만 교체.
- `GET /api/remote-ls?path=<p>` → `rclone lsjson <remote>:<p> --dirs-only`로 폴더만 반환.
- 기능: 폴더 진입 / 상위로 / **새 폴더 이름 입력**(목적지 미존재 가능) / "이 위치로 지정".
- 경로 검증: 제어문자·`..`·셸 메타문자 차단(`safeResticPath` 동류 함수 재사용).

### 4) 마이그레이션 상태머신 (`POST /api/remote-migrate`)

재인증(복원과 동일한 비밀번호 재확인 게이트) 후 시작. 백업 게이트(`r.gate`)를 잡아
마이그레이션 중 백업·복원·스케줄러가 끼어들지 못하게 한다. 즉시 반환하고 모달이 상태를
폴링한다. 진행상태는 메모리 + `/state/migration-status.json`에 기록.

| 단계 | 동작 | 실패 시 |
|------|------|---------|
| `preflight` | 새 경로≠현재 경로 / 새 위치에 기존 저장소 없음 / 원격 연결 | 중단, 원본 그대로 |
| `copy` | `rclone copy <remote>:<old> <remote>:<new> --stats 3s` (진행 바이트 파싱) | 중단, **전환 안 함**, 새 위치 부분복사물 잔류(재시도 시 skip하며 이어감) |
| `verify` | 새 경로 대상 `restic --repo <new> cat config` + `restic --repo <new> snapshots`(원본과 스냅샷 개수 일치) | 중단, 원본 활성 유지 |
| `switch` | `/config/remote-path` 기록 + `os.Setenv` → 이 순간부터 새 경로 활성 | — |
| `cleanup` | 원본 삭제 `rclone purge <remote>:<old>` | 경고만, 전환은 이미 성공 |
| `done` | 완료 | — |

**핵심 안전 불변식**: copy·verify 통과 전에는 절대 경로 전환·원본 삭제를 하지 않는다.

**중요**: preflight·verify의 새 경로 조회는 활성 env(`RESTIC_REPOSITORY`)를 바꾸지 않고
`restic --repo rclone:<remote>:<new>` 명시 오버라이드로 수행한다. 활성 경로는 `switch`
단계에서만 바뀐다. 그래야 검증 도중 실패해도 백업/복원이 원본을 계속 정상 사용한다.

### 5) 엣지 케이스

- 기존 데이터 없음(한 번도 백업 안 함): copy/cleanup 건너뛰고 경로만 전환.
- 새 경로에 이미 저장소 존재: preflight 거부(덮어쓰기 방지).
- 새 경로 == 현재 경로: 거부(노옵).
- 마이그레이션 중 컨테이너 재시작: `/config/remote-path`는 switch 단계 전엔 안 바뀌므로
  재시작 시 원본 경로로 안전 복귀. 중단된 copy 잔재는 재시도로 정리.

## 데이터 흐름

```
[UI 경로변경 버튼] → GET /api/remote-path (connected?)
   └ connected → [모달] GET /api/remote-ls (폴더 탐색) → 경로 선택
        └ [재인증] → POST /api/remote-migrate {newPath}
             └ 상태머신(goroutine, gate 보유): preflight→copy→verify→switch→cleanup→done
        └ [진행 패널] 폴링 GET /api/remote-migrate (phase, bytes, error)
```

## 오류 처리

- 모든 단계 실패는 `migration-status.json`의 `phase=failed` + `error` 메시지로 표면화.
- switch 전 실패: 원본이 계속 활성 → 사용자 데이터 안전. 재시도 가능.
- cleanup 실패: 전환은 성공했으므로 경고만 표시(원본은 수동/재시도 삭제 가능).
- 경로 검증 실패: 400으로 즉시 거부.

## 테스트

- 단위: 경로 검증 함수(정상/`..`/셸 메타문자), preflight 거부 로직(동일 경로, 기존 저장소
  존재) — restic/rclone 호출은 인터페이스로 추상화해 가짜 구현으로 검증.
- 수동: 빈 저장소→경로 전환, 데이터 있는 저장소→이동 후 스냅샷 보존 확인, 연결 끊긴 원격에서
  버튼 비활성화 확인.

## 변경 파일

- `engine/entrypoint.sh` — `/config/remote-path` 읽기
- `engine/web/api.go` — 라우트 등록 + `/api/remote-path`, `/api/remote-ls` 핸들러
- `engine/web/migrate.go` (신규) — 마이그레이션 상태머신, `/api/remote-migrate`
- `engine/web/ui/index.html` — 경로 변경 버튼 + 원격 브라우저 모달 + 진행 패널
- `engine/web/ui/app.js` — 브라우저/폴링/진행 로직
- `engine/web/ui/style.css` — 진행 패널 스타일(모달은 기존 재사용)
- 테스트 파일: 경로 검증/preflight 단위 테스트

## 범위 밖 (YAGNI)

- 다른 원격으로의 전환(원격 자체 변경)은 이번 범위 아님.
- 마이그레이션 일시정지/재개 UI 없음(rclone copy 재실행으로 이어가기만).
- 동시 다중 마이그레이션 없음(게이트로 단일 실행 보장).
