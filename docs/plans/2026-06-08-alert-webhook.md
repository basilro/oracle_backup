# 알림 webhook URL 설정(UI) 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 백업 알림 webhook URL을 대시보드 설정에서 보고·수정·해제하고 테스트 전송한다.

**Architecture:** `/config/alert-webhook` 전용 파일에 저장(엔진은 `cat`으로 읽어 bash-source 깨짐 회피). Go에 신규 `alerts.go`로 GET/POST/test 핸들러를 두고, 설정 탭에 카드를 추가한다.

**Tech Stack:** Go(net/http, os, encoding/json), bash, vanilla JS/CSS.

**설계 문서:** `docs/specs/2026-06-08-alert-webhook-design.md`

---

## 파일 구조

| 파일 | 변경 |
|------|------|
| `engine/scripts/lib/common.sh` | `webhook_url()` 우선순위에 `/config/alert-webhook` 추가 |
| `engine/web/alerts.go` (신규) | `validWebhookURL`, 파일 read/write, 테스트 전송, 핸들러 3개 |
| `engine/web/alerts_test.go` (신규) | `validWebhookURL` 단위테스트 |
| `engine/web/api.go` | 라우트 2개 등록 |
| `engine/web/ui/index.html` | 설정 탭 "알림" 카드 |
| `engine/web/ui/app.js` | 로드/저장/테스트 + 부팅 호출 |

---

## Task 1: 백엔드 (엔진 + Go 핸들러 + 테스트)

**Files:**
- Modify: `engine/scripts/lib/common.sh:29`
- Create: `engine/web/alerts.go`
- Create: `engine/web/alerts_test.go`
- Modify: `engine/web/api.go`

- [ ] **Step 1: 엔진 webhook_url 우선순위 확장**

`engine/scripts/lib/common.sh`의:
```bash
webhook_url() { if [ -f /secrets/discord-webhook ]; then cat /secrets/discord-webhook; else echo "${BACKUP_ALERT_WEBHOOK:-}"; fi; }
```
을 아래로 교체:
```bash
webhook_url() {
    if   [ -s /secrets/discord-webhook ]; then cat /secrets/discord-webhook
    elif [ -s /config/alert-webhook   ]; then cat /config/alert-webhook
    else echo "${BACKUP_ALERT_WEBHOOK:-}"; fi
}
```

- [ ] **Step 2: alerts.go 생성 (검증 + 파일 + 전송 + 핸들러)**

`engine/web/alerts.go`:
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const alertWebhookFile = "/config/alert-webhook"

// validWebhookURL accepts only an http(s) URL with no spaces/control chars, ≤2048.
func validWebhookURL(u string) bool {
	if len(u) == 0 || len(u) > 2048 {
		return false
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	for _, r := range u {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return false
		}
	}
	return true
}

// readAlertWebhook returns the UI-managed webhook URL ("" if unset).
func readAlertWebhook() string {
	b, err := os.ReadFile(alertWebhookFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeAlertWebhook atomically persists the webhook URL.
func writeAlertWebhook(u string) error {
	tmp := alertWebhookFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(u+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, alertWebhookFile)
}

// sendWebhookTest posts a test notification (notify()-compatible payload).
func sendWebhookTest(ctx context.Context, url string) error {
	host := os.Getenv("HOST_TAG")
	if host == "" {
		host = "backup"
	}
	payload := map[string]string{
		"content": "[백업:테스트] 테스트 알림 (호스트: " + host + ")",
		"text":    "[백업:테스트] 테스트 알림",
	}
	b, _ := json.Marshal(payload)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// handleAlertWebhook: GET → {url}; POST {url} → save (empty = clear).
func (s *Server) handleAlertWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]string{"url": readAlertWebhook()})
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
	var body struct{ URL string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	u := strings.TrimSpace(body.URL)
	if u == "" {
		os.Remove(alertWebhookFile)
		s.store.Audit(user, "alert-webhook", "clear")
		s.writeJSON(w, 200, map[string]any{"configured": false})
		return
	}
	if !validWebhookURL(u) {
		http.Error(w, "http:// 또는 https:// 로 시작하는 올바른 URL이어야 합니다", 400)
		return
	}
	if err := writeAlertWebhook(u); err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "저장 실패"})
		return
	}
	s.store.Audit(user, "alert-webhook", "set")
	s.writeJSON(w, 200, map[string]any{"configured": true})
}

