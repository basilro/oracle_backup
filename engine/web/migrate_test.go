package main

import (
	"os"
	"testing"
)

func TestValidRemotePath(t *testing.T) {
	ok := []string{"backups/host", "a", "내백업/server1", "x/y/z"}
	bad := []string{"", "/abs", "-flag", "a/../b", "a:b", "a;b", "a|b", "a$b"}
	for _, p := range ok {
		if !validRemotePath(p) {
			t.Errorf("expected valid: %q", p)
		}
	}
	for _, p := range bad {
		if validRemotePath(p) {
			t.Errorf("expected invalid: %q", p)
		}
	}
}

func TestRepoURL(t *testing.T) {
	os.Setenv("REMOTE_NAME", "gdrive")
	if got := repoURL("backups/h"); got != "rclone:gdrive:backups/h" {
		t.Errorf("repoURL = %q", got)
	}
}

func TestCurrentRepoPath(t *testing.T) {
	os.Setenv("HOST_TAG", "myhost")
	file := remotePathFileTest(t)
	// no override file → default
	os.Remove(file)
	if got := currentRepoPathFrom(file); got != "backups/myhost" {
		t.Errorf("default path = %q", got)
	}
	// with override
	os.WriteFile(file, []byte("custom/path\n"), 0644)
	if got := currentRepoPathFrom(file); got != "custom/path" {
		t.Errorf("override path = %q", got)
	}
}

func remotePathFileTest(t *testing.T) string {
	return t.TempDir() + "/remote-path"
}
