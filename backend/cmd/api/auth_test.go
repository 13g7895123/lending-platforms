package main

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	const plain = "correct horse battery"

	hash, err := hashPassword(plain)
	if err != nil {
		t.Fatalf("hashPassword returned error: %v", err)
	}
	if hash == plain || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash %q does not look like a bcrypt digest", hash)
	}
	if !verifyPassword(hash, plain) {
		t.Error("verifyPassword rejected the correct password")
	}
	if verifyPassword(hash, plain+"x") {
		t.Error("verifyPassword accepted a wrong password")
	}
	if verifyPassword("not-a-hash", plain) {
		t.Error("verifyPassword accepted a malformed hash")
	}
}

// 同一密碼兩次雜湊必須產生不同結果（bcrypt 自帶 salt）。
func TestHashPasswordIsSalted(t *testing.T) {
	first, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	second, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical; salt is missing")
	}
}

func TestSessionTokenIsHashedBeforeStorage(t *testing.T) {
	token, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken: %v", err)
	}
	if len(token) < 32 {
		t.Errorf("token length %d is too short for 256 bits of entropy", len(token))
	}

	hashed := hashSessionToken(token)
	if hashed == token {
		t.Error("stored value equals the raw token; a DB leak would allow impersonation")
	}
	if len(hashed) != 64 {
		t.Errorf("hashSessionToken returned %d chars, want 64 (hex sha256)", len(hashed))
	}
	if hashSessionToken(token) != hashed {
		t.Error("hashSessionToken is not deterministic")
	}

	other, _ := newSessionToken()
	if token == other {
		t.Error("newSessionToken produced a duplicate token")
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"user@example.com", "user@example.com", false},
		{"  User@Example.COM  ", "user@example.com", false},
		{"", "", true},
		{"not-an-email", "", true},
		{"@example.com", "", true},
	}

	for _, test := range tests {
		got, err := validateEmail(test.input)
		if test.wantErr {
			if err == nil {
				t.Errorf("validateEmail(%q) = %q, want error", test.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateEmail(%q) returned error: %v", test.input, err)
			continue
		}
		if got != test.want {
			t.Errorf("validateEmail(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := validatePassword("12345678"); err != nil {
		t.Errorf("8-character password rejected: %v", err)
	}
	if err := validatePassword("1234567"); err == nil {
		t.Error("7-character password accepted, want rejection")
	}
	if err := validatePassword(strings.Repeat("a", 201)); err == nil {
		t.Error("201-character password accepted, want rejection")
	}
	// 多位元組字元應以字元數而非位元組數計算
	if err := validatePassword("密碼密碼密碼密碼"); err != nil {
		t.Errorf("8 multi-byte characters rejected: %v", err)
	}
}
