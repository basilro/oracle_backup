package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.env")
	os.WriteFile(p, []byte("KEEP_DAILY=7\nUPLOAD_LIMIT_KBPS=5120\nBACKUP_SCHEDULE=0 3 * * *\nSCHEDULER_ENABLED=true\n"), 0644)
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeepDaily != 7 || c.UploadLimit != 5120 {
		t.Fatalf("%+v", c)
	}
	c.KeepDaily = 14
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	c2, _ := LoadConfig(p)
	if c2.KeepDaily != 14 {
		t.Fatal("save roundtrip")
	}
}

func TestConfigValidateCron(t *testing.T) {
	c := Config{KeepDaily: 7, UploadLimit: 1000, BackupSchedule: "not a cron"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid cron")
	}
	c.BackupSchedule = "0 3 * * *"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid cron rejected: %v", err)
	}
}

func TestConfigRejectInjection(t *testing.T) {
	c := Config{KeepDaily: 7, UploadLimit: 1000, BackupSchedule: "0 3 * * *", PgContainer: "postgres; rm -rf /"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected reject shell metachars")
	}
}