// handleAlertWebhookTest: POST {url} → send test, returns {ok,error}.
func (s *Server) handleAlertWebhookTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	user, _ := s.currentUser(r)
	var body struct{ URL string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	u := strings.TrimSpace(body.URL)
	if !validWebhookURL(u) {
		s.writeJSON(w, 200, map[string]any{"ok": false, "error": "URL 형식이 올바르지 않습니다"})
		return
	}
	if err := sendWebhookTest(r.Context(), u); err != nil {
		s.store.Audit(user, "alert-webhook-test", "fail")
		s.writeJSON(w, 200, map[string]any{"ok": false, "error": Redact(err.Error())})
		return
	}
	s.store.Audit(user, "alert-webhook-test", "ok")
	s.writeJSON(w, 200, map[string]any{"ok": true})
}
```

- [ ] **Step 3: alerts_test.go 생성 (검증 단위테스트)**

`engine/web/alerts_test.go`:
```go
package main

import (
	"strings"
	"testing"
)

func TestValidWebhookURL(t *testing.T) {
	ok := []string{
		"https://discord.com/api/webhooks/1/abc",
		"http://nas.local:8080/hook",
		"https://hooks.slack.com/services/T/B/x",
	}
	bad := []string{
		"",
		"ftp://x/y",
		"discord.com/webhook",
		"https://x.com/ has space",
		"https://x.com/\nline",
		"https://x.com/\tx",
	}
	for _, u := range ok {
		if !validWebhookURL(u) {
			t.Errorf("expected valid: %q", u)
		}
	}
	for _, u := range bad {
		if validWebhookURL(u) {
			t.Errorf("expected invalid: %q", u)
		}
	}
	if validWebhookURL("https://x.com/" + strings.Repeat("a", 2048)) {
		t.Error("over-length must be invalid")
	}
}
```

- [ ] **Step 4: 라우트 등록**

`engine/web/api.go`의:
```go
	mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
```
바로 아래에 추가:
```go
	mux.HandleFunc("/api/alert-webhook", s.requireAuth(s.handleAlertWebhook))
	mux.HandleFunc("/api/alert-webhook-test", s.requireAuth(s.handleAlertWebhookTest))
```

- [ ] **Step 5: 빌드 + 테스트**

Run:
```bash
docker run --rm -v /home/ubuntu/backup-stack/engine/web:/src -w /src \
  -v backupstack_gocache:/go -v backupstack_gobuild:/root/.cache/go-build \
  golang:1.22-alpine sh -c "go build ./... && go vet ./... && go test -run TestValidWebhookURL -v && go test ./... && echo ALL_OK"
rm -f /home/ubuntu/backup-stack/engine/web/backupengine
```
Expected: TestValidWebhookURL PASS, ALL_OK

- [ ] **Step 6: 커밋**

```bash
git add engine/scripts/lib/common.sh engine/web/alerts.go engine/web/alerts_test.go engine/web/api.go
git commit -m "feat(alert): webhook URL 설정 엔드포인트 + 엔진 우선순위(/config/alert-webhook)"
```

---

## Task 2: UI 알림 카드 + 검증·재기동

**Files:**
- Modify: `engine/web/ui/index.html`
- Modify: `engine/web/ui/app.js`

- [ ] **Step 1: 설정 탭에 "알림" 카드 추가**

`engine/web/ui/index.html`의:
```html
        <div class="row-actions"><button id="saveExcludes" class="btn-primary">제외 규칙 저장</button><span id="exMsg" class="msg"></span></div>
      </div>
    </section>
```
을 아래로 교체(알림 카드 추가):
```html
        <div class="row-actions"><button id="saveExcludes" class="btn-primary">제외 규칙 저장</button><span id="exMsg" class="msg"></span></div>
      </div>
      <div class="card">
        <h2>알림 (Discord/Slack webhook)</h2>
        <p class="dim" style="margin:0 0 12px;font-size:.86rem">백업 성공/실패 시 이 webhook으로 알림을 보냅니다. Discord·Slack 호환. <b>비우고 저장하면 알림이 꺼집니다.</b> <span class="dim">(<code>/secrets/discord-webhook</code> 파일이 있으면 그게 우선합니다.)</span></p>
        <div class="field"><div class="lab">Webhook URL</div><div class="ctl"><input id="alertUrl" type="text" placeholder="https://discord.com/api/webhooks/…" spellcheck="false" autocomplete="off" style="width:min(420px,70vw)"></div></div>
        <div class="row-actions"><button id="alertSave" class="btn-primary">저장</button><button id="alertTest" class="btn-ghost">테스트 전송</button><span id="alertMsg" class="msg"></span></div>
      </div>
    </section>
