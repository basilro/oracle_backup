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
