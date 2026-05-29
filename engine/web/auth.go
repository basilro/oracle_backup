package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = int64(12 * 3600)

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// signSession returns base64(user|issued|hmac).
func signSession(user string, issued int64, key []byte) string {
	msg := fmt.Sprintf("%s|%d", user, issued)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString([]byte(msg + "|" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))))
}

func verifySession(tok string, key []byte, now int64) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return "", false
	}
	user, issuedStr, sig := parts[0], parts[1], parts[2]
	msg := user + "|" + issuedStr
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
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
