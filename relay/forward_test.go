package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPasswordMatch(t *testing.T) {
	pw := "秘密密码"
	sum := sha256.Sum256([]byte(pw))
	stored := hex.EncodeToString(sum[:])

	// The raw password must match its stored hash (visitor auth page, and
	// legacy ASCII device handshakes).
	if !passwordMatch(stored, pw) {
		t.Fatal("raw password should match its stored hash")
	}
	// A SHA-256 hex digest must match directly (ASCII-safe device handshake
	// used for non-ASCII passwords).
	if !passwordMatch(stored, stored) {
		t.Fatal("hex digest should match the stored hash")
	}
	if passwordMatch(stored, "wrong") {
		t.Fatal("wrong password must not match")
	}
	if passwordMatch("not-hex", pw) {
		t.Fatal("non-hex stored hash must not match")
	}

	asciiPw := "secret123"
	asciiSum := sha256.Sum256([]byte(asciiPw))
	asciiHex := hex.EncodeToString(asciiSum[:])
	if !passwordMatch(asciiHex, asciiPw) {
		t.Fatal("ascii raw password should match")
	}
	if !passwordMatch(asciiHex, asciiHex) {
		t.Fatal("ascii hex digest should match")
	}
}
