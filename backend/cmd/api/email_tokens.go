package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Email 驗證與密碼重設共用一套一次性 token。
//
// 密碼重設是帳號接管的主要攻擊面，因此：
//   - token 只存雜湊（資料庫外洩不可用於重設）
//   - 單次使用（用過即失效）
//   - 短時效
//   - 重設成功後撤銷所有既有 session（攻擊者可能已登入）
//   - 對未註冊的 email 也回傳成功（不洩漏帳號是否存在）

const (
	purposeVerifyEmail   = "verify_email"
	purposeResetPassword = "reset_password"

	// 驗證信的時效較長：使用者可能過幾小時才看信
	verifyTokenTTL = 24 * time.Hour
	// 重設連結時效短：它能直接接管帳號
	resetTokenTTL = time.Hour
)

var errTokenInvalid = errors.New("token is invalid or has expired")

// newEmailToken 產生高熵的一次性 token（回傳明文，只會出現在信件中）。
func newEmailToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate email token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// issueEmailToken 建立並記錄一個 token，回傳明文供信件使用。
//
// 同一用途的既有未使用 token 會一併失效：重寄驗證信後舊連結不該還能用。
func (s *apiServer) issueEmailToken(
	ctx context.Context, userID, purpose string, ttl time.Duration,
) (string, error) {
	token, err := newEmailToken()
	if err != nil {
		return "", err
	}

	if _, err := s.db.Exec(ctx, `
		UPDATE email_tokens SET used_at = NOW()
		WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL
	`, userID, purpose); err != nil {
		return "", fmt.Errorf("invalidate previous tokens: %w", err)
	}

	if _, err := s.db.Exec(ctx, `
		INSERT INTO email_tokens (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, $3, $4)
	`, hashSessionToken(token), userID, purpose, time.Now().Add(ttl)); err != nil {
		return "", fmt.Errorf("store email token: %w", err)
	}
	return token, nil
}

// consumeEmailToken 驗證並用掉一個 token，回傳其所屬使用者。
//
// 以 UPDATE ... RETURNING 一次完成「檢查 + 標記已使用」：
// 先查再改會讓同一個 token 在併發下被用兩次。
func (s *apiServer) consumeEmailToken(
	ctx context.Context, transaction dbExecutorQuerier, token, purpose string,
) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errTokenInvalid
	}

	rows, err := transaction.Query(ctx, `
		UPDATE email_tokens SET used_at = NOW()
		WHERE token_hash = $1 AND purpose = $2
		  AND used_at IS NULL AND expires_at > NOW()
		RETURNING user_id
	`, hashSessionToken(token), purpose)
	if err != nil {
		return "", fmt.Errorf("consume email token: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("read consumed token: %w", err)
		}
		return "", errTokenInvalid
	}
	var userID string
	if err := rows.Scan(&userID); err != nil {
		return "", fmt.Errorf("scan token owner: %w", err)
	}
	return userID, nil
}

// sendVerificationEmail 產生 token 並寄出驗證信。
// 寄信失敗不回報錯誤給呼叫端：使用者可以要求重寄。
func (s *apiServer) sendVerificationEmail(ctx context.Context, userID, email, displayName string) {
	token, err := s.issueEmailToken(ctx, userID, purposeVerifyEmail, verifyTokenTTL)
	if err != nil {
		slog.Error("could not issue a verification token", "user_id", userID, "error", err)
		return
	}
	subject, body := verificationEmail(s.publicBaseURL, displayName, token)
	s.mailer.send(email, subject, body)
}

// ---------------------------------------------------------------- 驗證 email

func (s *apiServer) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeInternalError(w, "failed to start transaction", err)
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	userID, err := s.consumeEmailToken(ctx, transaction, request.Token, purposeVerifyEmail)
	if err != nil {
		if errors.Is(err, errTokenInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "此驗證連結無效或已失效")
			return
		}
		writeInternalError(w, "failed to verify the token", err)
		return
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE users SET email_verified_at = COALESCE(email_verified_at, NOW()), updated_at = NOW()
		WHERE id = $1
	`, userID); err != nil {
		writeInternalError(w, "failed to mark the email as verified", err)
		return
	}
	if err := transaction.Commit(ctx); err != nil {
		writeInternalError(w, "failed to commit verification", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

// resendVerification 重寄驗證信。需登入，避免被用來轟炸他人信箱。
func (s *apiServer) resendVerification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor := currentUser(r)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var verifiedAt *time.Time
	if err := s.db.QueryRow(ctx,
		`SELECT email_verified_at FROM users WHERE id = $1`, actor.ID).Scan(&verifiedAt); err != nil {
		writeInternalError(w, "failed to load the account", err)
		return
	}
	if verifiedAt != nil {
		writeError(w, http.StatusConflict, "此信箱已完成驗證")
		return
	}

	s.sendVerificationEmail(ctx, actor.ID, actor.Email, actor.DisplayName)
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ---------------------------------------------------------------- 密碼重設

func (s *apiServer) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var userID, displayName, email string
	err := s.db.QueryRow(ctx, `
		SELECT id, display_name, email FROM users WHERE lower(email) = lower($1)
	`, strings.TrimSpace(request.Email)).Scan(&userID, &displayName, &email)

	// 帳號不存在時也回傳成功：否則這個端點會變成帳號列舉工具
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("could not look up the account for reset", "error", err)
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "sent",
			"note":   "若該信箱已註冊，重設連結將寄達",
		})
		return
	}

	token, err := s.issueEmailToken(ctx, userID, purposeResetPassword, resetTokenTTL)
	if err != nil {
		writeInternalError(w, "failed to issue a reset token", err)
		return
	}
	subject, body := resetPasswordEmail(s.publicBaseURL, displayName, token)
	s.mailer.send(email, subject, body)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "sent",
		"note":   "若該信箱已註冊，重設連結將寄達",
	})
}

func (s *apiServer) resetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := validatePassword(request.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	passwordHash, err := hashPassword(request.Password)
	if err != nil {
		writeInternalError(w, "failed to process the password", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	transaction, err := s.db.Begin(ctx)
	if err != nil {
		writeInternalError(w, "failed to start transaction", err)
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	userID, err := s.consumeEmailToken(ctx, transaction, request.Token, purposeResetPassword)
	if err != nil {
		if errors.Is(err, errTokenInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "此重設連結無效或已失效")
			return
		}
		writeInternalError(w, "failed to verify the reset token", err)
		return
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1
	`, userID, passwordHash); err != nil {
		writeInternalError(w, "failed to update the password", err)
		return
	}

	// 撤銷所有既有 session：若帳號已被接管，改密碼必須同時把對方踢出
	if _, err := transaction.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		writeInternalError(w, "failed to revoke existing sessions", err)
		return
	}

	// 解除登入鎖定：擁有者已透過信件證明身分，不該還被先前的失敗次數鎖著
	if err := s.clearLoginFailures(ctx, transaction, userID); err != nil {
		writeInternalError(w, "failed to clear the login lockout", err)
		return
	}

	if err := transaction.Commit(ctx); err != nil {
		writeInternalError(w, "failed to commit the reset", err)
		return
	}

	// 目前的請求者也要被登出：新密碼應以新 session 重新登入
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "reset",
		"note":   "密碼已更新，所有裝置已登出，請以新密碼登入",
	})
}
