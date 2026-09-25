package main

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// CSRF 採 double-submit cookie：
//
// 伺服器下發一個「非 HttpOnly」的 csrf cookie，前端 JS 讀出其值並放進
// X-CSRF-Token header。攻擊者的跨站頁面雖能讓瀏覽器自動帶上 cookie，
// 但受同源政策限制讀不到 cookie 值，因此無法補上相符的 header。
//
// 與 SameSite=Strict 的 session cookie 形成雙重防護。

const (
	csrfCookieName = "creditflow_csrf"
	csrfHeaderName = "X-CSRF-Token"
)

func newCSRFToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// setCSRFCookie 下發 CSRF token。
// 刻意不設 HttpOnly：前端必須能讀取它才能放進 header。
func (s *apiServer) setCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *apiServer) clearCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

// csrfExemptPaths 是不需 CSRF token 的端點。
//
// 登入與註冊本身尚未持有 session，CSRF 對它們沒有實質意義
// （攻擊者讓受害者「登入攻擊者的帳號」屬於 session fixation，
// 由登入後重新下發 session 與 CSRF token 處理）。
var csrfExemptPaths = map[string]bool{
	"/v1/auth/login":    true,
	"/v1/auth/register": true,
	// 這三個端點由「尚未登入」的使用者呼叫：忘記密碼的人沒有 session，
	// 點信件連結的人也是從外部進來的。要求 CSRF token 會讓這些流程無法使用。
	//
	// 它們的防護來自別處：token 本身是高熵且單次使用的秘密，
	// 且 forgot-password 不洩漏帳號是否存在，並套用了限流。
	"/v1/auth/forgot-password": true,
	"/v1/auth/reset-password":  true,
	"/v1/auth/verify-email":    true,
}

// withCSRF 驗證非安全方法的請求帶有相符的 CSRF token。
func (s *apiServer) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || csrfExemptPaths[strings.TrimRight(r.URL.Path, "/")] {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusForbidden, "missing CSRF cookie")
			return
		}
		header := r.Header.Get(csrfHeaderName)
		if header == "" {
			writeError(w, http.StatusForbidden, "missing "+csrfHeaderName+" header")
			return
		}
		// 定長比較，避免以回應時間推測 token
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			writeError(w, http.StatusForbidden, "CSRF token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// csrfToken 端點讓前端在未登入狀態也能取得 token（例如送出登入表單前）。
func (s *apiServer) issueCSRFToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 已有 token 就沿用，避免多開分頁互相覆蓋
	if cookie, err := r.Cookie(csrfCookieName); err == nil && cookie.Value != "" {
		writeJSON(w, http.StatusOK, map[string]string{"csrfToken": cookie.Value})
		return
	}
	token, err := newCSRFToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue CSRF token")
		return
	}
	s.setCSRFCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}

// rotateCSRFToken 在登入／註冊成功後換發新 token。
// 登入前後使用不同 token，避免攻擊者預先植入已知 token。
// 換發失敗不影響登入結果：前端會在下次請求前向 /v1/auth/csrf 取得 token。
func (s *apiServer) rotateCSRFToken(w http.ResponseWriter) {
	token, err := newCSRFToken()
	if err != nil {
		return
	}
	s.setCSRFCookie(w, token)
}
