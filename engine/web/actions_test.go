package main

import "testing"

func TestRestoreTargetSafe(t *testing.T) {
	root := "/restore-out"
	ok := []string{"/restore-out", "/restore-out/sub"}
	bad := []string{"/restore-out/../etc", "/etc/passwd", "/restore-out/../../root", "/state", "relative/x"}
	for _, p := range ok {
		if _, err := safeRestoreTarget(root, p); err != nil {
			t.Fatalf("should allow %s: %v", p, err)
		}
	}
	for _, p := range bad {
		if _, err := safeRestoreTarget(root, p); err == nil {
			t.Fatalf("should reject %s", p)
		}
	}
}

func TestSnapshotIDValid(t *testing.T) {
	if !validSnapID("cb8e5520c33cb1824e") {
		t.Fatal("hex should pass")
	}
	if validSnapID("latest; rm -rf /") {
		t.Fatal("metachar should fail")
	}
	if !validSnapID("latest") {
		t.Fatal("latest keyword allowed")
	}
}

func TestSafeResticPath(t *testing.T) {
	if !safeResticPath("/home/docker/memos") {
		t.Fatal("clean path should pass")
	}
	if safeResticPath("/home; rm -rf /") {
		t.Fatal("metachar should fail")
	}
}
