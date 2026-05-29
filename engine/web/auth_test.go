package main

import "testing"

func TestSessionSignVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := signSession("admin", 1700000000, key)
	user, ok := verifySession(tok, key, 1700000000+10)
	if !ok || user != "admin" {
		t.Fatalf("verify failed user=%q ok=%v", user, ok)
	}
	if _, ok := verifySession(tok+"x", key, 1700000000+10); ok {
		t.Fatal("tampered should fail")
	}
	if _, ok := verifySession(tok, key, 1700000000+999999); ok {
		t.Fatal("expired should fail")
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
