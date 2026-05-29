package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// randPassword returns a random alphanumeric password (ambiguous chars omitted).
func randPassword(n int) string {
	const al = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	for i := range b {
		b[i] = al[int(b[i])%len(al)]
	}
	return string(b)
}

// pickAdminHash decides which bcrypt hash to use for the admin account.
//   - secretsHash: operator-provided hash file (/secrets), highest precedence
//   - stateHash:   previously persisted hash (/state)
//   - envPw:       plaintext password from WEB_ADMIN_PASSWORD(_FILE)
//
// Returns the hash to use, a hash to persist to /state ("" = nothing), and a
// generated plaintext to surface once ("" = none). Persisting the derived hash
// keeps the session version (and thus existing logins) stable across restarts.
func pickAdminHash(secretsHash, stateHash, envPw string) (use, persist, generated string, err error) {
	if secretsHash != "" {
		return secretsHash, "", "", nil
	}
	if envPw != "" {
		if stateHash != "" && checkPassword(stateHash, envPw) {
			return stateHash, "", "", nil // unchanged → reuse persisted hash
		}
		h, e := hashPassword(envPw)
		return h, h, "", e
	}
	if stateHash != "" {
		return stateHash, "", "", nil
	}
	gen := randPassword(16)
	h, e := hashPassword(gen)
	return h, h, gen, e
}

const sessionTTL = int64(12 * 3600)

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// sessionVersion derives a short token-version tag from the admin password hash,
// so any password change (new hash) invalidates all outstanding sessions.
func sessionVersion(adminHash string) string {
	sum := sha256.Sum256([]byte("sessionver|" + adminHash))
	return hex.EncodeToString(sum[:4])
}

// signSession returns base64(user|issued|ver|hmac).
func signSession(user string, issued int64, ver string, key []byte) string {
	msg := fmt.Sprintf("%s|%d|%s", user, issued, ver)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString([]byte(msg + "|" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))))
}

// verifySession checks signature, expiry, and that the embedded version matches wantVer.
func verifySession(tok string, key []byte, now int64, wantVer string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return "", false
	}
	user, issuedStr, ver, sig := parts[0], parts[1], parts[2], parts[3]
	msg := user + "|" + issuedStr + "|" + ver
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}
	if wantVer != "" && subtle.ConstantTimeCompare([]byte(ver), []byte(wantVer)) != 1 {
		return "", false
	}
	issued, err := strconv.ParseInt(issuedStr, 10, 64)
	if err != nil {
		return "", false
	}
	if now-issued > sessionTTL || now < issued {
		return "", false
	}
	return user, true
}
