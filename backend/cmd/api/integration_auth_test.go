//go:build integration

package main

import (
	"net/http"
	"testing"
)

func TestIntegrationRegisterAndLogin(t *testing.T) {
	resetDatabase(t)
	client := newTestClient(t, newTestAPIServer(t))

	t.Run("register creates a borrower and starts a session", func(t *testing.T) {
		response := client.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "new@creditflow.test", "password": "a-good-password", "displayName": "新使用者",
		}).expectStatus(t, http.StatusCreated, "register")

		var created user
		response.decode(t, &created)
		if created.Role != roleBorrower {
			t.Errorf("role = %q, want borrower", created.Role)
		}
		if created.Email != "new@creditflow.test" {
			t.Errorf("email = %q", created.Email)
		}
		if client.cookie(sessionCookieName) == "" {
			t.Error("register did not set a session cookie")
		}
	})

	t.Run("me returns the current session user", func(t *testing.T) {
		var account user
		client.do(http.MethodGet, "/v1/auth/me", nil).
			expectStatus(t, http.StatusOK, "me").decode(t, &account)
		if account.Email != "new@creditflow.test" {
			t.Errorf("me returned %q", account.Email)
		}
	})

	t.Run("duplicate email is rejected", func(t *testing.T) {
		fresh := client.fork()
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "new@creditflow.test", "password": "another-password", "displayName": "重複",
		}).expectStatus(t, http.StatusConflict, "duplicate register")
	})

	// email 比對不分大小寫：同一個人不該因大小寫而註冊出兩個帳號
	t.Run("email comparison is case insensitive", func(t *testing.T) {
		fresh := client.fork()
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "NEW@CREDITFLOW.TEST", "password": "another-password", "displayName": "大寫",
		}).expectStatus(t, http.StatusConflict, "uppercase duplicate register")

		fresh.login("NEW@CreditFlow.TEST", "a-good-password").
			expectStatus(t, http.StatusOK, "login with different casing")
	})

	t.Run("short password is rejected", func(t *testing.T) {
		fresh := client.fork()
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "short@creditflow.test", "password": "1234567", "displayName": "短密碼",
		}).expectStatus(t, http.StatusUnprocessableEntity, "short password")
	})

	t.Run("invalid email is rejected", func(t *testing.T) {
		fresh := client.fork()
		fresh.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "not-an-email", "password": "a-good-password", "displayName": "壞信箱",
		}).expectStatus(t, http.StatusUnprocessableEntity, "invalid email")
	})

	t.Run("wrong password is rejected", func(t *testing.T) {
		fresh := client.fork()
		fresh.login("new@creditflow.test", "wrong-password").
			expectStatus(t, http.StatusUnauthorized, "wrong password")
	})

	t.Run("unknown account is rejected", func(t *testing.T) {
		fresh := client.fork()
		fresh.login("nobody@creditflow.test", "any-password").
			expectStatus(t, http.StatusUnauthorized, "unknown account")
	})
}

