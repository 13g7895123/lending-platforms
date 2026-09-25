package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "creditflow_session"
	sessionTTL        = 12 * time.Hour
	roleBorrower      = "borrower"
	roleInvestor      = "investor"
	roleReviewer      = "reviewer"
)

type contextKey string

const currentUserKey contextKey = "currentUser"

type user struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	CreditScore int    `json:"creditScore"`
	// 出借人的可用餘額；借款人一律為 0
	AvailableBalance int64 `json:"availableBalance"`
	// 信箱是否已驗證。未驗證不阻擋登入，但不可送出貸款申請。
	EmailVerified bool `json:"emailVerified"`
}

var errInvalidCredentials = errors.New("invalid email or password")

// ---------------------------------------------------------------- 密碼與 token

func hashPassword(plain string) (string, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(digest), nil
}

func verifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// newSessionToken 產生高熵的 session token（回傳明文，只會出現在 Set-Cookie）。
func newSessionToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// hashSessionToken 將 token 以 SHA-256 雜湊後才落地，
// 資料庫外洩時無法直接拿來冒用身分。
func hashSessionToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func newUserID() (string, error) {
	buffer := make([]byte, 8)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate user id: %w", err)
	}
	return "usr_" + hex.EncodeToString(buffer), nil
}

// ---------------------------------------------------------------- 驗證

func validateEmail(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("email is required")
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		return "", errors.New("email format is invalid")
	}
	return strings.ToLower(trimmed), nil
}

func validatePassword(value string) error {
	if len([]rune(value)) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(value) > 200 {
		return errors.New("password is too long")
	}
	return nil
}

// ---------------------------------------------------------------- session 存取

func (s *apiServer) createSession(ctx context.Context, userID string) (string, time.Time, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(sessionTTL)
	_, err = s.db.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashSessionToken(token), userID, expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expiresAt, nil
}

func (s *apiServer) userForToken(ctx context.Context, token string) (*user, error) {
	var found user
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, u.role, u.credit_score, u.available_balance,
		       u.email_verified_at IS NOT NULL
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > NOW()
	`, hashSessionToken(token)).Scan(&found.ID, &found.Email, &found.DisplayName,
		&found.Role, &found.CreditScore, &found.AvailableBalance, &found.EmailVerified)
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func (s *apiServer) deleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashSessionToken(token))
	return err
}

func (s *apiServer) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.secureCookies,
		// Strict：瀏覽器不會在任何跨站情境帶上 session，與 CSRF token 形成雙重防護。
		// 本站所有請求都經由同源的 Nuxt proxy，故不影響正常操作。
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *apiServer) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

// ---------------------------------------------------------------- middleware

func currentUser(r *http.Request) *user {
	value, _ := r.Context().Value(currentUserKey).(*user)
	return value
}

// requireAuth 驗證 session cookie，未通過一律 401，不洩漏原因差異。
func (s *apiServer) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		found, err := s.userForToken(ctx, cookie.Value)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				s.clearSessionCookie(w)
				writeError(w, http.StatusUnauthorized, "session is invalid or expired")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to verify session")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), currentUserKey, found)))
	}
}

// requireRole 必須套用在 requireAuth 之內。
func (s *apiServer) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		actor := currentUser(r)
		if actor == nil || actor.Role != role {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		next(w, r)
	})
}

// ---------------------------------------------------------------- handlers

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *apiServer) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request registerRequest
	if !decodeJSON(w, r, &request) {
		return
	}

	email, err := validateEmail(request.Email)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := validatePassword(request.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		writeError(w, http.StatusUnprocessableEntity, "displayName is required")
		return
	}

	passwordHash, err := hashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to process password")
		return
	}
	userID, err := newUserID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 自助註冊可選擇借款人或出借人；
	// reviewer 只能由 seed/維運建立，任何其他值一律降級為 borrower，避免權限提升。
	role := roleBorrower
	if request.Role == roleInvestor {
		role = roleInvestor
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, role)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, email, passwordHash, displayName, role)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email is already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	token, expiresAt, err := s.createSession(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start session")
		return
	}
	s.setSessionCookie(w, token, expiresAt)
	s.rotateCSRFToken(w)

	// 寄信在回應之後才重要：寄失敗使用者仍可要求重寄，不該讓註冊失敗
	s.sendVerificationEmail(ctx, userID, email, displayName)

	writeJSON(w, http.StatusCreated, user{
		ID: userID, Email: email, DisplayName: displayName, Role: role, CreditScore: 700,
	})
}

func (s *apiServer) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request loginRequest
	if !decodeJSON(w, r, &request) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var found user
	var passwordHash string
	var lockedUntil *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT id, email, password_hash, display_name, role, credit_score, available_balance,
		       email_verified_at IS NOT NULL, locked_until
		FROM users WHERE lower(email) = lower($1)
	`, strings.TrimSpace(request.Email)).Scan(
		&found.ID, &found.Email, &passwordHash, &found.DisplayName, &found.Role,
		&found.CreditScore, &found.AvailableBalance, &found.EmailVerified, &lockedUntil)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 即使帳號不存在也跑一次雜湊比對，避免以回應時間推測帳號是否存在
			verifyPassword("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", request.Password)
			writeError(w, http.StatusUnauthorized, errInvalidCredentials.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to verify credentials")
		return
	}

	// 鎖定期間即使密碼正確也拒絕：否則攻擊者猜中密碼時鎖定就形同虛設。
	// 訊息刻意不提及帳號，否則「被鎖定」本身會成為帳號存在的證據。
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(*lockedUntil, time.Now())))
		writeError(w, http.StatusTooManyRequests,
			"登入嘗試過於頻繁，請稍後再試或使用忘記密碼重設")
		return
	}

	if !verifyPassword(passwordHash, request.Password) {
		newLock, lockErr := s.recordFailedLogin(ctx, found.ID, clientIP(r, s.trustProxy))
		if lockErr != nil {
			slog.Error("could not record a failed login", "user_id", found.ID, "error", lockErr)
		}
		// 本次失敗剛好觸發鎖定時一併告知等待時間
		if !newLock.IsZero() {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(newLock, time.Now())))
			writeError(w, http.StatusTooManyRequests,
				"登入嘗試過於頻繁，請稍後再試或使用忘記密碼重設")
			return
		}
		writeError(w, http.StatusUnauthorized, errInvalidCredentials.Error())
		return
	}

	// 成功登入清空計數：合法使用者偶爾打錯不該累積到被鎖
	if err := s.clearLoginFailures(ctx, s.db, found.ID); err != nil {
		slog.Error("could not clear login failures", "user_id", found.ID, "error", err)
	}

	token, expiresAt, err := s.createSession(ctx, found.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start session")
		return
	}
	s.setSessionCookie(w, token, expiresAt)
	s.rotateCSRFToken(w)
	writeJSON(w, http.StatusOK, found)
}

func (s *apiServer) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = s.deleteSession(ctx, cookie.Value)
	}
	s.clearSessionCookie(w)
	s.clearCSRFCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *apiServer) me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, currentUser(r))
}

// optionalAuth 在有有效 session 時把使用者帶進 context，沒有則照常放行。
// 用於公開但會依登入狀態調整內容的端點（例如市集顯示「我已投入多少」）。
func (s *apiServer) optionalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			next(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		found, err := s.userForToken(ctx, cookie.Value)
		if err != nil {
			// 無效 session 視同未登入，不阻擋公開內容
			next(w, r)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), currentUserKey, found)))
	}
}
