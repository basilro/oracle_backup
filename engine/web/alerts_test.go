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
