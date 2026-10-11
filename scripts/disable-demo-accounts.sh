#!/usr/bin/env bash
#
# 停用 seed 建立的展示帳號，並移除 seed 寫入的假資料。
#
# 用於「曾經在正式環境跑過 seed」的資料庫（舊版 deploy.sh 不分環境都會執行 seed）。
# 預設只輸出盤點結果（dry-run），加上 --apply 才會在單一 transaction 內修改。
#
# 刻意「停用」而非刪除帳號：users 的外鍵多為 ON DELETE CASCADE，
# 刪除展示出借人會連帶刪掉它在真實標的上的投資與分潤紀錄，
# 而 application_reviews.reviewer_id 沒有 cascade，刪除展示風控員會直接失敗。
# 展示帳號在真實資料上留下的痕跡只列出，交由人工判斷。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

usage() {
  cat <<'EOF'
用法：./scripts/disable-demo-accounts.sh <develop|production> [--apply]

  預設只盤點，不修改任何資料。
  --apply   停用展示帳號（作廢密碼與信箱、撤銷 session 與信件 token、餘額歸零），
            並刪除 seed 寫入的展示合約、待審申請與募資標的。
EOF
}

environment="${1:-}"
if [[ -z "$environment" || "$environment" == "-h" || "$environment" == "--help" ]]; then
  usage
  exit $([[ -z "$environment" ]] && echo 1 || echo 0)
fi
shift

APPLY=0
while (($#)); do
  case "$1" in
    --apply) APPLY=1 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "未知選項：$1" ;;
  esac
  shift
done

require_docker_compose
require_environment "$environment"
prepare_env_file
wait_for_database 45

# 與 backend/seed/*.sql 一致
DEMO_USERS="'usr_demo_borrower', 'usr_demo_investor', 'usr_demo_reviewer'"
DEMO_LOANS="'LN-2025-08841', 'LN-2024-11207', 'LN-2023-07733'"
DEMO_APPLICATION="'LN-2026-00412'"
# 已上架募資的展示申請（標的 LT-2026-000520）
DEMO_FUNDING_APPLICATION="'LN-2026-00520'"

# 展示標的可安全刪除的條件：沒有任何真實出借人投入過、也尚未撥款成合約。
# 真實出借人的資金必須走既有的取消流程退款（留下 investment_refunds 稽核紀錄），
# 不能被 cascade 一併刪除。
DEMO_FUNDING_DELETABLE="a.id = $DEMO_FUNDING_APPLICATION AND a.user_id = 'usr_demo_borrower'
  AND NOT EXISTS (SELECT 1 FROM loans lo WHERE lo.application_id = a.id)
  AND NOT EXISTS (
    SELECT 1 FROM investments i JOIN listings l ON l.id = i.listing_id
    WHERE l.application_id = a.id AND i.investor_id NOT IN ($DEMO_USERS)
  )"

run_sql() {
  local temporary_file
  temporary_file="$(mktemp)"
  cat > "$temporary_file"
  sql_in_db "$temporary_file"
  rm -f "$temporary_file"
}

log "盤點 $environment 環境的展示資料"
run_sql <<SQL
\\echo '== 展示帳號（停用後 email 會改為 <id>@disabled.invalid）=='
SELECT u.id, u.email, u.role, u.available_balance,
       (SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires_at > NOW()) AS active_sessions
FROM users u WHERE u.id IN ($DEMO_USERS) ORDER BY u.id;

\\echo '== seed 寫入的展示資料（--apply 會刪除）=='
SELECT 'loan' AS kind, id FROM loans
WHERE id IN ($DEMO_LOANS) AND user_id = 'usr_demo_borrower' AND application_id IS NULL
UNION ALL
SELECT 'application', a.id FROM applications a
WHERE a.id = $DEMO_APPLICATION AND a.user_id = 'usr_demo_borrower'
  AND NOT EXISTS (SELECT 1 FROM listings l WHERE l.application_id = a.id)
UNION ALL
SELECT 'funding application + listing', a.id FROM applications a
WHERE $DEMO_FUNDING_DELETABLE;

\\echo '== 需人工判斷：展示帳號在 seed 以外留下的資料（不會自動處理）=='
SELECT 'demo borrower 的其他申請' AS item, COUNT(*) AS count FROM applications
WHERE user_id = 'usr_demo_borrower' AND id NOT IN ($DEMO_APPLICATION, $DEMO_FUNDING_APPLICATION)
UNION ALL
SELECT '真實出借人投入展示標的（筆數；需先退款）', COUNT(*) FROM investments i
JOIN listings l ON l.id = i.listing_id
WHERE l.application_id = $DEMO_FUNDING_APPLICATION AND i.investor_id NOT IN ($DEMO_USERS)
UNION ALL
SELECT '展示標的已撥款成合約', COUNT(*) FROM loans WHERE application_id = $DEMO_FUNDING_APPLICATION
UNION ALL
SELECT 'demo borrower 的其他合約', COUNT(*) FROM loans
WHERE user_id = 'usr_demo_borrower' AND id NOT IN ($DEMO_LOANS)
UNION ALL
SELECT 'demo investor 投入真實標的（筆數）', COUNT(*) FROM investments i
JOIN listings l ON l.id = i.listing_id
WHERE i.investor_id = 'usr_demo_investor' AND l.borrower_id NOT IN ($DEMO_USERS)
UNION ALL
SELECT 'demo investor 投入真實標的（金額）', COALESCE(SUM(i.amount), 0) FROM investments i
JOIN listings l ON l.id = i.listing_id
WHERE i.investor_id = 'usr_demo_investor' AND l.borrower_id NOT IN ($DEMO_USERS)
UNION ALL
SELECT 'demo reviewer 做過的審核', COUNT(*) FROM application_reviews
WHERE reviewer_id = 'usr_demo_reviewer';
SQL

if (( ! APPLY )); then
  log "dry-run 完成，未修改任何資料。確認無誤後加上 --apply 執行"
  exit 0
fi

log "停用展示帳號並移除展示資料"
run_sql <<SQL
BEGIN;

-- 作廢的雜湊不是合法 bcrypt 格式，任何密碼都比對失敗；
-- 改掉 email 讓忘記密碼流程也找不到這些帳號
UPDATE users SET
    password_hash = '!disabled-demo-account',
    email = id || '@disabled.invalid',
    available_balance = 0,
    updated_at = NOW()
WHERE id IN ($DEMO_USERS);

DELETE FROM sessions WHERE user_id IN ($DEMO_USERS);
DELETE FROM email_tokens WHERE user_id IN ($DEMO_USERS);

DELETE FROM loans
WHERE id IN ($DEMO_LOANS) AND user_id = 'usr_demo_borrower' AND application_id IS NULL;

DELETE FROM applications a
WHERE a.id = $DEMO_APPLICATION AND a.user_id = 'usr_demo_borrower'
  AND NOT EXISTS (SELECT 1 FROM listings l WHERE l.application_id = a.id);

-- cascade 刪除其標的與 demo investor 的投資
DELETE FROM applications a WHERE $DEMO_FUNDING_DELETABLE;

COMMIT;
SQL

log "完成。若上方「需人工判斷」有非零項目，請另行處理："
log "  - 真實出借人投入展示標的：由正式的 reviewer 呼叫 POST /v1/admin/listings/LT-2026-000520/cancel"
log "    原額退款後保留紀錄（標的轉為已取消，不再出現在市集）"
