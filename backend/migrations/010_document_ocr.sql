-- 010: 文件 OCR 擷取與一致性比對
--
-- 取代「只驗檔案格式」的狀態：從文件中擷取文字，比對申請人填寫的資料
-- 是否出現在文件中。
--
-- 明確的能力邊界：OCR 能回答「這份文件裡有沒有出現申請人的姓名與身分證字號」，
-- 不能回答「這份文件是不是真的」。結果僅供風控參考，不自動核准或婉拒。

ALTER TABLE application_documents
    ADD COLUMN IF NOT EXISTS ocr_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (ocr_status IN ('pending', 'processing', 'done', 'failed', 'skipped'));

-- 擷取到的文字。保留原文供風控人工覆核，而非只存比對結果。
ALTER TABLE application_documents ADD COLUMN IF NOT EXISTS ocr_text TEXT NOT NULL DEFAULT '';
ALTER TABLE application_documents ADD COLUMN IF NOT EXISTS ocr_error TEXT NOT NULL DEFAULT '';
ALTER TABLE application_documents ADD COLUMN IF NOT EXISTS ocr_checked_at TIMESTAMPTZ;

-- 背景 worker 取件用：只掃 pending，依上傳順序處理
CREATE INDEX IF NOT EXISTS application_documents_ocr_pending_idx
    ON application_documents (created_at)
    WHERE ocr_status = 'pending';

-- ---------------------------------------------------------------- 比對結果

CREATE TABLE IF NOT EXISTS document_verifications (
    id BIGSERIAL PRIMARY KEY,
    document_id TEXT NOT NULL REFERENCES application_documents (id) ON DELETE CASCADE,
    -- 比對的項目：applicant_name、id_number
    field TEXT NOT NULL CHECK (field IN ('applicant_name', 'id_number')),
    -- matched：文件中找到申請人填寫的值
    -- not_found：擷取成功但找不到該值
    -- unreadable：OCR 未能擷取出足夠的文字
    result TEXT NOT NULL CHECK (result IN ('matched', 'not_found', 'unreadable')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 同一份文件的同一個項目只保留最新一次結果
    CONSTRAINT document_verifications_unique_field UNIQUE (document_id, field)
);

CREATE INDEX IF NOT EXISTS document_verifications_document_idx
    ON document_verifications (document_id);

-- 既有文件在 OCR 上線前上傳，標記為 skipped 而非 pending：
-- 它們不需要回溯處理，且避免 worker 一啟動就處理大量舊檔。
UPDATE application_documents SET ocr_status = 'skipped' WHERE ocr_checked_at IS NULL AND ocr_status = 'pending';