func TestIntegrationLogoutInvalidatesSession(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	client := newTestClient(t, newTestAPIServer(t))
	client.loginAs("borrower@creditflow.test", testPassword)
	client.do(http.MethodGet, "/v1/auth/me", nil).expectStatus(t, http.StatusOK, "me before logout")

	client.do(http.MethodPost, "/v1/auth/logout", nil).
		expectStatus(t, http.StatusOK, "logout")

	// session 必須在伺服器端失效，不只是清掉 cookie
	client.do(http.MethodGet, "/v1/auth/me", nil).
		expectStatus(t, http.StatusUnauthorized, "me after logout")

	var remaining int
	if err := testPool.QueryRow(testContext(t),
		`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d sessions remain after logout, want 0", remaining)
	}
}

// session token 必須以雜湊形式存放：資料庫外洩時不可直接拿來冒用身分。
func TestIntegrationSessionTokenIsHashedAtRest(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	client := newTestClient(t, newTestAPIServer(t))
	client.loginAs("borrower@creditflow.test", testPassword)

	rawToken := client.cookie(sessionCookieName)
	if rawToken == "" {
		t.Fatal("no session cookie was issued")
	}

	var storedHash string
	if err := testPool.QueryRow(testContext(t),
		`SELECT token_hash FROM sessions LIMIT 1`).Scan(&storedHash); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if storedHash == rawToken {
		t.Error("session token is stored verbatim; a database leak would allow impersonation")
	}
	if storedHash != hashSessionToken(rawToken) {
		t.Error("stored hash does not match hashSessionToken of the issued token")
	}
}

func TestIntegrationExpiredSessionIsRejected(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	client := newTestClient(t, newTestAPIServer(t))
	client.loginAs("borrower@creditflow.test", testPassword)

	// 讓 session 過期
	if _, err := testPool.Exec(testContext(t),
		`UPDATE sessions SET expires_at = NOW() - INTERVAL '1 hour'`); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	response := client.do(http.MethodGet, "/v1/auth/me", nil).
		expectStatus(t, http.StatusUnauthorized, "expired session")
	if message := response.errorMessage(t); message == "" {
		t.Error("expired session response has no error message")
	}
}

func TestIntegrationAuthorizationBoundaries(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	createUser(t, "reviewer@creditflow.test", "風控員", roleReviewer, 700)

	server := newTestAPIServer(t)
	anonymous := newTestClient(t, server)
	borrower := anonymous.fork()
	reviewer := anonymous.fork()
	borrower.loginAs("borrower@creditflow.test", testPassword)
	reviewer.loginAs("reviewer@creditflow.test", testReviewerPassword)

	protected := []string{"/v1/dashboard", "/v1/applications", "/v1/auth/me"}
	for _, path := range protected {
		t.Run("anonymous is rejected from "+path, func(t *testing.T) {
			anonymous.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusUnauthorized, path)
		})
	}

	reviewerOnly := []string{"/v1/admin/applications", "/v1/admin/overdue"}
	for _, path := range reviewerOnly {
		t.Run("anonymous is rejected from "+path, func(t *testing.T) {
			anonymous.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusUnauthorized, path)
		})
		t.Run("borrower is forbidden from "+path, func(t *testing.T) {
			borrower.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusForbidden, path)
		})
		t.Run("reviewer may access "+path, func(t *testing.T) {
			reviewer.do(http.MethodGet, path, nil).
				expectStatus(t, http.StatusOK, path)
		})
	}

	// 市集為公開資料，匿名仍須可讀（相容性保證）
	t.Run("market listings stay public", func(t *testing.T) {
		anonymous.do(http.MethodGet, "/v1/market/listings", nil).
			expectStatus(t, http.StatusOK, "market listings")
	})
}

// 自助註冊不得取得 reviewer 權限，即使請求主體夾帶 role 欄位。
func TestIntegrationRegisterCannotEscalateRole(t *testing.T) {
	resetDatabase(t)
	client := newTestClient(t, newTestAPIServer(t))

	response := client.do(http.MethodPost, "/v1/auth/register", map[string]any{
		"email": "sneaky@creditflow.test", "password": "a-good-password",
		"displayName": "試圖提權", "role": roleReviewer,
	}).expectStatus(t, http.StatusCreated, "register with role field")

	var created user
	response.decode(t, &created)
	if created.Role != roleBorrower {
		t.Fatalf("role = %q, want borrower; role escalation is possible", created.Role)
	}

	client.do(http.MethodGet, "/v1/admin/applications", nil).
		expectStatus(t, http.StatusForbidden, "admin access after register")

	var storedRole string
	if err := testPool.QueryRow(testContext(t),
		`SELECT role FROM users WHERE email = $1`, "sneaky@creditflow.test").Scan(&storedRole); err != nil {
		t.Fatalf("read stored role: %v", err)
	}
	if storedRole != roleBorrower {
		t.Errorf("stored role = %q, want borrower", storedRole)
	}
}
