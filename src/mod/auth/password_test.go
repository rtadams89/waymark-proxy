package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil || !IsBcryptHash(h) {
		t.Fatalf("HashPassword: %q %v", h, err)
	}
	if ok, up := PasswordMatches(h, "correct horse"); !ok || up {
		t.Errorf("bcrypt hash not accepted: %v %v", ok, up)
	}
	if ok, _ := PasswordMatches(h, "wrong"); ok {
		t.Error("wrong password accepted")
	}

	// Unsalted SHA-512 digests: accepted and flagged for upgrade
	legacy := legacyPasswordDigest("correct horse")
	if ok, up := PasswordMatches(legacy, "correct horse"); !ok || !up {
		t.Errorf("legacy hash: %v %v", ok, up)
	}
	if ok, _ := PasswordMatches(strings.ToUpper(legacy), "correct horse"); !ok {
		t.Error("legacy hash comparison should ignore hex case")
	}
	if ok, _ := PasswordMatches(legacy, "wrong"); ok {
		t.Error("wrong password accepted against legacy hash")
	}
	if ok, _ := PasswordMatches("", ""); ok {
		t.Error("empty stored hash accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", 73)); err == nil {
		t.Error("password over 72 bytes should be refused")
	}
}

func TestPasswordMatchesCached(t *testing.T) {
	h, _ := HashPassword("s3cret")
	for i := 0; i < 3; i++ {
		if !PasswordMatchesCached(h, "s3cret") {
			t.Fatal("cached check failed")
		}
	}
	if PasswordMatchesCached(h, "other") {
		t.Fatal("cache accepted a different password")
	}
	if !PasswordMatchesCached(legacyPasswordDigest("old"), "old") {
		t.Fatal("legacy hash not accepted by cached check")
	}
}
