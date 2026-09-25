-- 007: 真實檔案上傳
--
-- 取代 applications.documents（僅存檔名字串）的假上傳。
-- 檔案內容存於掛載的 volume，資料庫只存 metadata 與儲存路徑。
--
-- applications.documents 保留不動：舊資料仍可讀，且可隨時回滾。

CREATE TABLE IF NOT EXISTS application_documents (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    uploaded_by TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- 使用者提供的原始檔名，僅供顯示；絕不用於組出儲存路徑
    original_name TEXT NOT NULL,
    -- 實際儲存的相對路徑，由伺服器以隨機值生成
    storage_path TEXT NOT NULL,
    -- 由 magic bytes 判定的實際型別，不信任 Content-Type 與副檔名
    content_type TEXT NOT NULL CHECK (content_type IN ('application/pdf', 'image/jpeg', 'image/png')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    -- SHA-256，供完整性檢查與重複偵測
    checksum TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS application_documents_application_idx
    ON application_documents (application_id, created_at DESC);

-- 同一份申請不接受內容完全相同的檔案重複上傳
CREATE UNIQUE INDEX IF NOT EXISTS application_documents_dedupe
    ON application_documents (application_id, checksum);

-- 儲存路徑必須唯一，避免兩筆紀錄指向同一檔案
CREATE UNIQUE INDEX IF NOT EXISTS application_documents_storage_path_key
    ON application_documents (storage_path);
