package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *apiServer {
	return &apiServer{secureCookies: false}
}

func TestIsSafeMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isSafeMethod(method) {
			t.Errorf("%s should be a safe method", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		if isSafeMethod(method) {
			t.Errorf("%s must not be treated as safe", method)
		}
	}
}

func TestNewCSRFTokenIsRandom(t *testing.T) {
	first, err := newCSRFToken()
	if err != nil {
		t.Fatalf("newCSRFToken: %v", err)
	}
	second, _ := newCSRFToken()
	if first == second {
		t.Error("newCSRFToken produced a duplicate token")
	}
	if len(first) < 32 {
		t.Errorf("token length %d is too short", len(first))
	}
}

// CSRF cookie 必須可被 JS 讀取（double-submit 的前提），
// 但 session cookie 必須 HttpOnly。
func TestCSRFCookieIsReadableByScript(t *testing.T) {
	server := newTestServer()
	recorder := httptest.NewRecorder()
	server.setCSRFCookie(recorder, "token-value")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != csrfCookieName {
		t.Errorf("cookie name = %q, want %q", cookie.Name, csrfCookieName)
	}
	if cookie.HttpOnly {
		t.Error("CSRF cookie must not be HttpOnly; the front end has to read it")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("CSRF cookie should be SameSite=Strict")
	}
}

func TestWithCSRF(t *testing.T) {
	server := newTestServer()
	reached := false
	handler := server.withCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		method     string
		path       string
		cookie     string
		header     string
		wantStatus int
		wantReach  bool
	}{
		{"GET needs no token", http.MethodGet, "/v1/dashboard", "", "", http.StatusOK, true},
		{"login is exempt", http.MethodPost, "/v1/auth/login", "", "", http.StatusOK, true},
		{"register is exempt", http.MethodPost, "/v1/auth/register", "", "", http.StatusOK, true},
		// 這些端點由尚未登入的使用者呼叫，要求 CSRF token 會讓流程無法使用
		{"forgot-password is exempt", http.MethodPost, "/v1/auth/forgot-password", "", "", http.StatusOK, true},
		{"reset-password is exempt", http.MethodPost, "/v1/auth/reset-password", "", "", http.StatusOK, true},
		{"verify-email is exempt", http.MethodPost, "/v1/auth/verify-email", "", "", http.StatusOK, true},
		{"POST without cookie is rejected", http.MethodPost, "/v1/applications", "", "tok", http.StatusForbidden, false},
		{"POST without header is rejected", http.MethodPost, "/v1/applications", "tok", "", http.StatusForbidden, false},
		{"mismatched token is rejected", http.MethodPost, "/v1/applications", "tok-a", "tok-b", http.StatusForbidden, false},
		{"matching token passes", http.MethodPost, "/v1/applications", "tok", "tok", http.StatusOK, true},
		{"PATCH is also protected", http.MethodPatch, "/v1/admin/applications/x", "", "", http.StatusForbidden, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reached = false
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: test.cookie})
			}
			if test.header != "" {
				request.Header.Set(csrfHeaderName, test.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if reached != test.wantReach {
				t.Errorf("handler reached = %v, want %v", reached, test.wantReach)
			}
		})
	}
}

func TestIssueCSRFTokenReusesExisting(t *testing.T) {
	server := newTestServer()

	// 首次呼叫應下發新 token
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/csrf", nil)
	recorder := httptest.NewRecorder()
	server.issueCSRFToken(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("no CSRF cookie was set on first call")
	}

	// 已有 token 時應沿用，不覆蓋（避免多分頁互踩）
	existing := httptest.NewRequest(http.MethodGet, "/v1/auth/csrf", nil)
	existing.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "already-have-one"})
	secondRecorder := httptest.NewRecorder()
	server.issueCSRFToken(secondRecorder, existing)
	if cookies := secondRecorder.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("existing token was overwritten with %q", cookies[0].Value)
	}
}
