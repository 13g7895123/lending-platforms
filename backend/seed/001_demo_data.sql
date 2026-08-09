INSERT INTO loans (id, product, amount, annual_rate, paid_installments, total_installments, status, status_tone, created_at)
VALUES
    ('LN-2025-08841', '個人信用貸款', 500000, 4.88, 27, 36, '正常繳款', 'success', '2025-10-20T00:00:00Z'),
    ('LN-2024-11207', '汽車貸款', 680000, 3.45, 18, 60, '正常繳款', 'success', '2024-10-20T00:00:00Z'),
    ('LN-2026-00412', '企業週轉金', 1200000, 5.20, 0, 0, '審核中', 'warning', '2026-08-01T00:00:00Z'),
    ('LN-2023-07733', '信用卡代償', 250000, 6.10, 24, 24, '已結清', 'info', '2023-08-20T00:00:00Z')
ON CONFLICT (id) DO UPDATE SET
    product = EXCLUDED.product,
    amount = EXCLUDED.amount,
    annual_rate = EXCLUDED.annual_rate,
    paid_installments = EXCLUDED.paid_installments,
    total_installments = EXCLUDED.total_installments,
    status = EXCLUDED.status,
    status_tone = EXCLUDED.status_tone;

INSERT INTO market_listings (id, purpose, grade, annual_rate, amount, term_months, funded_percent, job, employment_years, region)
VALUES
    ('A-2291', '債務整合', 'A', 5.8, 500000, 36, 78, '上市櫃員工', '5 年以上', '台北市'),
    ('B-1187', '創業週轉', 'B', 7.2, 1200000, 48, 45, '自營商', '3~5 年', '台中市'),
    ('A-2288', '裝潢修繕', 'A', 4.9, 300000, 24, 92, '軍公教', '5 年以上', '新北市'),
    ('C-0442', '醫療支出', 'C', 9.6, 180000, 18, 31, '服務業', '1~3 年', '高雄市'),
    ('B-1190', '教育進修', 'B', 6.5, 400000, 36, 66, '一般企業', '3~5 年', '桃園市'),
    ('A-2295', '汽車購置', 'A', 5.2, 680000, 60, 12, '上市櫃員工', '5 年以上', '新竹市'),
    ('B-1193', '信用卡代償', 'B', 7.8, 250000, 24, 88, '一般企業', '3~5 年', '台南市'),
    ('C-0448', '營運週轉', 'C', 10.4, 900000, 36, 22, '自營商', '1~3 年', '彰化縣')
ON CONFLICT (id) DO UPDATE SET
    purpose = EXCLUDED.purpose,
    grade = EXCLUDED.grade,
    annual_rate = EXCLUDED.annual_rate,
    amount = EXCLUDED.amount,
    term_months = EXCLUDED.term_months,
    funded_percent = EXCLUDED.funded_percent,
    job = EXCLUDED.job,
    employment_years = EXCLUDED.employment_years,
    region = EXCLUDED.region,
    active = TRUE;
