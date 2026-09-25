-- 004: PII 加密
-- 身分證字號與電話改以 AES-256-GCM 加密存放（bytea），原明文欄位保留但清空。
--
-- 採「新欄位並存」策略：不 DROP 舊欄位，因此可隨時回滾到舊版程式碼
-- （舊版會讀到空字串而非崩潰）。確認穩定後再以另一支 migration 移除。

ALTER TABLE applications ADD COLUMN IF NOT EXISTS id_number_enc BYTEA;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS phone_enc BYTEA;

-- 舊明文欄位不再寫入，放寬 NOT NULL 以免舊程式路徑卡住
ALTER TABLE applications ALTER COLUMN id_number DROP NOT NULL;

-- 既有明文的搬遷由 API 啟動時的 migratePlaintextPII() 執行
-- （加密需要金鑰，SQL 端無法完成），此處僅備妥欄位。

-- 存取稽核：誰在何時讀取了完整 PII
CREATE TABLE IF NOT EXISTS pii_access_log (
    id BIGSERIAL PRIMARY KEY,
    application_id TEXT NOT NULL,
    accessed_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    field TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS pii_access_log_application_idx
    ON pii_access_log (application_id, created_at DESC);
CREATE INDEX IF NOT EXISTS pii_access_log_actor_idx
    ON pii_access_log (accessed_by, created_at DESC);
