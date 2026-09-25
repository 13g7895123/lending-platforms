package main

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// 標頭值中的 CR/LF 會讓攻擊者注入額外標頭，夾帶其他收件人。
func TestSanitizeHeaderValue(t *testing.T) {
	tests := map[string]string{
		"normal@example.com":                    "normal@example.com",
		"請驗證您的信箱":                               "請驗證您的信箱",
		"evil@example.com\r\nBcc: victim@x.com": "evil@example.comBcc: victim@x.com",
		"line\nbreak":                           "linebreak",
		"carriage\rreturn":                      "carriagereturn",
		"  padded  ":                            "padded",
	}
	for input, want := range tests {
		if got := sanitizeHeaderValue(input); got != want {
			t.Errorf("sanitizeHeaderValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestComposeRejectsHeaderInjection(t *testing.T) {
	m := &mailer{from: "no-reply@creditflow.test", logger: quietLogger()}

	message := m.compose(
		"victim@example.com\r\nBcc: attacker@evil.com",
		"Subject\r\nX-Injected: yes",
		"body",
	)

	// 標頭區（第一個空行之前）不可含注入的標頭
	headerSection := message
	if index := strings.Index(message, "\r\n\r\n"); index >= 0 {
		headerSection = message[:index]
	}
	for _, injected := range []string{"Bcc:", "X-Injected:"} {
		// 注入的字串會被併入同一行，因此不該出現在行首
		for _, line := range strings.Split(headerSection, "\r\n") {
			if strings.HasPrefix(line, injected) {
				t.Errorf("header %q was injected as its own line", injected)
			}
		}
	}
}

func TestComposeUsesCRLF(t *testing.T) {
	m := &mailer{from: "no-reply@creditflow.test", logger: quietLogger()}
	message := m.compose("to@example.com", "主旨", "第一行\n第二行\r\n第三行")

	// RFC 5322 要求 CRLF；單獨的 LF 會讓部分伺服器拒收
	for _, line := range strings.Split(message, "\r\n") {
		if strings.Contains(line, "\n") || strings.Contains(line, "\r") {
			t.Errorf("line %q still contains a bare CR or LF", line)
		}
	}
	if !strings.Contains(message, "Content-Type: text/plain; charset=UTF-8") {
		t.Error("the message is missing a UTF-8 content type")
	}
	if !strings.Contains(message, "\r\n\r\n") {
		t.Error("the message has no blank line separating headers from the body")
	}
}

func TestNormalizeLineEndings(t *testing.T) {
	tests := map[string]string{
		"a\nb":     "a\r\nb",
		"a\r\nb":   "a\r\nb",
		"a\rb":     "a\r\nb",
		"a\r\n\nb": "a\r\n\r\nb",
		"plain":    "plain",
	}
	for input, want := range tests {
		if got := normalizeLineEndings(input); got != want {
			t.Errorf("normalizeLineEndings(%q) = %q, want %q", input, got, want)
		}
	}
}

// 未設定 SMTP_HOST 時不該嘗試寄信，也不該 panic。
func TestMailerDisabledWithoutHost(t *testing.T) {
	m := &mailer{host: "", logger: quietLogger()}
	if m.enabled() {
		t.Error("the mailer reports as enabled without a host")
	}
	// 不應 panic；內容改寫進日誌
	m.send("someone@example.com", "主旨", "內容")
}

func TestVerificationEmailContainsUsableLink(t *testing.T) {
	subject, body := verificationEmail("http://localhost:3000", "陳建宏", "abc123token")

	if subject == "" {
		t.Error("the subject is empty")
	}
	if !strings.Contains(body, "http://localhost:3000/verify-email?token=abc123token") {
		t.Errorf("the body does not contain a usable verification link:\n%s", body)
	}
	if !strings.Contains(body, "陳建宏") {
		t.Error("the body does not address the recipient by name")
	}
	// 尾端斜線不該造成雙斜線
	_, trailing := verificationEmail("http://localhost:3000/", "陳建宏", "tok")
	if strings.Contains(trailing, "//verify-email") {
		t.Error("a trailing slash in the base URL produced a double slash")
	}
}

func TestResetPasswordEmailContainsUsableLink(t *testing.T) {
	subject, body := resetPasswordEmail("http://localhost:3000", "陳建宏", "reset-token")

	if subject == "" {
		t.Error("the subject is empty")
	}
	if !strings.Contains(body, "http://localhost:3000/reset-password?token=reset-token") {
		t.Errorf("the body does not contain a usable reset link:\n%s", body)
	}
	// 必須說明時效與副作用，讓收件人知道發生了什麼
	if !strings.Contains(body, "1 小時") {
		t.Error("the body does not state how long the link is valid")
	}
	if !strings.Contains(body, "登出") {
		t.Error("the body does not warn that other sessions will be signed out")
	}
	if !strings.Contains(body, "並未要求") {
		t.Error("the body does not tell an unintended recipient what to do")
	}
}

func TestEmailTokenIsRandomAndLongEnough(t *testing.T) {
	first, err := newEmailToken()
	if err != nil {
		t.Fatalf("newEmailToken: %v", err)
	}
	if len(first) < 32 {
		t.Errorf("token length %d is too short to resist guessing", len(first))
	}
	second, _ := newEmailToken()
	if first == second {
		t.Error("newEmailToken produced a duplicate token")
	}
	// token 會出現在 URL 中，不可含需要編碼的字元
	if strings.ContainsAny(first, "+/=&?#") {
		t.Errorf("token %q contains characters that need URL encoding", first)
	}
}

// 重設連結的時效必須明顯短於驗證信：它能直接接管帳號。
func TestTokenTTLs(t *testing.T) {
	if resetTokenTTL >= verifyTokenTTL {
		t.Errorf("the reset TTL (%v) should be shorter than the verification TTL (%v)",
			resetTokenTTL, verifyTokenTTL)
	}
	if resetTokenTTL < 10*60*1e9 {
		t.Error("the reset window is too short to be usable")
	}
}
