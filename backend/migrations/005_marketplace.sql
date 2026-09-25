-- 005: P2P 撮合
--
-- 讓投資市集成為真實撮合：標的來自已核准的申請，出借人投標累積資金，
-- 滿額後自動撥款生成合約。
--
-- 流程變更：核准不再直接生成合約，改為
--   approved（上架）→ funding（募資中）→ funded（募滿）→ disbursed（已撥款）
--
-- 舊的 market_listings 保留但不再使用（標記為 legacy），可隨時回滾。

-- ---------------------------------------------------------------- 出借人角色

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('borrower', 'investor', 'reviewer'));

-- 出借人可用餘額。真實系統會接金流與信託帳戶；
-- 此處簡化為單一餘額欄位，入金為展示用端點。
ALTER TABLE users ADD COLUMN IF NOT EXISTS available_balance BIGINT NOT NULL DEFAULT 0
    CHECK (available_balance >= 0);

-- ---------------------------------------------------------------- 申請狀態擴充

ALTER TABLE applications DROP CONSTRAINT IF EXISTS applications_status_check;
ALTER TABLE applications ADD CONSTRAINT applications_status_check
    CHECK (status IN (
        'pending', 'reviewing', 'approved', 'rejected', 'more_info_required',
        'funding', 'funded', 'disbursed'
    ));

-- ---------------------------------------------------------------- 募資標的

CREATE TABLE IF NOT EXISTS listings (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    borrower_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose TEXT NOT NULL,
    grade TEXT NOT NULL CHECK (grade IN ('A', 'B', 'C')),
    annual_rate DOUBLE PRECISION NOT NULL,
    -- 募資目標金額（等於核貸金額）
    target_amount BIGINT NOT NULL CHECK (target_amount > 0),
    -- 已募集金額，由 investments 累加；以 CHECK 防止超募
    funded_amount BIGINT NOT NULL DEFAULT 0 CHECK (funded_amount >= 0),
    term_months INTEGER NOT NULL,
    job TEXT NOT NULL DEFAULT '',
    employment_years TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'funding'
        CHECK (status IN ('funding', 'funded', 'disbursed', 'cancelled')),
    funded_at TIMESTAMPTZ,
    disbursed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 募集金額不得超過目標
    CONSTRAINT listings_not_oversubscribed CHECK (funded_amount <= target_amount)
);

-- 一筆申請只會產生一個標的
CREATE UNIQUE INDEX IF NOT EXISTS listings_application_id_key ON listings (application_id);
CREATE INDEX IF NOT EXISTS listings_status_idx ON listings (status, created_at DESC);
CREATE INDEX IF NOT EXISTS listings_borrower_idx ON listings (borrower_id, created_at DESC);

-- 合約回指標的，便於查詢資金來源
ALTER TABLE loans ADD COLUMN IF NOT EXISTS listing_id TEXT REFERENCES listings (id) ON DELETE SET NULL;

-- ---------------------------------------------------------------- 投標紀錄

CREATE TABLE IF NOT EXISTS investments (
    id BIGSERIAL PRIMARY KEY,
    listing_id TEXT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    investor_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    amount BIGINT NOT NULL CHECK (amount > 0),
    -- 冪等鍵：同一鍵重送只成立一次，防止重複扣款
    idempotency_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS investments_idempotency_key_uidx
    ON investments (idempotency_key);
CREATE INDEX IF NOT EXISTS investments_listing_idx ON investments (listing_id, created_at DESC);
CREATE INDEX IF NOT EXISTS investments_investor_idx ON investments (investor_id, created_at DESC);

-- ---------------------------------------------------------------- 舊資料標記

-- market_listings 是認證上線前的靜態展示資料，已由 listings 取代。
-- 保留資料表以便回滾，但停用所有列避免同時出現兩份市集。
COMMENT ON TABLE market_listings IS 'legacy: 已由 listings 取代，僅保留供回滾';
UPDATE market_listings SET active = FALSE WHERE active = TRUE;
