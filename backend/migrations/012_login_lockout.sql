-- 012: 登入失敗鎖定
--
-- 在此之前只有 IP 維度的限流：攻擊者輪替 IP 即可對單一帳號無限次嘗試，
-- 因為失敗次數從未按帳號記錄。

ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_count INTEGER NOT NULL DEFAULT 0
    CHECK (failed_login_count >= 0);

-- 鎖定到期時間。NULL 表示未鎖定。
-- 採暫時鎖定而非永久：永久鎖定會讓攻擊者能以故意輸錯來封鎖任意帳號。
ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

-- 最後一次失敗的時間與來源，供風控查閱異常樣態
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_failed_login_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_failed_login_ip TEXT NOT NULL DEFAULT '';

-- 風控查詢目前被鎖定的帳號
CREATE INDEX IF NOT EXISTS users_locked_until_idx
    ON users (locked_until)
    WHERE locked_until IS NOT NULL;
