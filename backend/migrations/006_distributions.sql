-- 006: 還款分潤
--
-- 借款人還款時，按投資占比把本金與利息分配給出借人，讓資金流回出借端。
-- 在此之前出借人的「預估收益」純為估算，實際收不到任何錢。

CREATE TABLE IF NOT EXISTS distributions (
    id BIGSERIAL PRIMARY KEY,
    -- 來源還款：一筆還款會產生多筆分潤（每位出借人一筆）
    repayment_id BIGINT NOT NULL REFERENCES repayments (id) ON DELETE CASCADE,
    investment_id BIGINT NOT NULL REFERENCES investments (id) ON DELETE CASCADE,
    investor_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    loan_id TEXT NOT NULL REFERENCES loans (id) ON DELETE CASCADE,
    principal BIGINT NOT NULL DEFAULT 0 CHECK (principal >= 0),
    interest BIGINT NOT NULL DEFAULT 0 CHECK (interest >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 同一筆還款對同一筆投資只能分配一次
    CONSTRAINT distributions_unique_per_repayment UNIQUE (repayment_id, investment_id)
);

CREATE INDEX IF NOT EXISTS distributions_investor_idx
    ON distributions (investor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS distributions_loan_idx
    ON distributions (loan_id, created_at DESC);

-- 投資的累計回收。可由 distributions 聚合得出，
-- 但出借人清單是高頻查詢，冗餘欄位避免每次都掃全表。
ALTER TABLE investments ADD COLUMN IF NOT EXISTS principal_returned BIGINT NOT NULL DEFAULT 0
    CHECK (principal_returned >= 0);
ALTER TABLE investments ADD COLUMN IF NOT EXISTS interest_earned BIGINT NOT NULL DEFAULT 0
    CHECK (interest_earned >= 0);