```

- [ ] **Step 2: app.js — 로드/저장/테스트 함수 + 와이어링 추가**

`engine/web/ui/app.js`의:
```javascript
$("#logout").onclick = logout;
```
바로 위에 추가:
```javascript
/* ---------- alert webhook ---------- */
async function loadAlertWebhook() {
  const el = $("#alertUrl"); if (!el) return;
  try { const d = await (await api("/api/alert-webhook")).json(); el.value = d.url || ""; } catch (e) {}
}
async function alertSave() {
  const m = $("#alertMsg"); m.className = "msg"; m.textContent = "저장 중…";
  try {
    const r = await api("/api/alert-webhook", { method: "POST", body: JSON.stringify({ URL: $("#alertUrl").value }) });
    if (!r.ok) { m.className = "msg fail"; m.textContent = "✕ " + (await r.text()); return; }
    const d = await r.json();
    m.className = "msg ok"; m.textContent = d.configured ? "✓ 저장됨" : "✓ 알림 해제됨";
  } catch (e) { if (String(e.message) !== "unauthorized") { m.className = "msg fail"; m.textContent = "✕ " + e.message; } }
}
async function alertTest() {
  const m = $("#alertMsg"); m.className = "msg"; m.textContent = "전송 중…";
  try {
    const d = await (await api("/api/alert-webhook-test", { method: "POST", body: JSON.stringify({ URL: $("#alertUrl").value }) })).json();
    if (d.ok) { m.className = "msg ok"; m.textContent = "✓ 테스트 알림 전송됨 — 채널을 확인하세요"; }
    else { m.className = "msg fail"; m.textContent = "✕ 전송 실패: " + (d.error || ""); }
  } catch (e) { if (String(e.message) !== "unauthorized") { m.className = "msg fail"; m.textContent = "✕ " + e.message; } }
}
$("#alertSave") && ($("#alertSave").onclick = alertSave);
$("#alertTest") && ($("#alertTest").onclick = alertTest);
```
(이 블록 다음 줄에 기존 `$("#logout").onclick = logout;`이 그대로 이어진다.)

- [ ] **Step 3: app.js — 부팅 시 로드**

`engine/web/ui/app.js`의 부팅 IIFE에서:
```javascript
  loadRemotes();
  loadRepoPath();
```
을 아래로 교체:
```javascript
  loadRemotes();
  loadRepoPath();
  loadAlertWebhook();
```

- [ ] **Step 4: 캐시버스트 스탬프 갱신**

`engine/web/ui/app.js`의 1번 줄을:
```javascript
const BUILD = "ui-2026-06-08c";
```
로 바꾸고:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && sed -i 's/v=20260608b/v=20260608c/g' index.html && echo "count=$(grep -c 20260608c index.html)"
```
Expected: count=5

- [ ] **Step 5: app.js 문법 검사**

Run: `cd /home/ubuntu/backup-stack/engine/web/ui && node --check app.js && echo OK`
Expected: OK

- [ ] **Step 6: 이미지 재빌드 + 기동 + 라우트 확인**

Run:
```bash
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 6 && \
  curl -fsS http://localhost:8088/healthz && echo " healthz" && \
  for p in alert-webhook alert-webhook-test; do echo "/api/$p -> $(curl -s -o /dev/null -w '%{http_code}' http://localhost:8088/api/$p)"; done
```
Expected: `ok healthz`, 두 라우트 모두 `401`

- [ ] **Step 7: 수동 검증 (브라우저)**

1. 설정 탭에 "알림" 카드 표시, 기존 값 채워짐(없으면 빈칸).
2. 잘못된 URL 저장 → 빨간 오류. 올바른 URL 저장 → "✓ 저장됨".
3. "테스트 전송" → 채널에 `[백업:테스트] 테스트 알림` 수신, UI "✓ 전송됨".
4. 비우고 저장 → "✓ 알림 해제됨", `/config/alert-webhook` 삭제.

- [ ] **Step 8: 커밋**

```bash
cd /home/ubuntu/backup-stack && git add engine/web/ui/index.html engine/web/ui/app.js
git commit -m "feat(ui): 설정 탭 알림 webhook 카드(저장·테스트) + 캐시버스트 ui-2026-06-08c"
```

---

## 검증 체크리스트 (spec 대비)

- [x] 전용 파일 `/config/alert-webhook` 저장 — `writeAlertWebhook`/`readAlertWebhook`
- [x] 엔진 우선순위 secrets→config→env — `webhook_url()`
- [x] GET/POST(set·clear)/test 엔드포인트 — alerts.go 핸들러 + 라우트
- [x] 평문 표시·편집 — UI 입력칸 + GET로 채움
- [x] 테스트 버튼(필드값으로 전송) — `handleAlertWebhookTest` + `alertTest`
- [x] 검증 http(s)·제어문자·길이 — `validWebhookURL` + 단위테스트
- [x] 비우면 해제 — POST 빈 값 → `os.Remove`
