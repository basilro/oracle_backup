# 알림 webhook URL 설정 (UI) 설계

> 작성일: 2026-06-08
> 상태: 승인됨 (구현 계획 대기)

## 목표

백업 성공/실패 알림을 보내는 Discord/Slack 호환 webhook URL을 대시보드 설정에서
직접 보고·수정·해제하고, 저장된 값으로 테스트 알림을 보낼 수 있게 한다.

## 결정 사항 (확정)

- **표시 방식**: 평문 표시·편집. (단일 운영자 도구이고 다수 사용자 서비스가 아님)
- **테스트 버튼**: 포함. 필드에 입력된 값으로 즉시 테스트 전송(저장 전/후 모두 확인 가능).
- **저장 위치**: 전용 파일 `/config/alert-webhook`. config.env는 bash `source` 대상이라
  URL의 `&`·`?`에서 깨질 수 있어 회피. `/secrets`는 읽기 전용이라 UI가 못 씀.

## 배경 (현재 구조)

- `webhook_url()`(common.sh): `/secrets/discord-webhook`(ro) → `BACKUP_ALERT_WEBHOOK` env.
- `notify()`가 그 URL로 `{"content":..., "text":...}` POST(Discord+Slack 호환).
- `/config`는 쓰기 가능 마운트, `/secrets`는 읽기 전용. Go에는 webhook 처리 없음.

## 컴포넌트 설계

### 1) 엔진 — webhook_url 우선순위 확장

`engine/scripts/lib/common.sh`:
```bash
webhook_url() {
    if   [ -s /secrets/discord-webhook ]; then cat /secrets/discord-webhook
    elif [ -s /config/alert-webhook   ]; then cat /config/alert-webhook
    else echo "${BACKUP_ALERT_WEBHOOK:-}"; fi
}
```
우선순위: `/secrets/discord-webhook`(명시) → `/config/alert-webhook`(UI 관리) → env.
`-f`→`-s`로 바꿔 빈 파일은 건너뛴다.

### 2) Go — 신규 `engine/web/alerts.go`

- `GET /api/alert-webhook` → `{url}` (현재 `/config/alert-webhook` 내용, 없으면 빈 문자열).
- `POST /api/alert-webhook` (CSRF): 바디 `{url}`.
  - 빈 값 → 파일 삭제(알림 해제).
  - 값 있음 → `validWebhookURL` 검증 후 원자적 기록(temp+rename).
  - 감사로그 `alert-webhook set|clear`.
- `POST /api/alert-webhook-test` (CSRF): 바디 `{url}` → 해당 URL로 테스트 알림 전송 →
  `{ok, error}`. 검증 실패/전송 실패는 `ok=false`+사유.
- `validWebhookURL(u)`: `http://`/`https://` 시작, 제어문자·공백·줄바꿈 없음, 길이 ≤ 2048.
- 테스트 페이로드는 `notify()`와 동일 형식:
  `{"content":"[백업:테스트] 테스트 알림 (호스트: <HOST_TAG>)","text":"[백업:테스트] 테스트 알림"}`.
  10초 타임아웃, 2xx면 성공.

### 3) UI — 설정 탭 "알림" 카드

- URL 입력칸(현재 값 채움) + **저장** + **테스트 전송** 버튼 + 상태 메시지.
- 비우고 저장 = 해제. 안내: Discord/Slack 호환, 비우면 알림 끔.
- 저장/테스트는 `api()` + CSRF 사용. 부팅 시 현재 값 로드.

## 데이터 흐름

```
[설정 탭] GET /api/alert-webhook → 입력칸 채움
  ├ 저장 → POST /api/alert-webhook {url} → /config/alert-webhook 기록/삭제
  └ 테스트 → POST /api/alert-webhook-test {url} → 즉시 전송 → 성공/실패 표시
백업 실행 시 notify() → webhook_url()(이제 /config/alert-webhook 포함) → POST
```

## 오류 처리

- 검증 실패: 400 + 사유("http/https로 시작해야 합니다" 등).
- 테스트 전송 실패(네트워크/4xx/5xx): `{ok:false, error}` 200 응답으로 UI에 표시.
- 파일 쓰기 실패: 500.
- URL은 자격증명 성격이지만 사용자 선택에 따라 평문 표시(로그·감사에는 URL 미기록, "set"/"clear"만).

## 테스트

- 단위: `validWebhookURL` — 정상(https/http), 거부(빈문자열·ftp·공백·줄바꿈·제어문자·초과길이).
- 수동: 저장 후 테스트 버튼으로 채널 수신 확인, 비우고 저장 시 알림 꺼짐 확인, 실제 백업
  성공/실패 알림 수신 확인.

## 변경 파일

- `engine/scripts/lib/common.sh` — webhook_url 우선순위
- `engine/web/alerts.go` (신규) — 핸들러 3개 + 검증 + 테스트 전송
- `engine/web/api.go` — 라우트 2개(`/api/alert-webhook`, `/api/alert-webhook-test`)
- `engine/web/alerts_test.go` (신규) — validWebhookURL 단위테스트
- `engine/web/ui/index.html` — 설정 탭 알림 카드
- `engine/web/ui/app.js` — 로드/저장/테스트 로직

## 범위 밖 (YAGNI)

- 다중 webhook·채널별 분기.
- 알림 종류별 on/off(성공만/실패만 등).
- webhook 외 알림 수단(이메일 등).
