//go:build integration

package main

import (
	"net/http"
	"testing"
	"time"
)

// tokenFor 直接從資料庫取出最新一個未使用的 token 的明文替代方案：
// 明文只出現在信件中，因此測試改為自行簽發一個 token 來驗證後續流程。
func issueTokenForTest(t *testing.T, server *apiServer, userID, purpose string, ttl time.Duration) string {
	t.Helper()
	token, err := server.issueEmailToken(testContext(t), userID, purpose, ttl)
	if err != nil {
		t.Fatalf("issue %s token: %v", purpose, err)
	}
	return token
}

func TestIntegrationEmailVerificationFlow(t *testing.T) {
	resetDatabase(t)
	server := newTestAPIServer(t)
	client := newTestClient(t, server)

	var created user
	client.do(http.MethodPost, "/v1/auth/register", map[string]string{
		"email": "newuser@creditflow.test", "password": "a-good-password",
		"displayName": "新使用者",
	}).expectStatus(t, http.StatusCreated, "register").decode(t, &created)

	t.Run("a new account starts unverified", func(t *testing.T) {
		if created.EmailVerified {
			t.Error("a freshly registered account reports as verified")
		}
		var verifiedAt *time.Time
		if err := testPool.QueryRow(testContext(t),
			`SELECT email_verified_at FROM users WHERE id = $1`, created.ID).Scan(&verifiedAt); err != nil {
			t.Fatalf("read verified_at: %v", err)
		}
		if verifiedAt != nil {
			t.Error("email_verified_at was set before verification")
		}
	})

	t.Run("a verification token was issued", func(t *testing.T) {
		var count int
		if err := testPool.QueryRow(testContext(t), `
			SELECT COUNT(*) FROM email_tokens
			WHERE user_id = $1 AND purpose = 'verify_email' AND used_at IS NULL
		`, created.ID).Scan(&count); err != nil {
			t.Fatalf("count tokens: %v", err)
		}
		if count != 1 {
			t.Errorf("%d unused verification tokens, want 1", count)
		}
	})

	// 未驗證不阻擋登入，但不可送出貸款申請
	t.Run("an unverified account cannot submit an application", func(t *testing.T) {
		response := client.do(http.MethodPost, "/v1/applications", validApplicationBody())
		response.expectStatus(t, http.StatusForbidden, "application while unverified")
		if response.errorMessage(t) == "" {
			t.Error("no message explaining why the application was refused")
		}
	})

	token := issueTokenForTest(t, server, created.ID, purposeVerifyEmail, verifyTokenTTL)

	t.Run("the token verifies the email", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": token}).
			expectStatus(t, http.StatusOK, "verify")

		var verifiedAt *time.Time
		if err := testPool.QueryRow(testContext(t),
			`SELECT email_verified_at FROM users WHERE id = $1`, created.ID).Scan(&verifiedAt); err != nil {
			t.Fatalf("read verified_at: %v", err)
		}
		if verifiedAt == nil {
			t.Error("email_verified_at was not set after verification")
		}
	})

	t.Run("the account can now submit an application", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/applications", validApplicationBody()).
			expectStatus(t, http.StatusCreated, "application after verification")
	})

	t.Run("the token cannot be reused", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": token}).
			expectStatus(t, http.StatusUnprocessableEntity, "reusing the token")
	})

	t.Run("resending is refused once verified", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/resend-verification", nil).
			expectStatus(t, http.StatusConflict, "resend after verification")
	})
}

