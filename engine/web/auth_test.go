package main

import "testing"

func TestSessionSignVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := signSession("admin", 1700000000, "v1", key)
	user, ok := verifySession(tok, key, 1700000000+10, "v1")
	if !ok || user != "admin" {
		t.Fatalf("verify failed user=%q ok=%v", user, ok)
	}
	if _, ok := verifySession(tok+"x", key, 1700000000+10, "v1"); ok {
		t.Fatal("tampered should fail")
	}
	if _, ok := verifySession(tok, key, 1700000000+999999, "v1"); ok {
		t.Fatal("expired should fail")
	}
	if _, ok := verifySession(tok, key, 1700000000+10, "v2"); ok {
		t.Fatal("version mismatch (password change) should fail")
	}
}

func TestSessionVersionChangesWithHash(t *testing.T) {
	if sessionVersion("hashA") == sessionVersion("hashB") {
		t.Fatal("different admin hashes must yield different session versions")
	}
}

func TestPasswordHash(t *testing.T) {
	h, _ := hashPassword("s3cret")
	if !checkPassword(h, "s3cret") {
		t.Fatal("should match")
	}
	if checkPassword(h, "wrong") {
		t.Fatal("should not match")
	}
}
