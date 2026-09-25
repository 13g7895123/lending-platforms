-- 003: 還款引擎
-- 讓攤還表的狀態真正會推進：還款、逾期判定、結清收尾。
-- 全部 additive 且可重複執行（migrate.sh 會逐檔重跑所有 migration）。

-- ---------------------------------------------------------------- 還款交易紀錄

CREATE TABLE IF NOT EXISTS repayments (
    id BIGSERIAL PRIMARY KEY,
    loan_id TEXT NOT NULL REFERENCES loans (id) ON DELETE CASCADE,
    -- 單期還款指向該期；提前清償為整筆交易，installment_no 為 NULL
    installment_no INTEGER,
    kind TEXT NOT NULL CHECK (kind IN ('installment', 'prepayment')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    principal BIGINT NOT NULL DEFAULT 0 CHECK (principal >= 0),
    interest BIGINT NOT NULL DEFAULT 0 CHECK (interest >= 0),
    -- 由誰觸發（借款人本人；未來若開放代繳可為他人）
    paid_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    -- 冪等鍵：同一鍵重送只會成立一次，防止重複扣款
    idempotency_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 冪等保證：同一筆還款意圖只能落一次帳
CREATE UNIQUE INDEX IF NOT EXISTS repayments_idempotency_key_uidx
    ON repayments (idempotency_key);

CREATE INDEX IF NOT EXISTS repayments_loan_id_idx
    ON repayments (loan_id, created_at DESC);

-- ---------------------------------------------------------------- 期數欄位擴充

-- 實收金額：本期範圍內不支援部分繳款，但保留欄位以利後續擴充與對帳
ALTER TABLE loan_installments ADD COLUMN IF NOT EXISTS paid_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE loan_installments ADD COLUMN IF NOT EXISTS overdue_days INTEGER NOT NULL DEFAULT 0;

-- loan_installments.status 狀態機：
--   scheduled --(due_date 到期)--> due --(還款)--> paid
--   due       --(due_date 過期未繳)--> overdue --(還款)--> paid
-- paid 為終局狀態，不可再轉出。

-- ---------------------------------------------------------------- 合約結清

ALTER TABLE loans ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ;

-- 催收清單需依逾期天數排序
CREATE INDEX IF NOT EXISTS loan_installments_overdue_idx
    ON loan_installments (status, overdue_days DESC)
    WHERE status = 'overdue';
