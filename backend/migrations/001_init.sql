CREATE TABLE IF NOT EXISTS applications (
    id TEXT PRIMARY KEY,
    product TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount >= 50000 AND amount <= 3000000),
    term_months INTEGER NOT NULL CHECK (term_months IN (12, 24, 36, 48, 60, 84)),
    purpose TEXT NOT NULL,
    applicant_name TEXT NOT NULL,
    id_number TEXT NOT NULL,
    phone TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL,
    job TEXT NOT NULL DEFAULT '',
    employment_years TEXT NOT NULL DEFAULT '',
    annual_income BIGINT NOT NULL CHECK (annual_income > 0),
    monthly_expenses BIGINT NOT NULL DEFAULT 0 CHECK (monthly_expenses >= 0),
    housing TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    documents JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'reviewing', 'approved', 'rejected')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS applications_status_created_at_idx
    ON applications (status, created_at DESC);

CREATE TABLE IF NOT EXISTS loans (
    id TEXT PRIMARY KEY,
    product TEXT NOT NULL,
    amount BIGINT NOT NULL,
    annual_rate DOUBLE PRECISION NOT NULL,
    paid_installments INTEGER NOT NULL DEFAULT 0,
    total_installments INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    status_tone TEXT NOT NULL DEFAULT 'info' CHECK (status_tone IN ('success', 'warning', 'info', 'error')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS market_listings (
    id TEXT PRIMARY KEY,
    purpose TEXT NOT NULL,
    grade TEXT NOT NULL CHECK (grade IN ('A', 'B', 'C')),
    annual_rate DOUBLE PRECISION NOT NULL,
    amount BIGINT NOT NULL,
    term_months INTEGER NOT NULL,
    funded_percent INTEGER NOT NULL CHECK (funded_percent >= 0 AND funded_percent <= 100),
    job TEXT NOT NULL,
    employment_years TEXT NOT NULL,
    region TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
