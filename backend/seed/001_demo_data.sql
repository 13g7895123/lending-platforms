-- LN-2026-00412 在本版改由 applications 表承載（待審申請），
-- 清掉舊版 seed 遺留在 loans 的同 id 列，避免重複呈現。
DELETE FROM loans WHERE id = 'LN-2026-00412' AND application_id IS NULL;

INSERT INTO loans (id, user_id, product, amount, annual_rate, paid_installments, total_installments, status, status_tone, monthly_payment, created_at)
VALUES
    ('LN-2025-08841', 'usr_demo_borrower', '個人信用貸款', 500000, 4.88, 27, 36, '正常繳款', 'success', 14959, '2025-10-20T00:00:00Z'),
    ('LN-2024-11207', 'usr_demo_borrower', '汽車貸款', 680000, 3.45, 18, 60, '正常繳款', 'success', 12355, '2024-10-20T00:00:00Z'),
    ('LN-2023-07733', 'usr_demo_borrower', '信用卡代償', 250000, 6.10, 24, 24, '已結清', 'info', 11091, '2023-08-20T00:00:00Z')
ON CONFLICT (id) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    product = EXCLUDED.product,
    amount = EXCLUDED.amount,
    annual_rate = EXCLUDED.annual_rate,
    paid_installments = EXCLUDED.paid_installments,
    total_installments = EXCLUDED.total_installments,
    status = EXCLUDED.status,
    status_tone = EXCLUDED.status_tone,
    monthly_payment = EXCLUDED.monthly_payment;

-- 一筆待審申請，讓風控後台開箱即有案件可審
INSERT INTO applications (
    id, user_id, product, amount, term_months, purpose, applicant_name, id_number,
    phone, email, job, employment_years, annual_income, monthly_expenses,
    housing, note, documents, status, created_at
) VALUES (
    'LN-2026-00412', 'usr_demo_borrower', '企業週轉金', 1200000, 48, '創業／營運週轉',
    '陳建宏', 'A12****678', '0912-345-678', 'demo@creditflow.test',
    '自營商／SOHO', '3~5 年', 1080000, 32000, '自有有貸款',
    '擴充生產線設備', '["身分證正反面.jpg", "近六個月薪轉存摺.pdf"]'::jsonb,
    'pending', '2026-08-01T00:00:00Z'
)
ON CONFLICT (id) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    status = EXCLUDED.status;

-- market_listings 已由 listings 取代（migration 005 標記為 legacy）。
-- 標的現在來自風控核准的真實申請，不再由 seed 灌入靜態資料。
-- 保留這行確保重跑 seed 不會讓舊資料復活。
DELETE FROM market_listings;

-- 一筆已核准並上架募資的標的，讓投資市集開箱即有東西可投。
-- 對應 demo 借款人的企業週轉金申請（LN-2026-00412）。
INSERT INTO applications (
    id, user_id, product, amount, term_months, purpose, applicant_name,
    id_number, phone, email, job, employment_years, annual_income,
    monthly_expenses, housing, note, documents, status, created_at
) VALUES (
    'LN-2026-00520', 'usr_demo_borrower', '個人信用貸款', 800000, 48, '裝潢修繕',
    '陳建宏', '', '', 'demo@creditflow.test', '上市櫃公司員工', '5 年以上',
    1080000, 32000, '自有有貸款', '主臥與衛浴翻新',
    '["身分證正反面.jpg", "近六個月薪轉存摺.pdf"]'::jsonb,
    'funding', '2026-09-10T00:00:00Z'
)
ON CONFLICT (id) DO UPDATE SET status = 'funding';

INSERT INTO listings (
    id, application_id, borrower_id, purpose, grade, annual_rate,
    target_amount, funded_amount, term_months, job, employment_years, region,
    status, funding_deadline
) VALUES (
    'LT-2026-000520', 'LN-2026-00520', 'usr_demo_borrower', '裝潢修繕', 'A', 4.88,
    800000, 240000, 48, '上市櫃公司員工', '5 年以上', '自有有貸款', 'funding',
    -- 期限以執行時間為基準，讓重跑 seed 後標的不會立刻被逾期掃描取消
    NOW() + INTERVAL '14 days'
)
ON CONFLICT (id) DO UPDATE SET
    funded_amount = EXCLUDED.funded_amount,
    status = EXCLUDED.status,
    funding_deadline = EXCLUDED.funding_deadline,
    cancelled_at = NULL,
    cancel_reason = '';

-- 已有其他出借人投入的部分，讓進度條非零
INSERT INTO investments (listing_id, investor_id, amount, idempotency_key)
VALUES ('LT-2026-000520', 'usr_demo_investor', 240000, 'seed:LT-2026-000520:initial')
ON CONFLICT (idempotency_key) DO NOTHING;
