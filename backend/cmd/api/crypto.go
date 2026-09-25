package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

// PII 以 AES-256-GCM 加密後才落地。GCM 同時提供機密性與完整性，
// 因此資料庫端被篡改的密文會在解密時失敗，而非靜默回傳錯誤資料。
//
// 金鑰由 PII_ENCRYPTION_KEY 環境變數注入（base64 編碼的 32 bytes），
// 絕不寫入版控。production 缺金鑰時 API 拒絕啟動。

const piiKeySize = 32

var errPIIKeyMissing = errors.New("PII_ENCRYPTION_KEY is not set")

type piiCipher struct {
	aead cipher.AEAD
}

// newPIICipher 由 base64 金鑰建立加密器。
func newPIICipher(encodedKey string) (*piiCipher, error) {
	trimmed := strings.TrimSpace(encodedKey)
	if trimmed == "" {
		return nil, errPIIKeyMissing
	}
	key, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode PII key: %w", err)
	}
	if len(key) != piiKeySize {
		return nil, fmt.Errorf("PII key must be %d bytes after base64 decoding, got %d", piiKeySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &piiCipher{aead: aead}, nil
}

// encrypt 回傳 nonce||ciphertext。每次呼叫都使用新的隨機 nonce，
// 因此同一段明文加密兩次會得到不同密文（防止以密文比對推測內容）。
func (c *piiCipher) encrypt(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(cryptorand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal 把密文附加在 nonce 之後，使 nonce 與密文一起落地
	return c.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

// decrypt 解開 encrypt 產生的 nonce||ciphertext。
func (c *piiCipher) decrypt(payload []byte) (string, error) {
	if len(payload) == 0 {
		return "", nil
	}
	nonceSize := c.aead.NonceSize()
	if len(payload) < nonceSize {
		return "", errors.New("ciphertext is shorter than the nonce")
	}
	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt PII: %w", err)
	}
	return string(plain), nil
}

// newPIIKey 產生一把新的 base64 金鑰，供部署腳本與開發環境使用。
func newPIIKey() (string, error) {
	key := make([]byte, piiKeySize)
	if _, err := io.ReadFull(cryptorand.Reader, key); err != nil {
		return "", fmt.Errorf("generate PII key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// ---------------------------------------------------------------- 遮罩

// maskIDNumber 遮蔽身分證字號，只留前 3 後 3（例：A12****789）。
// 長度不足時一律全遮，避免短字串反而洩漏更多。
func maskIDNumber(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) < 7 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:3]) + "****" + string(runes[len(runes)-3:])
}

// maskPhone 遮蔽電話，只留末 3 碼（例：****678）。
// 保留原字串中的分隔符會洩漏格式，故一律以固定樣式輸出。
func maskPhone(value string) string {
	digits := make([]rune, 0, len(value))
	for _, char := range value {
		if char >= '0' && char <= '9' {
			digits = append(digits, char)
		}
	}
	if len(digits) == 0 {
		return ""
	}
	if len(digits) <= 3 {
		return strings.Repeat("*", len(digits))
	}
	return "****" + string(digits[len(digits)-3:])
}

// validIDNumberLength 防止過長輸入塞爆加密欄位。
func validIDNumberLength(value string) bool {
	count := utf8.RuneCountInString(strings.TrimSpace(value))
	return count > 0 && count <= 64
}

// ---------------------------------------------------------------- 啟動與遷移

// developFallbackPIIKey 只在 develop 環境缺金鑰時使用，讓本機開發免設定。
// 它是固定值，因此 develop 資料庫的密文不具實質保護 —— 這是刻意的取捨，
// production 缺金鑰時會直接拒絕啟動。
// 解碼後為 "creditflow-develop-only-key-32b!"（恰好 32 bytes）
const developFallbackPIIKey = "Y3JlZGl0Zmxvdy1kZXZlbG9wLW9ubHkta2V5LTMyYiE="

// loadPIICipher 依環境決定金鑰來源。
func loadPIICipher(cfg config, logger *slog.Logger) (*piiCipher, error) {
	if cfg.piiKey != "" {
		return newPIICipher(cfg.piiKey)
	}
	if strings.EqualFold(cfg.appEnv, "production") {
		return nil, errors.New(
			"PII_ENCRYPTION_KEY must be set in production; generate one with scripts/deploy.sh --auto-secrets")
	}
	logger.Warn("PII_ENCRYPTION_KEY is not set; using the insecure develop fallback key",
		"app_env", cfg.appEnv)
	return newPIICipher(developFallbackPIIKey)
}

// migratePlaintextPII 把既有明文 PII 加密進新欄位並清空明文。
//
// 加密需要金鑰，SQL migration 無法完成，故在 API 啟動時執行。
// 本函式為 idempotent：已搬遷的列（明文為空）會被跳過。
//
// 注意部署順序：deploy.sh 會先啟動容器再跑 migration，因此 API 首次啟動時
// 加密欄位可能尚未存在。此時回報「尚未就緒」而非讓程序退出——
// 否則 API 會在 migration 有機會執行前就進入重啟迴圈。
func (s *apiServer) migratePlaintextPII(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ready, err := s.piiColumnsReady(ctx)
	if err != nil {
		return 0, err
	}
	if !ready {
		return -1, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, COALESCE(id_number, ''), COALESCE(phone, '')
		FROM applications
		WHERE (id_number IS NOT NULL AND id_number <> '')
		   OR (phone IS NOT NULL AND phone <> '')
	`)
	if err != nil {
		return 0, fmt.Errorf("scan plaintext PII: %w", err)
	}

	type pending struct {
		id       string
		idNumber string
		phone    string
	}
	batch := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.idNumber, &item.phone); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read plaintext PII: %w", err)
		}
		batch = append(batch, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate plaintext PII: %w", err)
	}

	for _, item := range batch {
		idEnc, err := s.pii.encrypt(item.idNumber)
		if err != nil {
			return 0, fmt.Errorf("encrypt id_number for %s: %w", item.id, err)
		}
		phoneEnc, err := s.pii.encrypt(item.phone)
		if err != nil {
			return 0, fmt.Errorf("encrypt phone for %s: %w", item.id, err)
		}
		// 只在該欄位尚未加密時寫入，避免覆蓋已加密的值
		if _, err := s.db.Exec(ctx, `
			UPDATE applications
			SET id_number_enc = COALESCE(id_number_enc, $2),
			    phone_enc = COALESCE(phone_enc, $3),
			    id_number = '',
			    phone = ''
			WHERE id = $1
		`, item.id, idEnc, phoneEnc); err != nil {
			return 0, fmt.Errorf("store encrypted PII for %s: %w", item.id, err)
		}
	}
	return len(batch), nil
}

// piiColumnsReady 檢查 migration 004 是否已建立加密欄位。
func (s *apiServer) piiColumnsReady(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_name = 'applications'
		  AND column_name IN ('id_number_enc', 'phone_enc')
	`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check PII columns: %w", err)
	}
	return count == 2, nil
}
