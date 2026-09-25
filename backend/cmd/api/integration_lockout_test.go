//go:build integration

package main

import (
	"net/http"
	"testing"
	"time"
)

// lockoutServer 建立一個鎖定測試用的環境。
// IP 限流設高，確保被擋下的原因是帳號鎖定而非 IP 限流。
func lockoutServer(t *testing.T) (*apiServer, *testClient, string) {
	t.Helper()
	resetDatabase(t)
	userID := createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	return server, newTestClient(t, server), userID
}

func failLogin(t *testing.T, client *testClient, times int) testResponse {
	t.Helper()
	var last testResponse
	for i := 0; i < times; i++ {
		last = client.login("borrower@creditflow.test", "definitely-wrong")
	}
	return last
}

func TestIntegrationAccountLocksAfterRepeatedFailures(t *testing.T) {
	server, client, userID := lockoutServer(t)
	threshold := lockoutThresholds[0].attempts

	t.Run("failures below the threshold return 401", func(t *testing.T) {
		response := failLogin(t, client, threshold-1)
		response.expectStatus(t, http.StatusUnauthorized, "below the threshold")

		var count int
		var lockedUntil *time.Time
		if err := testPool.QueryRow(testContext(t), `
			SELECT failed_login_count, locked_until FROM users WHERE id = $1
		`, userID).Scan(&count, &lockedUntil); err != nil {
			t.Fatalf("read lockout state: %v", err)
		}
		if count != threshold-1 {
			t.Errorf("failed_login_count = %d, want %d", count, threshold-1)
		}
		if lockedUntil != nil {
			t.Error("the account was locked before reaching the threshold")
		}
	})

	t.Run("reaching the threshold locks the account", func(t *testing.T) {
		response := failLogin(t, client, 1)
		response.expectStatus(t, http.StatusTooManyRequests, "at the threshold")

		if response.Header.Get("Retry-After") == "" {
			t.Error("the lockout response has no Retry-After header")
		}

		var lockedUntil *time.Time
		if err := testPool.QueryRow(testContext(t),
			`SELECT locked_until FROM users WHERE id = $1`, userID).Scan(&lockedUntil); err != nil {
			t.Fatalf("read locked_until: %v", err)
		}
		if lockedUntil == nil || !lockedUntil.After(time.Now()) {
			t.Error("locked_until was not set to a future time")
		}
	})

	// 鎖定期間即使密碼正確也要拒絕：否則攻擊者猜中密碼時鎖定就形同虛設
	t.Run("the correct password is refused while locked", func(t *testing.T) {
		response := client.login("borrower@creditflow.test", testPassword)
		response.expectStatus(t, http.StatusTooManyRequests, "correct password while locked")

		var sessions int
		if err := testPool.QueryRow(testContext(t),
			`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userID).Scan(&sessions); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		if sessions != 0 {
			t.Errorf("%d sessions were created during a lockout", sessions)
		}
	})

	t.Run("the account works again once the lockout expires", func(t *testing.T) {
		if _, err := testPool.Exec(testContext(t), `
			UPDATE users SET locked_until = NOW() - INTERVAL '1 minute' WHERE id = $1
		`, userID); err != nil {
			t.Fatalf("expire the lockout: %v", err)
		}
		client.login("borrower@creditflow.test", testPassword).
			expectStatus(t, http.StatusOK, "after the lockout expired")
	})

	t.Run("a successful sign-in clears the counter", func(t *testing.T) {
		var count int
		var lockedUntil *time.Time
		if err := testPool.QueryRow(testContext(t), `
			SELECT failed_login_count, locked_until FROM users WHERE id = $1
		`, userID).Scan(&count, &lockedUntil); err != nil {
			t.Fatalf("read lockout state: %v", err)
		}
		if count != 0 {
			t.Errorf("failed_login_count = %d after a successful sign-in, want 0", count)
		}
		if lockedUntil != nil {
			t.Error("locked_until survived a successful sign-in")
		}
	})

	_ = server
}

// 中途成功登入應清空計數，讓偶爾打錯的使用者不會累積到被鎖。
func TestIntegrationSuccessfulLoginResetsTheCounter(t *testing.T) {
	_, client, userID := lockoutServer(t)
	threshold := lockoutThresholds[0].attempts

	failLogin(t, client, threshold-1)
	client.login("borrower@creditflow.test", testPassword).
		expectStatus(t, http.StatusOK, "successful sign-in")

	// 再錯 threshold-1 次仍不該被鎖，因為計數已歸零
	response := failLogin(t, client, threshold-1)
	response.expectStatus(t, http.StatusUnauthorized, "after the counter was reset")

	var count int
	if err := testPool.QueryRow(testContext(t),
		`SELECT failed_login_count FROM users WHERE id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("read count: %v", err)
	}
	if count != threshold-1 {
		t.Errorf("failed_login_count = %d, want %d (the counter did not restart)",
			count, threshold-1)
	}
}

// 鎖定機制不可成為帳號列舉的管道：
// 對不存在的帳號重複嘗試必須與存在但密碼錯誤的情況無法區分。
func TestIntegrationLockoutDoesNotRevealAccountExistence(t *testing.T) {
	_, client, _ := lockoutServer(t)
	threshold := lockoutThresholds[0].attempts

	// 對不存在的帳號嘗試同樣次數
	var lastMissing testResponse
	for i := 0; i < threshold+2; i++ {
		lastMissing = client.login("nobody@creditflow.test", "whatever")
	}

	// 不存在的帳號永遠是 401：沒有紀錄可累加，也就不會被鎖
	lastMissing.expectStatus(t, http.StatusUnauthorized, "unknown account")

	var orphanRows int
	if err := testPool.QueryRow(testContext(t), `
		SELECT COUNT(*) FROM users WHERE email = 'nobody@creditflow.test'
	`).Scan(&orphanRows); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if orphanRows != 0 {
		t.Error("a record was created for an account that does not exist")
	}

	// 存在的帳號在達到門檻前同樣回 401，因此前幾次無法區分兩者
	fresh := client.fork()
	early := failLogin(t, fresh, threshold-1)
	if early.StatusCode != lastMissing.StatusCode {
		t.Errorf("existing account returned %d but missing account returned %d; "+
			"the difference leaks which accounts exist",
			early.StatusCode, lastMissing.StatusCode)
	}
	if string(early.Body) != string(lastMissing.Body) {
		t.Errorf("response bodies differ and leak account existence:\n existing: %s\n missing:  %s",
			early.Body, lastMissing.Body)
	}
}

// 密碼重設成功後應解除鎖定：擁有者已透過信件證明身分。
func TestIntegrationPasswordResetClearsLockout(t *testing.T) {
	server, client, userID := lockoutServer(t)

	failLogin(t, client, lockoutThresholds[0].attempts)
	client.login("borrower@creditflow.test", testPassword).
		expectStatus(t, http.StatusTooManyRequests, "locked out")

	token := issueTokenForTest(t, server, userID, purposeResetPassword, resetTokenTTL)
	anonymous := client.fork()
	anonymous.do(http.MethodPost, "/v1/auth/reset-password",
		map[string]string{"token": token, "password": "a-brand-new-password"}).
		expectStatus(t, http.StatusOK, "reset")

	var count int
	var lockedUntil *time.Time
	if err := testPool.QueryRow(testContext(t), `
		SELECT failed_login_count, locked_until FROM users WHERE id = $1
	`, userID).Scan(&count, &lockedUntil); err != nil {
		t.Fatalf("read lockout state: %v", err)
	}
	if count != 0 || lockedUntil != nil {
		t.Errorf("the lockout survived a password reset (count=%d, locked=%v)", count, lockedUntil)
	}

	// 重設後應能立即以新密碼登入
	fresh := client.fork()
	fresh.login("borrower@creditflow.test", "a-brand-new-password").
		expectStatus(t, http.StatusOK, "sign in after the reset")
}

// 失敗來源會被記錄，供風控查閱異常樣態。
func TestIntegrationFailedLoginRecordsSource(t *testing.T) {
	_, client, userID := lockoutServer(t)

	failLogin(t, client, 1)

	var failedAt *time.Time
	var sourceIP string
	if err := testPool.QueryRow(testContext(t), `
		SELECT last_failed_login_at, last_failed_login_ip FROM users WHERE id = $1
	`, userID).Scan(&failedAt, &sourceIP); err != nil {
		t.Fatalf("read failure metadata: %v", err)
	}
	if failedAt == nil {
		t.Error("last_failed_login_at was not recorded")
	}
	if sourceIP == "" {
		t.Error("last_failed_login_ip was not recorded")
	}
}
