-- 009: 募資期限與取消退款
--
-- 在此之前 listings 的 cancelled 狀態存在於 CHECK 約束，但沒有任何程式碼會設定它。
-- 這代表募不滿的標的會永久停在 funding，出借人已投入的資金被無限期鎖住。

-- 募資截止時間。既有標的以建立時間 + 14 天回填。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS funding_deadline TIMESTAMPTZ;

UPDATE listings
SET funding_deadline = created_at + INTERVAL '14 days'
WHERE funding_deadline IS NULL;

ALTER TABLE listings ALTER COLUMN funding_deadline SET NOT NULL;

ALTER TABLE listings ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ;
ALTER TABLE listings ADD COLUMN IF NOT EXISTS cancel_reason TEXT NOT NULL DEFAULT '';

-- 掃描逾期標的的查詢條件
CREATE INDEX IF NOT EXISTS listings_funding_deadline_idx
    ON listings (funding_deadline)
    WHERE status = 'funding';

-- ---------------------------------------------------------------- 退款紀錄

-- 取消標的時把資金退回出借人。獨立記錄而非直接改 investments，
-- 理由是「投入」與「退款」是兩筆不同的事件，對帳時需要都看得到。
CREATE TABLE IF NOT EXISTS investment_refunds (
    id BIGSERIAL PRIMARY KEY,
    investment_id BIGINT NOT NULL REFERENCES investments (id) ON DELETE CASCADE,
    listing_id TEXT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    investor_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    amount BIGINT NOT NULL CHECK (amount > 0),
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 一筆投資只能退款一次；重複取消不會重複退錢
    CONSTRAINT investment_refunds_once_per_investment UNIQUE (investment_id)
);

CREATE INDEX IF NOT EXISTS investment_refunds_investor_idx
    ON investment_refunds (investor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS investment_refunds_listing_idx
    ON investment_refunds (listing_id);
