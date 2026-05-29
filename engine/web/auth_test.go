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

func TestPickAdminHash(t *testing.T) {
	// 1. operator-provided secrets hash wins
	if u, p, g, _ := pickAdminHash("SECRETHASH", "x", "y"); u != "SECRETHASH" || p != "" || g != "" {
		t.Fatalf("secrets hash should win, got u=%q p=%q g=%q", u, p, g)
	}
	// 2. env password, no prior state → derive + persist
	u, p, _, err := pickAdminHash("", "", "mypw")
	if err != nil || u == "" || p != u || !checkPassword(u, "mypw") {
		t.Fatalf("env derive failed: err=%v u=%q p=%q", err, u, p)
	}
	// 3. env password unchanged vs persisted state → reuse (stable hash)
	sh, _ := hashPassword("mypw")
	if u2, p2, _, _ := pickAdminHash("", sh, "mypw"); u2 != sh || p2 != "" {
		t.Fatalf("should reuse matching state hash: u=%q p=%q", u2, p2)
	}
	// 4. env password changed → re-derive + persist
	u3, p3, _, _ := pickAdminHash("", sh, "changed")
	if p3 == "" || u3 == sh || !checkPassword(u3, "changed") {
		t.Fatal("should re-derive when env password changes")
	}
	// 5. nothing provided → generate + persist + surface plaintext
	u4, p4, g4, _ := pickAdminHash("", "", "")
	if g4 == "" || p4 == "" || !checkPassword(u4, g4) {
		t.Fatal("should generate a password when nothing is configured")
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