func TestIntegrationVerificationRejectsBadTokens(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	client := newTestClient(t, server)

	// 既有帳號由 migration 標記為已驗證，改回未驗證以測試流程
	if _, err := testPool.Exec(testContext(t),
		`UPDATE users SET email_verified_at = NULL WHERE id = $1`, userID); err != nil {
		t.Fatalf("reset verification: %v", err)
	}

	tests := []struct {
		name  string
		token string
	}{
		{"empty token", ""},
		{"garbage token", "not-a-real-token"},
		{"whitespace token", "   "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client.do(http.MethodPost, "/v1/auth/verify-email",
				map[string]string{"token": test.token}).
				expectStatus(t, http.StatusUnprocessableEntity, test.name)
		})
	}

	t.Run("an expired token is rejected", func(t *testing.T) {
		token := issueTokenForTest(t, server, userID, purposeVerifyEmail, verifyTokenTTL)
		if _, err := testPool.Exec(testContext(t),
			`UPDATE email_tokens SET expires_at = NOW() - INTERVAL '1 hour'`); err != nil {
			t.Fatalf("expire token: %v", err)
		}
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": token}).
			expectStatus(t, http.StatusUnprocessableEntity, "expired token")
	})

	// 重新簽發應讓舊 token 失效
	t.Run("reissuing invalidates the previous token", func(t *testing.T) {
		first := issueTokenForTest(t, server, userID, purposeVerifyEmail, verifyTokenTTL)
		second := issueTokenForTest(t, server, userID, purposeVerifyEmail, verifyTokenTTL)

		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": first}).
			expectStatus(t, http.StatusUnprocessableEntity, "superseded token")
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": second}).
			expectStatus(t, http.StatusOK, "latest token")
	})
}

func TestIntegrationPasswordResetFlow(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	loggedIn := newTestClient(t, server)
	loggedIn.loginAs("borrower@creditflow.test", testPassword)

	// 另建一個 session，驗證重設會撤銷「所有」裝置
	secondDevice := loggedIn.fork()
	secondDevice.loginAs("borrower@creditflow.test", testPassword)

	t.Run("forgot-password does not reveal whether the account exists", func(t *testing.T) {
		anonymous := loggedIn.fork()

		existing := anonymous.do(http.MethodPost, "/v1/auth/forgot-password",
			map[string]string{"email": "borrower@creditflow.test"}).
			expectStatus(t, http.StatusOK, "existing account")
		missing := anonymous.do(http.MethodPost, "/v1/auth/forgot-password",
			map[string]string{"email": "nobody@creditflow.test"}).
			expectStatus(t, http.StatusOK, "missing account")

		if string(existing.Body) != string(missing.Body) {
			t.Errorf("responses differ and leak account existence:\n existing: %s\n missing:  %s",
				existing.Body, missing.Body)
		}
	})

	t.Run("no token is issued for an unknown account", func(t *testing.T) {
		var count int
		if err := testPool.QueryRow(testContext(t), `
			SELECT COUNT(*) FROM email_tokens WHERE purpose = 'reset_password'
			  AND user_id NOT IN (SELECT id FROM users)
		`).Scan(&count); err != nil {
			t.Fatalf("count orphan tokens: %v", err)
		}
		if count != 0 {
			t.Errorf("%d tokens exist for accounts that do not exist", count)
		}
	})

	token := issueTokenForTest(t, server, userID, purposeResetPassword, resetTokenTTL)

	t.Run("a short password is refused", func(t *testing.T) {
		anonymous := loggedIn.fork()
		anonymous.do(http.MethodPost, "/v1/auth/reset-password",
			map[string]string{"token": token, "password": "short"}).
			expectStatus(t, http.StatusUnprocessableEntity, "short password")
	})

	t.Run("the reset succeeds and revokes every session", func(t *testing.T) {
		anonymous := loggedIn.fork()
		anonymous.do(http.MethodPost, "/v1/auth/reset-password",
			map[string]string{"token": token, "password": "a-brand-new-password"}).
			expectStatus(t, http.StatusOK, "reset")

		// 兩個原本登入的 client 都該被登出
		loggedIn.do(http.MethodGet, "/v1/auth/me", nil).
			expectStatus(t, http.StatusUnauthorized, "first device after reset")
		secondDevice.do(http.MethodGet, "/v1/auth/me", nil).
			expectStatus(t, http.StatusUnauthorized, "second device after reset")

		var remaining int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userID).Scan(&remaining); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		if remaining != 0 {
			t.Errorf("%d sessions survived the reset", remaining)
		}
	})

	t.Run("the old password no longer works", func(t *testing.T) {
		fresh := loggedIn.fork()
		fresh.login("borrower@creditflow.test", testPassword).
			expectStatus(t, http.StatusUnauthorized, "old password")
	})

	t.Run("the new password works", func(t *testing.T) {
		fresh := loggedIn.fork()
		fresh.login("borrower@creditflow.test", "a-brand-new-password").
			expectStatus(t, http.StatusOK, "new password")
	})

	t.Run("the token cannot be reused", func(t *testing.T) {
		fresh := loggedIn.fork()
		fresh.do(http.MethodPost, "/v1/auth/reset-password",
			map[string]string{"token": token, "password": "yet-another-password"}).
			expectStatus(t, http.StatusUnprocessableEntity, "reusing the reset token")
	})
}

