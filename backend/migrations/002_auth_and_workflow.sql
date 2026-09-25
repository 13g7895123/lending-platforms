-- 002: 認證授權、申請審核閉環、合約生成所需結構
-- 全部 additive 且可重複執行（migrate.sh 會逐檔重跑所有 migration）

-- ---------------------------------------------------------------- 使用者與工作階段

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'borrower' CHECK (role IN ('borrower', 'reviewer')),
    credit_score INTEGER NOT NULL DEFAULT 700 CHECK (credit_score BETWEEN 0 AND 900),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- email 大小寫不敏感唯一：一律以 lower(email) 為準
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_key ON users (lower(email));

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at);

-- ---------------------------------------------------------------- 既有表綁定使用者

ALTER TABLE applications ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE loans ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES users (id) ON DELETE CASCADE;

-- 核准後由申請單生成合約，回指來源申請
ALTER TABLE loans ADD COLUMN IF NOT EXISTS application_id TEXT REFERENCES applications (id) ON DELETE SET NULL;
ALTER TABLE loans ADD COLUMN IF NOT EXISTS monthly_payment BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS applications_user_id_idx ON applications (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS loans_user_id_idx ON loans (user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS loans_application_id_key ON loans (application_id) WHERE application_id IS NOT NULL;

-- 001 的 status CHECK 只允許 pending/reviewing/approved/rejected，
-- 審核流程另需「要求補件」狀態
ALTER TABLE applications DROP CONSTRAINT IF EXISTS applications_status_check;
ALTER TABLE applications ADD CONSTRAINT applications_status_check
    CHECK (status IN ('pending', 'reviewing', 'approved', 'rejected', 'more_info_required'));

-- ---------------------------------------------------------------- 審核稽核軌跡

CREATE TABLE IF NOT EXISTS application_reviews (
    id BIGSERIAL PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    reviewer_id TEXT NOT NULL REFERENCES users (id),
    action TEXT NOT NULL CHECK (action IN ('approve', 'reject', 'request_more_info')),
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS application_reviews_application_id_idx
    ON application_reviews (application_id, created_at DESC);

-- ---------------------------------------------------------------- 攤還表

CREATE TABLE IF NOT EXISTS loan_installments (
    loan_id TEXT NOT NULL REFERENCES loans (id) ON DELETE CASCADE,
    installment_no INTEGER NOT NULL CHECK (installment_no > 0),
    due_date DATE NOT NULL,
    amount_due BIGINT NOT NULL CHECK (amount_due >= 0),
    principal BIGINT NOT NULL CHECK (principal >= 0),
    interest BIGINT NOT NULL CHECK (interest >= 0),
    remaining_balance BIGINT NOT NULL CHECK (remaining_balance >= 0),
    status TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'due', 'paid', 'overdue')),
    paid_at TIMESTAMPTZ,
    PRIMARY KEY (loan_id, installment_no)
);

CREATE INDEX IF NOT EXISTS loan_installments_due_idx ON loan_installments (due_date, status);
