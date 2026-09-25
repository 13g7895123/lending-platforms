package main

import (
	"strings"
	"testing"
)

func testCipher(t *testing.T) *piiCipher {
	t.Helper()
	key, err := newPIIKey()
	if err != nil {
		t.Fatalf("newPIIKey: %v", err)
	}
	cipher, err := newPIICipher(key)
	if err != nil {
		t.Fatalf("newPIICipher: %v", err)
	}
	return cipher
}

func TestPIIEncryptDecryptRoundTrip(t *testing.T) {
	cipher := testCipher(t)

	for _, plain := range []string{
		"A123456789",
		"0912-345-678",
		"陳建宏",                        // 多位元組
		strings.Repeat("x", 500),     // 長字串
		"!@#$%^&*()_+{}[]|\\:;\"'<>", // 特殊字元
	} {
		encrypted, err := cipher.encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt(%q): %v", plain, err)
		}
		if string(encrypted) == plain {
			t.Errorf("ciphertext equals plaintext for %q", plain)
		}
		decrypted, err := cipher.decrypt(encrypted)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if decrypted != plain {
			t.Errorf("round trip got %q, want %q", decrypted, plain)
		}
	}
}

func TestPIIEncryptEmptyReturnsNil(t *testing.T) {
	cipher := testCipher(t)
	encrypted, err := cipher.encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if encrypted != nil {
		t.Errorf("encrypt(\"\") = %v, want nil", encrypted)
	}
	decrypted, err := cipher.decrypt(nil)
	if err != nil || decrypted != "" {
		t.Errorf("decrypt(nil) = (%q, %v), want (\"\", nil)", decrypted, err)
	}
}

// 同一明文每次加密都必須產生不同密文（隨機 nonce），
// 否則可用密文比對推測兩筆申請是否為同一人。
func TestPIIEncryptUsesFreshNonce(t *testing.T) {
	cipher := testCipher(t)
	first, _ := cipher.encrypt("A123456789")
	second, _ := cipher.encrypt("A123456789")
	if string(first) == string(second) {
		t.Error("encrypting the same value twice produced identical ciphertext; nonce is not random")
	}
}

// GCM 具完整性保護：被篡改的密文必須解密失敗，而非回傳錯誤明文。
func TestPIIDecryptRejectsTamperedCiphertext(t *testing.T) {
	cipher := testCipher(t)
	encrypted, _ := cipher.encrypt("A123456789")

	tampered := make([]byte, len(encrypted))
	copy(tampered, encrypted)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := cipher.decrypt(tampered); err == nil {
		t.Error("tampered ciphertext decrypted successfully; integrity is not protected")
	}

	if _, err := cipher.decrypt([]byte("too-short")); err == nil {
		t.Error("truncated payload decrypted successfully")
	}
}

// 不同金鑰不可互相解密。
func TestPIIDecryptFailsWithDifferentKey(t *testing.T) {
	first := testCipher(t)
	second := testCipher(t)
	encrypted, _ := first.encrypt("A123456789")
	if _, err := second.decrypt(encrypted); err == nil {
		t.Error("ciphertext decrypted with a different key")
	}
}

func TestNewPIICipherRejectsBadKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"not base64", "!!!not-base64!!!"},
		{"too short", "c2hvcnQ="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newPIICipher(test.key); err == nil {
				t.Errorf("newPIICipher(%q) succeeded, want error", test.key)
			}
		})
	}
}

func TestNewPIIKeyIsUniqueAndCorrectSize(t *testing.T) {
	first, err := newPIIKey()
	if err != nil {
		t.Fatalf("newPIIKey: %v", err)
	}
	second, _ := newPIIKey()
	if first == second {
		t.Error("newPIIKey produced a duplicate key")
	}
	if _, err := newPIICipher(first); err != nil {
		t.Errorf("generated key rejected by newPIICipher: %v", err)
	}
}

func TestMaskIDNumber(t *testing.T) {
	tests := map[string]string{
		"A123456789": "A12****789",
		"":           "",
		"  ":         "",
		"A12345":     "******", // 長度不足全遮
		"AB1234567":  "AB1****567",
	}
	for input, want := range tests {
		if got := maskIDNumber(input); got != want {
			t.Errorf("maskIDNumber(%q) = %q, want %q", input, got, want)
		}
	}
}

// 遮罩後不得殘留足以識別個人的片段。
func TestMaskIDNumberHidesMiddle(t *testing.T) {
	masked := maskIDNumber("A123456789")
	if strings.Contains(masked, "3456") {
		t.Errorf("masked value %q still exposes the middle digits", masked)
	}
}

func TestMaskPhone(t *testing.T) {
	tests := map[string]string{
		"0912-345-678": "****678",
		"0912345678":   "****678",
		"":             "",
		"12":           "**",
		"abc":          "",
	}
	for input, want := range tests {
		if got := maskPhone(input); got != want {
			t.Errorf("maskPhone(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidIDNumberLength(t *testing.T) {
	if !validIDNumberLength("A123456789") {
		t.Error("normal ID number rejected")
	}
	if validIDNumberLength("") || validIDNumberLength("   ") {
		t.Error("empty ID number accepted")
	}
	if validIDNumberLength(strings.Repeat("x", 65)) {
		t.Error("over-long ID number accepted")
	}
}

// maskDecrypted 不可因解密失敗而讓整份清單失敗。
func TestMaskDecryptedHandlesFailure(t *testing.T) {
	cipher := testCipher(t)
	if got := maskDecrypted(cipher, nil, maskIDNumber); got != "" {
		t.Errorf("empty payload = %q, want empty string", got)
	}
	if got := maskDecrypted(cipher, []byte("garbage-data-here"), maskIDNumber); got != "(無法讀取)" {
		t.Errorf("undecryptable payload = %q, want (無法讀取)", got)
	}
	encrypted, _ := cipher.encrypt("A123456789")
	if got := maskDecrypted(cipher, encrypted, maskIDNumber); got != "A12****789" {
		t.Errorf("valid payload = %q, want A12****789", got)
	}
}

// develop fallback 金鑰必須可用：否則本機開發會在啟動時就失敗。
// 這條測試守住「金鑰常數長度正確」這個容易寫錯的細節。
func TestDevelopFallbackPIIKeyIsValid(t *testing.T) {
	cipher, err := newPIICipher(developFallbackPIIKey)
	if err != nil {
		t.Fatalf("develop fallback key is unusable: %v", err)
	}
	encrypted, err := cipher.encrypt("A123456789")
	if err != nil {
		t.Fatalf("encrypt with fallback key: %v", err)
	}
	decrypted, err := cipher.decrypt(encrypted)
	if err != nil || decrypted != "A123456789" {
		t.Errorf("round trip with fallback key = (%q, %v)", decrypted, err)
	}
}