// 驗證 token 不可用於重設密碼（用途必須隔離），反之亦然。
func TestIntegrationTokenPurposesAreIsolated(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	client := newTestClient(t, server)

	verifyToken := issueTokenForTest(t, server, userID, purposeVerifyEmail, verifyTokenTTL)
	resetToken := issueTokenForTest(t, server, userID, purposeResetPassword, resetTokenTTL)

	t.Run("a verification token cannot reset a password", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/reset-password",
			map[string]string{"token": verifyToken, "password": "hijacked-password"}).
			expectStatus(t, http.StatusUnprocessableEntity, "verify token used for reset")
	})

	t.Run("a reset token cannot verify an email", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": resetToken}).
			expectStatus(t, http.StatusUnprocessableEntity, "reset token used for verify")
	})

	t.Run("each token still works for its own purpose", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/auth/verify-email",
			map[string]string{"token": verifyToken}).
			expectStatus(t, http.StatusOK, "verify with its own token")
		client.do(http.MethodPost, "/v1/auth/reset-password",
			map[string]string{"token": resetToken, "password": "a-proper-new-password"}).
			expectStatus(t, http.StatusOK, "reset with its own token")
	})
}

// token 只存雜湊：資料庫外洩不可直接用於驗證或重設。
func TestIntegrationEmailTokensAreHashedAtRest(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	token := issueTokenForTest(t, server, userID, purposeResetPassword, resetTokenTTL)

	var storedHash string
	if err := testPool.QueryRow(testContext(t),
		`SELECT token_hash FROM email_tokens WHERE user_id = $1 AND used_at IS NULL`,
		userID).Scan(&storedHash); err != nil {
		t.Fatalf("read token hash: %v", err)
	}

	if storedHash == token {
		t.Error("the token is stored verbatim; a database leak would allow account takeover")
	}
	if storedHash != hashSessionToken(token) {
		t.Error("the stored value does not match the expected hash")
	}
}

// 未驗證的帳號仍可登入並要求重寄。
func TestIntegrationUnverifiedAccountCanStillSignInAndResend(t *testing.T) {
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	if _, err := testPool.Exec(testContext(t),
		`UPDATE users SET email_verified_at = NULL WHERE id = $1`, userID); err != nil {
		t.Fatalf("reset verification: %v", err)
	}

	client := newTestClient(t, newTestAPIServer(t))
	account := client.loginAs("borrower@creditflow.test", testPassword)
	if account.EmailVerified {
		t.Error("the account reports as verified")
	}

	client.do(http.MethodPost, "/v1/auth/resend-verification", nil).
		expectStatus(t, http.StatusOK, "resend")

	var count int
	if err := testPool.QueryRow(testContext(t), `
		SELECT COUNT(*) FROM email_tokens
		WHERE user_id = $1 AND purpose = 'verify_email' AND used_at IS NULL
	`, userID).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 1 {
		t.Errorf("%d unused tokens after resending, want 1", count)
	}

	// 其他使用者不可替別人要求重寄
	t.Run("anonymous cannot resend", func(t *testing.T) {
		anonymous := client.fork()
		anonymous.do(http.MethodPost, "/v1/auth/resend-verification", nil).
			expectStatus(t, http.StatusUnauthorized, "anonymous resend")
	})
}
