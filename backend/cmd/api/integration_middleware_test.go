//go:build integration

package main

import (
	"net/http"
	"testing"
	"time"
)

func TestIntegrationCSRFProtection(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	client := newTestClient(t, newTestAPIServer(t))
	client.loginAs("borrower@creditflow.test", testPassword)

	t.Run("mutating request without token is forbidden", func(t *testing.T) {
		client.doWithoutCSRF(http.MethodPost, "/v1/applications", validApplicationBody()).
			expectStatus(t, http.StatusForbidden, "POST without CSRF token")
	})

	t.Run("logout without token is forbidden", func(t *testing.T) {
		client.doWithoutCSRF(http.MethodPost, "/v1/auth/logout", nil).
			expectStatus(t, http.StatusForbidden, "logout without CSRF token")
	})

	t.Run("safe methods need no token", func(t *testing.T) {
		client.doWithoutCSRF(http.MethodGet, "/v1/dashboard", nil).
			expectStatus(t, http.StatusOK, "GET without CSRF token")
	})

	t.Run("matching token is accepted", func(t *testing.T) {
		client.do(http.MethodPost, "/v1/applications", validApplicationBody()).
			expectStatus(t, http.StatusCreated, "POST with CSRF token")
	})

	// login/register 為豁免端點：此時尚無 session，CSRF 無實質意義
	t.Run("login is exempt", func(t *testing.T) {
		fresh := client.fork()
		fresh.doWithoutCSRF(http.MethodPost, "/v1/auth/login", map[string]string{
			"email": "borrower@creditflow.test", "password": testPassword,
		}).expectStatus(t, http.StatusOK, "login without CSRF token")
	})

	// 登入後必須換發 token，避免攻擊者預先植入已知值
	t.Run("login rotates the csrf token", func(t *testing.T) {
		fresh := client.fork()
		fresh.do(http.MethodGet, "/v1/auth/csrf", nil).expectStatus(t, http.StatusOK, "issue token")
		before := fresh.cookie(csrfCookieName)
		if before == "" {
			t.Fatal("no csrf token was issued")
		}
		fresh.loginAs("borrower@creditflow.test", testPassword)
		if after := fresh.cookie(csrfCookieName); after == before {
			t.Error("csrf token was not rotated on login")
		}
	})
}

func TestIntegrationCSRFTokenMismatchIsRejected(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	client := newTestClient(t, server)
	client.loginAs("borrower@creditflow.test", testPassword)
	client.do(http.MethodGet, "/v1/auth/csrf", nil)

	// 手動送出一個 header 與 cookie 不符的請求
	request, err := http.NewRequest(http.MethodPost, client.server.URL+"/v1/applications", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "cookie-value"})
	request.Header.Set(csrfHeaderName, "header-value")

	response, err := client.http.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("mismatched token got HTTP %d, want 403", response.StatusCode)
	}
}

func TestIntegrationLoginRateLimit(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	// 只允許 3 次登入嘗試，便於觸發
	server.authLimiter = newRateLimiter(3, 3, time.Minute)
	client := newTestClient(t, server)

	var limited testResponse
	for attempt := 1; attempt <= 6; attempt++ {
		response := client.login("borrower@creditflow.test", "wrong-password")
		if response.StatusCode == http.StatusTooManyRequests {
			limited = response
			break
		}
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d got HTTP %d, want 401 or 429", attempt, response.StatusCode)
		}
	}

	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatal("rate limit was never triggered after 6 failed attempts")
	}
	if retryAfter := limited.Header.Get("Retry-After"); retryAfter == "" {
		t.Error("429 response has no Retry-After header")
	}
}

// 登入與註冊必須各自計桶：打滿註冊配額不該讓正常登入被擋。
func TestIntegrationAuthRateLimitBucketsAreSeparate(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)

	server := newTestAPIServer(t)
	server.authLimiter = newRateLimiter(3, 3, time.Minute)
	client := newTestClient(t, server)

	// 打滿註冊配額
	exhausted := false
	for attempt := 1; attempt <= 6; attempt++ {
		response := client.do(http.MethodPost, "/v1/auth/register", map[string]string{
			"email": "dup@creditflow.test", "password": "a-good-password", "displayName": "重複",
		})
		if response.StatusCode == http.StatusTooManyRequests {
			exhausted = true
			break
		}
	}
	if !exhausted {
		t.Fatal("register rate limit was never triggered")
	}

	// 登入應仍可用
	client.login("borrower@creditflow.test", testPassword).
		expectStatus(t, http.StatusOK, "login after register quota exhausted")
}

func TestIntegrationGlobalRateLimit(t *testing.T) {
	resetDatabase(t)

	server := newTestAPIServer(t)
	server.limiter = newRateLimiter(5, 5, time.Minute)
	client := newTestClient(t, server)

	limited := false
	for attempt := 1; attempt <= 10; attempt++ {
		if client.do(http.MethodGet, "/health", nil).StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("global rate limit was never triggered")
	}
}

func TestIntegrationRequestIDIsEchoed(t *testing.T) {
	resetDatabase(t)
	client := newTestClient(t, newTestAPIServer(t))

	t.Run("generated when absent", func(t *testing.T) {
		response := client.do(http.MethodGet, "/health", nil)
		if response.Header.Get("X-Request-ID") == "" {
			t.Error("response has no X-Request-ID")
		}
	})

	t.Run("caller supplied value is preserved", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodGet, client.server.URL+"/health", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("X-Request-ID", "caller-provided-id")
		response, err := client.http.Do(request)
		if err != nil {
			t.Fatalf("send request: %v", err)
		}
		defer response.Body.Close()
		if got := response.Header.Get("X-Request-ID"); got != "caller-provided-id" {
			t.Errorf("X-Request-ID = %q, want caller-provided-id", got)
		}
	})
}

func TestIntegrationRouteBoundaries(t *testing.T) {
	resetDatabase(t)
	createUser(t, "borrower@creditflow.test", "借款人", roleBorrower, 780)
	client := newTestClient(t, newTestAPIServer(t))
	client.loginAs("borrower@creditflow.test", testPassword)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"unknown loan subroute", http.MethodGet, "/v1/loans/LN-1/bogus", http.StatusNotFound},
		{"non numeric installment", http.MethodPost, "/v1/loans/LN-1/installments/abc/pay", http.StatusNotFound},
		{"zero installment", http.MethodPost, "/v1/loans/LN-1/installments/0/pay", http.StatusNotFound},
		{"missing loan id", http.MethodGet, "/v1/loans//schedule", http.StatusNotFound},
		{"wrong method on dashboard", http.MethodPost, "/v1/dashboard", http.StatusMethodNotAllowed},
		{"wrong method on health", http.MethodPost, "/health", http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client.do(test.method, test.path, nil).
				expectStatus(t, test.want, test.name)
		})
	}
}
