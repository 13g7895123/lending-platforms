-- demo 帳號（僅供開發／展示環境）
--   借款人：demo@creditflow.test     / demo1234
--   出借人：investor@creditflow.test / invest1234（預先入金 200 萬）
--   風控員：reviewer@creditflow.test / review1234
-- 正式環境請勿套用本檔，或務必於上線前更換密碼。

INSERT INTO users (id, email, password_hash, display_name, role, credit_score, available_balance)
VALUES
    ('usr_demo_borrower', 'demo@creditflow.test',
     '$2a$10$OkTrrLgbtlo92dprHJVmGO9GPlJ5FiCoytm5Fw4jBHKVJn4Nqeave',
     '陳建宏', 'borrower', 782, 0),
    ('usr_demo_investor', 'investor@creditflow.test',
     '$2a$10$2zFCp/Y0Ur0ffxqj03Fvxuj.TxbUmWqEPrhLqEQL3A5VJiPm.hZ2W',
     '吳雅婷', 'investor', 700, 2000000),
    ('usr_demo_reviewer', 'reviewer@creditflow.test',
     '$2a$10$L0wIuNUWIpU4TUuBCKSfsenrMccR3EVFf3hlmLtGY..YzC3WsExg2',
     '林品瑄', 'reviewer', 700, 0)
ON CONFLICT (id) DO UPDATE SET
    email = EXCLUDED.email,
    password_hash = EXCLUDED.password_hash,
    display_name = EXCLUDED.display_name,
    role = EXCLUDED.role,
    credit_score = EXCLUDED.credit_score,
    available_balance = EXCLUDED.available_balance,
    updated_at = NOW();
