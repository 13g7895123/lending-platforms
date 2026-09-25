-- 011: Email 驗證與密碼重設
--
-- 補上帳號生命週期的兩個缺口：確認 email 屬於本人、忘記密碼時能自行重設。

-- 驗證時間。既有帳號視為已驗證：它們在此機制上線前建立，
-- 回溯要求驗證會把現有使用者鎖在門外。
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;

UPDATE users SET email_verified_at = created_at WHERE email_verified_at IS NULL;

-- ---------------------------------------------------------------- 一次性 token

CREATE TABLE IF NOT EXISTS email_tokens (
    -- 只存雜湊，與 sessions 相同策略：資料庫外洩時無法直接用於驗證或重設
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    expires_at TIMESTAMPTZ NOT NULL,
    -- 單次使用：用過即記錄時間，之後不再接受
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS email_tokens_user_purpose_idx
    ON email_tokens (user_id, purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS email_tokens_expires_at_idx
    ON email_tokens (expires_at);
