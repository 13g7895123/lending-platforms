#!/usr/bin/env bash
# 端到端驗證：認證授權 → 申請審核閉環 → 合約生成
#
# 用法：./scripts/verify-flow.sh [base-url]
#   預設 base-url 為 http://localhost:13000（develop 環境的 Nuxt proxy）
#
# 全程透過 Nuxt 的 /api proxy 呼叫，等同於瀏覽器實際走的路徑。

set -uo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

BASE="${1:-http://localhost:13000}"
API="$BASE/api"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# csrf_token_from <cookie-jar> → 印出 jar 中的 CSRF token（沒有則為空）
csrf_token_from() {
  local jar="$1"
  [[ -f "$jar" ]] || return 0
  awk '$6 == "creditflow_csrf" { print $7 }' "$jar" | tail -1
}

# ensure_csrf <cookie-jar>：確保 jar 內有 CSRF token
ensure_csrf() {
  local jar="$1"
  [[ -n "$(csrf_token_from "$jar")" ]] && return 0
  curl -s -m 20 -o /dev/null -b "$jar" -c "$jar" "$API/v1/auth/csrf" || true
}

# request <method> <path> <cookie-jar|-> [body] → 印出 "狀態碼<TAB>回應主體"
#
# 非 GET 請求會自動附上 X-CSRF-Token（double-submit），
# 等同瀏覽器端 useCsrf().mutate() 的行為。
request() {
  local method="$1" path="$2" jar="$3" body="${4:-}"
  local args=(-s -m 20 -o "$WORK/body" -w '%{http_code}' -X "$method" "$API$path")
  if [[ "$jar" != "-" ]]; then
    args+=(-b "$jar" -c "$jar")
    if [[ "$method" != "GET" && "$method" != "HEAD" ]]; then
      ensure_csrf "$jar"
      local token
      token="$(csrf_token_from "$jar")"
      [[ -n "$token" ]] && args+=(-H "X-CSRF-Token: $token")
    fi
  fi
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' -d "$body")
  local code
  code="$(curl "${args[@]}")"
  printf '%s\t%s' "$code" "$(cat "$WORK/body")"
}

# request_no_csrf：刻意不帶 CSRF header，用於驗證防護生效
request_no_csrf() {
  local method="$1" path="$2" jar="$3" body="${4:-}"
  local args=(-s -m 20 -o "$WORK/body" -w '%{http_code}' -X "$method" "$API$path" -b "$jar" -c "$jar")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' -d "$body")
  local code
  code="$(curl "${args[@]}")"
  printf '%s\t%s' "$code" "$(cat "$WORK/body")"
}

code_of() { cut -f1 <<<"$1"; }
body_of() { cut -f2- <<<"$1"; }

# json_get <json> <python-expression-on-`d`>
json_get() {
  python3 -c 'import json,sys
d=json.load(sys.stdin)
print(eval(sys.argv[1]))' "$2" <<<"$1" 2>/dev/null
}

# disburse_new_loan：送出申請 → 核准上架 → 出借人投滿 → 回傳撥款後的合約 id。
#
# 核准不再直接發約，因此需要合約的測試段落都透過這個函式取得。
# 依賴 $BORROWER_JAR、$REVIEWER_JAR、$INVESTOR_JAR 已登入。
disburse_new_loan() {
  local suffix="${1:-$RANDOM}"
  local app_id listing_id target

  app_id="$(json_get "$(body_of "$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")")" 'd["id"]')"
  [[ -n "$app_id" ]] || return 1

  local approve_body
  approve_body="$(body_of "$(request PATCH "/v1/admin/applications/$app_id" "$REVIEWER_JAR" '{"action":"approve"}')")"
  listing_id="$(json_get "$approve_body" 'd["listing"]["id"]')"
  target="$(json_get "$approve_body" 'd["listing"]["targetAmount"]')"
  [[ -n "$listing_id" && -n "$target" ]] || return 1

  request POST /v1/investments/top-up "$INVESTOR_JAR" "{\"amount\":$target}" >/dev/null
  local invest_body
  invest_body="$(body_of "$(request POST "/v1/listings/$listing_id/invest" "$INVESTOR_JAR" \
    "{\"amount\":$target,\"requestId\":\"disburse-$suffix\"}")")"
  json_get "$invest_body" 'd["disbursedLoan"]["id"]'
}

expect_code() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then ok "$label（HTTP $got）"; else bad "$label：預期 HTTP $want，實得 $got"; fi
}

BORROWER_JAR="$WORK/borrower.cookies"
REVIEWER_JAR="$WORK/reviewer.cookies"
ANON_JAR="$WORK/anon.cookies"
: >"$BORROWER_JAR"; : >"$REVIEWER_JAR"; : >"$ANON_JAR"

step "0. 服務健康檢查"
result="$(request GET /health -)"
expect_code "GET /health 回應正常" 200 "$(code_of "$result")"

# 本腳本第 21 節會刻意打滿登入限流配額（每分鐘每 IP）。
# 連續重跑時配額可能尚未回補，先等待再開始，否則前面的登入會誤報失敗。
# 限流測試（第 21 節）會打滿此來源 IP 的登入配額，使腳本無法立即重跑。
# 因此預設跳過，需要時以 VERIFY_RATE_LIMIT=1 明確開啟。
RUN_RATE_LIMIT_TEST="${VERIFY_RATE_LIMIT:-0}"

step "1. 未登入必須被拒（驗收標準 1）"
for path in /v1/dashboard /v1/applications /v1/auth/me; do
  result="$(request GET "$path" "$ANON_JAR")"
  expect_code "未登入 GET $path 被拒" 401 "$(code_of "$result")"
done
result="$(request GET /v1/admin/applications "$ANON_JAR")"
expect_code "未登入 GET /v1/admin/applications 被拒" 401 "$(code_of "$result")"

step "2. 公開端點仍可匿名存取"
# /v1/listings 是新的真實市集（標的來自已核准申請）
result="$(request GET /v1/listings "$ANON_JAR")"
expect_code "匿名可讀募資市集" 200 "$(code_of "$result")"
is_array="$(json_get "$(body_of "$result")" 'isinstance(d, list)')"
[[ "$is_array" == "True" ]] && ok "市集回傳陣列" || bad "市集回應格式非陣列"

# 舊的 market_listings 端點仍存在但已停用所有列（migration 005 標記為 legacy）
result="$(request GET /v1/market/listings "$ANON_JAR")"
expect_code "legacy 市集端點仍可存取" 200 "$(code_of "$result")"
legacy_count="$(json_get "$(body_of "$result")" 'len(d)')"
[[ "${legacy_count:-x}" == "0" ]] && ok "legacy 靜態標的已停用（0 筆）" || bad "legacy 仍有 ${legacy_count:-?} 筆啟用中"

step "3. 登入流程"
result="$(request POST /v1/auth/login "$BORROWER_JAR" '{"email":"demo@creditflow.test","password":"wrong-password"}')"
expect_code "錯誤密碼被拒" 401 "$(code_of "$result")"

result="$(request POST /v1/auth/login "$BORROWER_JAR" '{"email":"demo@creditflow.test","password":"demo1234"}')"
expect_code "借款人登入成功" 200 "$(code_of "$result")"
borrower_role="$(json_get "$(body_of "$result")" 'd["role"]')"
[[ "$borrower_role" == "borrower" ]] && ok "角色為 borrower" || bad "角色為 ${borrower_role:-?}，預期 borrower"

if grep -q 'creditflow_session' "$BORROWER_JAR"; then
  ok "session cookie 已下發"
else
  bad "session cookie 未下發（proxy 可能沒轉發 Set-Cookie）"
fi

result="$(request POST /v1/auth/login "$REVIEWER_JAR" '{"email":"reviewer@creditflow.test","password":"review1234"}')"
expect_code "風控員登入成功" 200 "$(code_of "$result")"
reviewer_role="$(json_get "$(body_of "$result")" 'd["role"]')"
[[ "$reviewer_role" == "reviewer" ]] && ok "角色為 reviewer" || bad "角色為 ${reviewer_role:-?}，預期 reviewer"

step "4. 角色授權隔離（驗收標準 2）"
result="$(request GET /v1/admin/applications "$BORROWER_JAR")"
expect_code "借款人存取風控後台被拒" 403 "$(code_of "$result")"
result="$(request GET /v1/admin/applications "$REVIEWER_JAR")"
expect_code "風控員可存取風控後台" 200 "$(code_of "$result")"

step "5. 借款人送出申請 → 風控端看得到（驗收標準 3）"
APPLY_BODY='{"product":"個人信用貸款","amount":600000,"termMonths":36,"purpose":"債務整合",
"applicantName":"端到端測試","idNumber":"A123456789","phone":"0900-000-000",
"email":"demo@creditflow.test","job":"上市櫃公司員工","employmentYears":"3~5 年",
"annualIncome":1200000,"monthlyExpenses":30000,"housing":"租屋","note":"verify-flow",
"documents":["身分證正反面.jpg"]}'
result="$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")"
expect_code "申請送出成功" 201 "$(code_of "$result")"
APP_ID="$(json_get "$(body_of "$result")" 'd["id"]')"
if [[ -n "${APP_ID:-}" ]]; then
  ok "取得案件編號 $APP_ID"
else
  bad "未取得案件編號，後續驗證中止"
  printf '\n通過 %d / 失敗 %d\n' "$PASS" "$FAIL"; exit 1
fi

result="$(request GET /v1/applications "$BORROWER_JAR")"
mine="$(json_get "$(body_of "$result")" "len([x for x in d['items'] if x['id']=='$APP_ID'])")"
[[ "${mine:-0}" == "1" ]] && ok "借款人在自己的申請列表看到 $APP_ID" || bad "借款人查不到自己的申請"

result="$(request GET /v1/admin/applications "$REVIEWER_JAR")"
seen="$(json_get "$(body_of "$result")" "len([x for x in d['items'] if x['id']=='$APP_ID'])")"
[[ "${seen:-0}" == "1" ]] && ok "風控端在待審清單看到同一筆 $APP_ID" || bad "風控端看不到該申請（前後台仍未接通）"

dbr="$(json_get "$(body_of "$result")" "[x['dbr'] for x in d['items'] if x['id']=='$APP_ID'][0]")"
rec="$(json_get "$(body_of "$result")" "[x['recommendation'] for x in d['items'] if x['id']=='$APP_ID'][0]")"
[[ -n "${dbr:-}" ]] && ok "後端已試算 DBR=${dbr}% 建議「${rec}」" || bad "後端未提供 DBR 試算"

step "6. 申請驗證規則"
result="$(request POST /v1/applications "$BORROWER_JAR" '{"product":"信貸","amount":10000,"termMonths":36,"purpose":"x","applicantName":"a","email":"a@b.c","annualIncome":100000,"monthlyExpenses":0}')"
expect_code "金額低於下限被拒" 422 "$(code_of "$result")"
result="$(request POST /v1/applications "$BORROWER_JAR" '{"product":"信貸","amount":500000,"termMonths":30,"purpose":"x","applicantName":"a","email":"a@b.c","annualIncome":100000,"monthlyExpenses":0}')"
expect_code "非白名單期數被拒" 422 "$(code_of "$result")"

step "7. 風控核准 → 上架募資標的（驗收標準 4a）"
result="$(request PATCH "/v1/admin/applications/$APP_ID" "$REVIEWER_JAR" '{"action":"approve","reason":"verify-flow 自動測試"}')"
expect_code "核准成功" 200 "$(code_of "$result")"
APPROVE_BODY="$(body_of "$result")"
LISTING_ID="$(json_get "$APPROVE_BODY" 'd["listing"]["id"]')"
TARGET_AMOUNT="$(json_get "$APPROVE_BODY" 'd["listing"]["targetAmount"]')"
[[ -n "${LISTING_ID:-}" ]] && ok "核准後上架標的 $LISTING_ID（目標 $TARGET_AMOUNT）" || bad "核准後未上架標的"

# 核准本身不得直接發約：資金必須先到位
no_loan="$(json_get "$APPROVE_BODY" 'd.get("loan") is None')"
[[ "$no_loan" == "True" ]] && ok "核准不直接發約（待募資完成）" || bad "核准直接生成了合約"

status_after="$(json_get "$APPROVE_BODY" 'd["status"]')"
[[ "$status_after" == "funding" ]] && ok "申請狀態轉為 funding" || bad "申請狀態為 ${status_after:-?}"

result="$(request PATCH "/v1/admin/applications/$APP_ID" "$REVIEWER_JAR" '{"action":"approve"}')"
expect_code "重複核准被拒" 409 "$(code_of "$result")"

step "7b. 出借人投標 → 募滿撥款（驗收標準 4b）"
INVESTOR_JAR="$WORK/investor.cookies"; : >"$INVESTOR_JAR"
result="$(request POST /v1/auth/login "$INVESTOR_JAR" '{"email":"investor@creditflow.test","password":"invest1234"}')"
expect_code "出借人登入成功" 200 "$(code_of "$result")"
investor_role="$(json_get "$(body_of "$result")" 'd["role"]')"
[[ "$investor_role" == "investor" ]] && ok "角色為 investor" || bad "角色為 ${investor_role:-?}"

result="$(request GET /v1/listings "$INVESTOR_JAR")"
expect_code "讀取募資市集" 200 "$(code_of "$result")"
listed="$(json_get "$(body_of "$result")" "len([x for x in d if x['id']=='$LISTING_ID'])")"
[[ "${listed:-0}" == "1" ]] && ok "標的出現在市集" || bad "標的未出現在市集"

# 借款人與風控員都不得投標
result="$(request POST "/v1/listings/$LISTING_ID/invest" "$BORROWER_JAR" '{"amount":10000,"requestId":"borrower-try"}')"
expect_code "借款人無法投標" 403 "$(code_of "$result")"
result="$(request POST "/v1/listings/$LISTING_ID/invest" "$REVIEWER_JAR" '{"amount":10000,"requestId":"reviewer-try"}')"
expect_code "風控員無法投標" 403 "$(code_of "$result")"

# 先確保餘額足夠
request POST /v1/investments/top-up "$INVESTOR_JAR" "{\"amount\":$TARGET_AMOUNT}" >/dev/null

result="$(request POST "/v1/listings/$LISTING_ID/invest" "$INVESTOR_JAR" '{"amount":1000,"requestId":"vf-partial"}')"
expect_code "部分投標成功" 201 "$(code_of "$result")"
partial="$(json_get "$(body_of "$result")" 'd["listing"]["fundedAmount"]')"
[[ "${partial:-0}" == "1000" ]] && ok "募集金額累計為 $partial" || bad "募集金額為 ${partial:-?}"
not_funded="$(json_get "$(body_of "$result")" 'd["fullyFunded"]')"
[[ "$not_funded" == "False" ]] && ok "尚未募滿，未撥款" || bad "部分投標就觸發撥款"

result="$(request POST "/v1/listings/$LISTING_ID/invest" "$INVESTOR_JAR" '{"amount":1000,"requestId":"vf-partial"}')"
expect_code "重複 requestId 被拒（冪等）" 409 "$(code_of "$result")"

# 一次投入超過剩餘額度：後端只接受剩餘部分，不可超募
result="$(request POST "/v1/listings/$LISTING_ID/invest" "$INVESTOR_JAR" "{\"amount\":$TARGET_AMOUNT,\"requestId\":\"vf-final\"}")"
expect_code "投滿標的成功" 201 "$(code_of "$result")"
FINAL_BODY="$(body_of "$result")"
accepted="$(json_get "$FINAL_BODY" 'd["investment"]["amount"]')"
expected=$(( TARGET_AMOUNT - 1000 ))
[[ "${accepted:-0}" == "$expected" ]] && ok "超額投標被裁切為剩餘額度 $accepted" || bad "接受金額 ${accepted:-?}，預期 $expected"

fully="$(json_get "$FINAL_BODY" 'd["fullyFunded"]')"
[[ "$fully" == "True" ]] && ok "標的已募滿" || bad "fullyFunded = ${fully:-?}"
LOAN_ID="$(json_get "$FINAL_BODY" 'd["disbursedLoan"]["id"]')"
[[ -n "${LOAN_ID:-}" ]] && ok "募滿自動撥款，合約 $LOAN_ID" || bad "募滿後未撥款"

result="$(request POST "/v1/listings/$LISTING_ID/invest" "$INVESTOR_JAR" '{"amount":1000,"requestId":"vf-late"}')"
expect_code "已募滿標的拒絕投標" 409 "$(code_of "$result")"

result="$(request GET /v1/investments "$INVESTOR_JAR")"
expect_code "讀取出借人持倉" 200 "$(code_of "$result")"
# 累計投入包含 seed 資料與前次執行的殘留，故只斷言「至少涵蓋本次投入」
invested="$(json_get "$(body_of "$result")" 'd["totalInvested"]')"
mine="$(json_get "$(body_of "$result")" "sum(x['amount'] for x in d['investments'] if x['listingId']=='$LISTING_ID')")"
[[ "${mine:-0}" == "$TARGET_AMOUNT" ]] && ok "本標的投入合計 $mine（持倉總額 $invested）" \
  || bad "本標的投入 ${mine:-?}，預期 $TARGET_AMOUNT"
est="$(json_get "$(body_of "$result")" 'd["estimatedReturn"]')"
[[ "${est:-0}" -gt 0 ]] && ok "預估收益 $est" || bad "未計算預估收益"

result="$(request GET /v1/investments "$BORROWER_JAR")"
expect_code "借款人無法讀取持倉" 403 "$(code_of "$result")"

result="$(request GET /v1/dashboard "$BORROWER_JAR")"
expect_code "借款人讀取儀表板" 200 "$(code_of "$result")"
DASH="$(body_of "$result")"
has_loan="$(json_get "$DASH" "len([x for x in d['loans'] if x['id']=='$LOAN_ID'])")"
[[ "${has_loan:-0}" == "1" ]] && ok "新合約出現在借款人儀表板" || bad "新合約未出現在儀表板"

monthly="$(json_get "$DASH" "[x['monthlyPayment'] for x in d['loans'] if x['id']=='$LOAN_ID'][0]")"
[[ "${monthly:-0}" -gt 0 ]] && ok "合約月付金 = $monthly（非寫死常數）" || bad "合約月付金為 0"

summary_monthly="$(json_get "$DASH" 'd["summary"]["monthlyPayment"]')"
summary_total="$(json_get "$DASH" 'd["summary"]["totalBorrowed"]')"
if [[ "${summary_monthly:-0}" != "14982" && "${summary_monthly:-0}" -gt 0 ]]; then
  ok "摘要月付金 $summary_monthly 為真實聚合（不再是寫死的 14982）"
else
  bad "摘要月付金仍為 ${summary_monthly:-?}"
fi
ok "摘要借款總額 $summary_total"

step "8. 攤還表正確性（驗收標準 5）"
result="$(request GET "/v1/loans/$LOAN_ID/schedule" "$BORROWER_JAR")"
expect_code "讀取攤還明細" 200 "$(code_of "$result")"
SCHED="$(body_of "$result")"
rows="$(json_get "$SCHED" 'len(d["schedule"])')"
[[ "${rows:-0}" == "36" ]] && ok "攤還表 36 期，與申請期數一致" || bad "攤還表 ${rows:-?} 期，預期 36"

principal_sum="$(json_get "$SCHED" 'sum(r["principal"] for r in d["schedule"])')"
[[ "${principal_sum:-0}" == "600000" ]] && ok "本金加總 $principal_sum = 核貸金額（零誤差）" || bad "本金加總 ${principal_sum:-?}，預期 600000"

final_balance="$(json_get "$SCHED" 'd["schedule"][-1]["remainingBalance"]')"
[[ "${final_balance:-x}" == "0" ]] && ok "末期剩餘本金歸零" || bad "末期剩餘本金 ${final_balance:-?}，預期 0"

consistent="$(json_get "$SCHED" 'all(r["amountDue"]==r["principal"]+r["interest"] for r in d["schedule"])')"
[[ "$consistent" == "True" ]] && ok "每期應繳 = 本金 + 利息" || bad "有期數的應繳金額與本息不符"

first_status="$(json_get "$SCHED" 'd["schedule"][0]["status"]')"
[[ "$first_status" == "due" ]] && ok "第一期標記為待繳" || bad "第一期狀態為 ${first_status:-?}，預期 due"

step "8b. 還款分潤（資金閉環）"
# 記下分潤前的出借人餘額
before_balance="$(json_get "$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")" 'd["balance"]')"

# 取第一期的應繳金額與本息拆分
SCHED="$(body_of "$(request GET "/v1/loans/$LOAN_ID/schedule" "$BORROWER_JAR")")"
due_amount="$(json_get "$SCHED" '[r["amountDue"] for r in d["schedule"] if r["status"]=="due"][0]')"
due_no="$(json_get "$SCHED" '[r["installmentNo"] for r in d["schedule"] if r["status"]=="due"][0]')"

result="$(request POST "/v1/loans/$LOAN_ID/installments/$due_no/pay" "$BORROWER_JAR" '{}')"
expect_code "借款人繳納第 $due_no 期" 201 "$(code_of "$result")"

# 本標的由單一出借人全額出資，故其餘額增加應等於該期實收金額
after_balance="$(json_get "$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")" 'd["balance"]')"
gained=$(( after_balance - before_balance ))
[[ "$gained" == "$due_amount" ]] && ok "出借人餘額增加 $gained，等於借款人繳款金額" \
  || bad "餘額增加 $gained，預期 $due_amount（金額未守恆）"

# 注意：totalPrincipalReturned 是跨所有投資的累計（含前次執行殘留），
# 因此這裡驗證「本合約本期的分潤」而非累計值。
DIST="$(body_of "$(request GET /v1/investments/distributions "$INVESTOR_JAR")")"
this_principal="$(json_get "$DIST" "sum(x['principal'] for x in d if x['loanId']=='$LOAN_ID')")"
this_interest="$(json_get "$DIST" "sum(x['interest'] for x in d if x['loanId']=='$LOAN_ID')")"
[[ "${this_principal:-0}" -gt 0 ]] && ok "本期分得本金 $this_principal" || bad "未分到本金"
[[ "${this_interest:-0}" -gt 0 ]] && ok "本期分得利息 $this_interest" || bad "未分到利息"
[[ $(( this_principal + this_interest )) == "$due_amount" ]] \
  && ok "本金加利息 = 該期實收（零誤差）" \
  || bad "本金 $this_principal + 利息 $this_interest 不等於 $due_amount"

PORTFOLIO="$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")"
returned="$(json_get "$PORTFOLIO" 'd["totalPrincipalReturned"]')"
[[ "${returned:-0}" -gt 0 ]] && ok "持倉累計已收本金 $returned" || bad "持倉未回報已收本金"

outstanding="$(json_get "$PORTFOLIO" 'd["outstandingPrincipal"]')"
invested_total="$(json_get "$PORTFOLIO" 'd["totalInvested"]')"
[[ -n "${outstanding:-}" ]] && ok "待收本金 $outstanding（累計投入 $invested_total）" || bad "未回報待收本金"

dist_total="$(json_get "$DIST" "sum(x['total'] for x in d if x['loanId']=='$LOAN_ID')")"
[[ "${dist_total:-0}" == "$due_amount" ]] && ok "收款明細合計 $dist_total 與實收一致" \
  || bad "收款明細 ${dist_total:-?}，預期 $due_amount"

result="$(request GET /v1/investments/distributions "$BORROWER_JAR")"
expect_code "借款人無法讀取收款明細" 403 "$(code_of "$result")"

step "8c. 檔案上傳（真實檔案，經 Nuxt proxy）"
# 建立一筆 pending 申請作為上傳對象（已核准的申請會鎖定附件）
DOC_APP="$(json_get "$(body_of "$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")")" 'd["id"]')"
[[ -n "${DOC_APP:-}" ]] && ok "建立文件測試申請 $DOC_APP" || bad "無法建立申請"

DOC_FILE="$WORK/sample.pdf"
printf '%%PDF-1.7\nverify-flow sample document\n' > "$DOC_FILE"

# upload_file <path> <cookie-jar> <file> <filename> → "狀態碼<TAB>回應主體"
upload_file() {
  local path="$1" jar="$2" file="$3" filename="$4"
  ensure_csrf "$jar"
  local token code
  token="$(csrf_token_from "$jar")"
  code="$(curl -s -m 30 -o "$WORK/body" -w '%{http_code}' -X POST "$API$path" \
    -b "$jar" -c "$jar" -H "X-CSRF-Token: $token" \
    -F "file=@${file};filename=${filename}")"
  printf '%s\t%s' "$code" "$(cat "$WORK/body")"
}

result="$(upload_file "/v1/applications/$DOC_APP/documents" "$BORROWER_JAR" "$DOC_FILE" "測試文件.pdf")"
expect_code "上傳 PDF 成功" 201 "$(code_of "$result")"
DOC_ID="$(json_get "$(body_of "$result")" 'd["id"]')"
doc_type="$(json_get "$(body_of "$result")" 'd["contentType"]')"
[[ "$doc_type" == "application/pdf" ]] && ok "型別由內容判定為 $doc_type" || bad "型別為 ${doc_type:-?}"

result="$(request GET "/v1/applications/$DOC_APP/documents" "$BORROWER_JAR")"
expect_code "列出申請文件" 200 "$(code_of "$result")"
doc_count="$(json_get "$(body_of "$result")" 'len(d)')"
[[ "${doc_count:-0}" == "1" ]] && ok "文件清單有 1 筆" || bad "文件清單 ${doc_count:-?} 筆"

# 下載並比對內容
DOWNLOADED="$WORK/downloaded.pdf"
dl_code="$(curl -s -m 30 -b "$BORROWER_JAR" -o "$DOWNLOADED" -D "$WORK/dl-headers" \
  -w '%{http_code}' "$API/v1/documents/$DOC_ID/download")"
expect_code "下載文件" 200 "$dl_code"
if cmp -s "$DOC_FILE" "$DOWNLOADED"; then
  ok "下載內容與上傳完全一致（byte-for-byte）"
else
  bad "下載內容與上傳不符"
fi

# 非 ASCII 檔名必須以 RFC 5987 編碼，否則瀏覽器會顯示亂碼
disposition="$(grep -i '^content-disposition:' "$WORK/dl-headers" | head -1)"
if grep -q "filename\*=UTF-8''" <<<"$disposition"; then
  ok "中文檔名以 RFC 5987 編碼"
else
  bad "Content-Disposition 缺少 filename* 編碼：$disposition"
fi
grep -qi 'nosniff' "$WORK/dl-headers" && ok "回應帶 X-Content-Type-Options: nosniff" \
  || bad "下載回應缺少 nosniff"

# 副檔名可偽造，只有內容說得準
FAKE_FILE="$WORK/fake.pdf"
printf 'this is plain text, not a PDF\n' > "$FAKE_FILE"
result="$(upload_file "/v1/applications/$DOC_APP/documents" "$BORROWER_JAR" "$FAKE_FILE" "fake.pdf")"
expect_code "偽造副檔名被拒" 422 "$(code_of "$result")"

# 相同內容不可重複上傳
result="$(upload_file "/v1/applications/$DOC_APP/documents" "$BORROWER_JAR" "$DOC_FILE" "duplicate.pdf")"
expect_code "重複內容被拒" 409 "$(code_of "$result")"

# 風控員可讀（審核需要）但不可改
result="$(request GET "/v1/applications/$DOC_APP/documents" "$REVIEWER_JAR")"
expect_code "風控員可列出文件" 200 "$(code_of "$result")"
dl_code="$(curl -s -m 30 -b "$REVIEWER_JAR" -o /dev/null -w '%{http_code}' \
  "$API/v1/documents/$DOC_ID/download")"
expect_code "風控員可下載文件" 200 "$dl_code"
result="$(upload_file "/v1/applications/$DOC_APP/documents" "$REVIEWER_JAR" "$DOC_FILE" "reviewer.pdf")"
expect_code "風控員不可上傳（不得改動審核依據）" 409 "$(code_of "$result")"

# 出借人與匿名者都不該讀得到
dl_code="$(curl -s -m 30 -b "$INVESTOR_JAR" -o /dev/null -w '%{http_code}' \
  "$API/v1/documents/$DOC_ID/download")"
expect_code "出借人無法下載他人文件" 404 "$dl_code"
dl_code="$(curl -s -m 30 -o /dev/null -w '%{http_code}' "$API/v1/documents/$DOC_ID/download")"
expect_code "未登入無法下載" 401 "$dl_code"

step "8c-2. 文件 OCR 辨識"
# 產生一張含申請人資料的圖片，驗證 OCR 真的擷取得到文字
OCR_IMAGE="$WORK/ocr-id.png"
if python3 -c "
from PIL import Image, ImageDraw, ImageFont
img = Image.new('L', (1000, 240), 255)
d = ImageDraw.Draw(img)
f = ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf', 44)
d.text((40, 40), 'Name: VERIFY FLOW TEST', font=f, fill=0)
d.text((40, 130), 'ID No: A123456789', font=f, fill=0)
img.save('$OCR_IMAGE')
" 2>/dev/null; then
  OCR_APP="$(json_get "$(body_of "$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")")" 'd["id"]')"
  result="$(upload_file "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR" "$OCR_IMAGE" "id-card.png")"
  expect_code "上傳含文字的圖片" 201 "$(code_of "$result")"

  ocr_initial="$(json_get "$(body_of "$result")" 'd["ocrStatus"]')"
  [[ "$ocr_initial" == "pending" ]] && ok "上傳立即回應，OCR 狀態為 pending（未阻塞）" \
    || bad "上傳後 ocrStatus 為 ${ocr_initial:-?}，預期 pending"

  # 背景 worker 每 5 秒取件，最多等 60 秒
  ocr_final=""
  for attempt in $(seq 1 12); do
    sleep 5
    ocr_final="$(json_get "$(body_of "$(request GET "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR")")" \
      'd[0]["ocrStatus"]')"
    [[ "$ocr_final" == "done" || "$ocr_final" == "failed" || "$ocr_final" == "skipped" ]] && break
  done

  case "$ocr_final" in
    done)
      ok "OCR 於背景完成辨識"
      DOC_BODY="$(body_of "$(request GET "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR")")"
      id_result="$(json_get "$DOC_BODY" 'd[0]["verifications"]["id_number"]')"
      [[ "$id_result" == "matched" ]] && ok "身分證字號與文件內容相符" \
        || bad "身分證比對結果為 ${id_result:-?}，預期 matched"
      ;;
    skipped)
      printf '  \033[33m!\033[0m OCR 工具不可用，文件標記為 skipped（此環境未安裝 tesseract）\n'
      ;;
    *)
      bad "OCR 狀態為 ${ocr_final:-逾時未完成}"
      ;;
  esac

  # 與申請人無關的文件應回報 not_found，而非誤判為相符
  OTHER_IMAGE="$WORK/ocr-other.png"
  python3 -c "
from PIL import Image, ImageDraw, ImageFont
img = Image.new('L', (900, 200), 255)
d = ImageDraw.Draw(img)
f = ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf', 36)
d.text((40, 40), 'UNRELATED DOCUMENT', font=f, fill=0)
d.text((40, 110), 'ID: B987654321', font=f, fill=0)
img.save('$OTHER_IMAGE')
" 2>/dev/null
  result="$(upload_file "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR" "$OTHER_IMAGE" "other.png")"
  expect_code "上傳無關文件" 201 "$(code_of "$result")"

  if [[ "$ocr_final" == "done" ]]; then
    for attempt in $(seq 1 12); do
      sleep 5
      remaining="$(json_get "$(body_of "$(request GET "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR")")" \
        "len([x for x in d if x['ocrStatus'] in ('pending','processing')])")"
      [[ "${remaining:-1}" == "0" ]] && break
    done
    other_result="$(json_get "$(body_of "$(request GET "/v1/applications/$OCR_APP/documents" "$BORROWER_JAR")")" \
      "[x['verifications']['id_number'] for x in d if x['originalName']=='other.png'][0]")"
    [[ "$other_result" == "not_found" ]] && ok "無關文件正確回報 not_found（未誤判為相符）" \
      || bad "無關文件比對結果為 ${other_result:-?}，預期 not_found"
  fi
else
  printf '  \033[33m!\033[0m 跳過 OCR 驗證（本機缺少 PIL 或字型，無法產生測試圖片）\n'
fi

step "8c-3. 文件刪除"
# 刪除
result="$(request DELETE "/v1/documents/$DOC_ID" "$BORROWER_JAR")"
expect_code "刪除文件" 200 "$(code_of "$result")"
dl_code="$(curl -s -m 30 -b "$BORROWER_JAR" -o /dev/null -w '%{http_code}' \
  "$API/v1/documents/$DOC_ID/download")"
expect_code "刪除後無法下載" 404 "$dl_code"

step "8d. Proxy 透通性（排除清單而非 allowlist）"
# proxy 先前以 allowlist 逐項轉發 header，造成過五次靜默失效。
# 這一節鎖住「後端新增 header 無需改 proxy」這個性質。

PROXY_HEADERS="$WORK/proxy-headers"
curl -s -m 15 -o /dev/null -D "$PROXY_HEADERS" "$API/health"

# CORS header 由後端設定，proxy 的舊 allowlist 未涵蓋它們
if grep -qi '^access-control-allow-headers:' "$PROXY_HEADERS"; then
  ok "後端設定的 CORS header 已穿透 proxy"
else
  bad "CORS header 未穿透（proxy 仍在過濾未列舉的 header）"
fi

# hop-by-hop header 不該被轉發到瀏覽器（連線語意屬於單一跳）
if grep -qi '^transfer-encoding:' "$PROXY_HEADERS"; then
  bad "hop-by-hop header transfer-encoding 被轉發"
else
  ok "hop-by-hop header 未被轉發"
fi

# content-length 不轉發後端的值（$fetch 解壓後會不符），改由 Nitro 依實際 body 重算。
# 因此這裡驗證的是「值正確」而非「不存在」。
actual_length="$(curl -s -m 15 "$API/health" | wc -c | tr -d '[:space:]')"
proxy_length="$(grep -i '^content-length:' "$PROXY_HEADERS" | tr -d '[:space:]' | cut -d: -f2)"
if [[ -z "$proxy_length" ]]; then
  ok "content-length 未設定（由傳輸層決定）"
elif [[ "$proxy_length" == "$actual_length" ]]; then
  ok "content-length $proxy_length 與實際 body 長度一致"
else
  bad "content-length $proxy_length 與實際 body 長度 $actual_length 不符"
fi

# 三種內容型別都必須正確處理
json_type="$(grep -i '^content-type:' "$PROXY_HEADERS" | head -1)"
if grep -qi 'application/json' <<<"$json_type"; then
  ok "JSON 回應的 content-type 正確"
else
  bad "JSON 回應的 content-type 為 ${json_type:-?}"
fi

# 自訂 request header 應能到達後端：以後端會回送的 X-Request-ID 驗證
custom_id="verify-flow-$RANDOM"
echoed="$(curl -s -m 15 -o /dev/null -D - -H "X-Request-ID: $custom_id" "$API/health" \
  | grep -i '^x-request-id:' | tr -d '\r' | cut -d' ' -f2)"
[[ "$echoed" == "$custom_id" ]] && ok "自訂 request header 可達後端並回送" \
  || bad "X-Request-ID 回送為 ${echoed:-?}，預期 $custom_id"

step "8e. 收入最小化揭露與清單分頁"
result="$(request GET /v1/admin/applications "$REVIEWER_JAR")"
expect_code "讀取風控清單" 200 "$(code_of "$result")"
ADMIN_BODY="$(body_of "$result")"

# 收入與支出不可以原值出現在清單回應
leaked="$(json_get "$ADMIN_BODY" "'annualIncome' in str(d) or 'monthlyExpenses' in str(d)")"
[[ "$leaked" == "False" ]] && ok "清單回應不含收入原值欄位" || bad "清單回應仍含收入原值"

income_range="$(json_get "$ADMIN_BODY" "d['items'][0]['incomeRange']")"
[[ -n "${income_range:-}" && "$income_range" != "None" ]] && ok "改以級距揭露：$income_range" \
  || bad "未提供收入級距"

# DBR 仍須正確——隱藏原值不可讓風控判讀失真
dbr_ok="$(json_get "$ADMIN_BODY" "d['items'][0]['dbr'] > 0")"
[[ "$dbr_ok" == "True" ]] && ok "DBR 仍正確計算" || bad "DBR 計算失效"

# 完整金額只在稽核端點揭露
result="$(request POST "/v1/admin/applications/$APP_ID/reveal" "$REVIEWER_JAR" '{"reason":"verify-flow 收入覆核"}')"
expect_code "reveal 取得完整金額" 200 "$(code_of "$result")"
revealed_income="$(json_get "$(body_of "$result")" 'd["annualIncome"]')"
[[ "${revealed_income:-0}" == "1200000" ]] && ok "完整年收入 $revealed_income 僅於稽核端點揭露" \
  || bad "reveal 回傳年收入 ${revealed_income:-?}"

# 分頁
result="$(request GET "/v1/applications?limit=2" "$BORROWER_JAR")"
expect_code "分頁查詢" 200 "$(code_of "$result")"
PAGE_BODY="$(body_of "$result")"
page_len="$(json_get "$PAGE_BODY" "len(d['items'])")"
page_total="$(json_get "$PAGE_BODY" "d['page']['total']")"
[[ "${page_len:-0}" -le 2 ]] && ok "limit=2 回傳 $page_len 筆" || bad "limit 未生效，回傳 ${page_len:-?} 筆"
[[ "${page_total:-0}" -ge "${page_len:-0}" ]] && ok "總筆數 $page_total" || bad "總筆數異常"

# limit 超出上限須被夾住
capped="$(json_get "$(body_of "$(request GET '/v1/applications?limit=99999' "$BORROWER_JAR")")" "d['page']['limit']")"
[[ "${capped:-0}" == "200" ]] && ok "超大 limit 被夾到上限 $capped" || bad "limit 未夾住：${capped:-?}"

# 無效參數不該讓請求失敗
result="$(request GET "/v1/applications?limit=abc&offset=xyz" "$BORROWER_JAR")"
expect_code "無效分頁參數不致請求失敗" 200 "$(code_of "$result")"

# offset 翻頁不可重複或遺漏
if [[ "${page_total:-0}" -gt 2 ]]; then
  first_ids="$(json_get "$PAGE_BODY" "','.join(x['id'] for x in d['items'])")"
  second_ids="$(json_get "$(body_of "$(request GET '/v1/applications?limit=2&offset=2' "$BORROWER_JAR")")" "','.join(x['id'] for x in d['items'])")"
  overlap="$(python3 -c "
a=set('$first_ids'.split(','))
b=set('$second_ids'.split(','))
print(len(a & b))" 2>/dev/null)"
  [[ "${overlap:-1}" == "0" ]] && ok "相鄰頁面無重複項目" || bad "頁面間有 ${overlap:-?} 筆重複"
fi

step "8f. 募資期限與取消退款"
# 另建一筆標的專門測試取消，避免影響後續段落使用的 $LOAN_ID
CANCEL_APP="$(json_get "$(body_of "$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")")" 'd["id"]')"
CANCEL_BODY="$(body_of "$(request PATCH "/v1/admin/applications/$CANCEL_APP" "$REVIEWER_JAR" '{"action":"approve"}')")"
CANCEL_LISTING="$(json_get "$CANCEL_BODY" 'd["listing"]["id"]')"
[[ -n "${CANCEL_LISTING:-}" ]] && ok "建立測試標的 $CANCEL_LISTING" || bad "無法建立測試標的"

# 標的應帶有募資期限與剩餘天數
days="$(json_get "$(body_of "$(request GET /v1/listings "$INVESTOR_JAR")")" \
  "[x['daysRemaining'] for x in d if x['id']=='$CANCEL_LISTING'][0]")"
[[ "${days:-0}" == "14" ]] && ok "募資期限為 14 天（剩餘 $days 天）" || bad "剩餘天數為 ${days:-?}，預期 14"

# 部分投標後取消，確認全額退回
request POST /v1/investments/top-up "$INVESTOR_JAR" '{"amount":200000}' >/dev/null
before_balance="$(json_get "$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")" 'd["balance"]')"
result="$(request POST "/v1/listings/$CANCEL_LISTING/invest" "$INVESTOR_JAR" '{"amount":50000,"requestId":"vf-cancel"}')"
expect_code "部分投標成功" 201 "$(code_of "$result")"
after_invest="$(json_get "$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")" 'd["balance"]')"
[[ $(( before_balance - after_invest )) == "50000" ]] && ok "投標扣款 50000" || bad "扣款金額異常"

# 借款人與出借人都不可取消
result="$(request POST "/v1/admin/listings/$CANCEL_LISTING/cancel" "$BORROWER_JAR" '{"reason":"我不想借了"}')"
expect_code "借款人無法取消標的" 403 "$(code_of "$result")"
result="$(request POST "/v1/admin/listings/$CANCEL_LISTING/cancel" "$INVESTOR_JAR" '{"reason":"想拿回錢"}')"
expect_code "出借人無法取消標的" 403 "$(code_of "$result")"

result="$(request POST "/v1/admin/listings/$CANCEL_LISTING/cancel" "$REVIEWER_JAR" '{"reason":""}')"
expect_code "未填理由被拒" 422 "$(code_of "$result")"

result="$(request POST "/v1/admin/listings/$CANCEL_LISTING/cancel" "$REVIEWER_JAR" '{"reason":"verify-flow 取消測試"}')"
expect_code "風控員取消標的" 200 "$(code_of "$result")"
refunded="$(json_get "$(body_of "$result")" 'd["refundedAmount"]')"
[[ "${refunded:-0}" == "50000" ]] && ok "退款金額 $refunded 等於投入金額" || bad "退款 ${refunded:-?}，預期 50000"

final_balance="$(json_get "$(body_of "$(request GET /v1/investments "$INVESTOR_JAR")")" 'd["balance"]')"
[[ "$final_balance" == "$before_balance" ]] && ok "餘額完全回復（$final_balance）" \
  || bad "餘額為 $final_balance，預期回復為 $before_balance"

result="$(request POST "/v1/admin/listings/$CANCEL_LISTING/cancel" "$REVIEWER_JAR" '{"reason":"再取消一次"}')"
expect_code "重複取消被拒" 409 "$(code_of "$result")"

result="$(request POST "/v1/listings/$CANCEL_LISTING/invest" "$INVESTOR_JAR" '{"amount":10000,"requestId":"vf-after-cancel"}')"
expect_code "已取消標的拒絕投標" 409 "$(code_of "$result")"

# 取消後申請應回到可重新處理的狀態，而非被婉拒
app_status="$(json_get "$(body_of "$(request GET /v1/admin/applications "$REVIEWER_JAR")")" \
  "[x['status'] for x in d['items'] if x['id']=='$CANCEL_APP'][0]")"
[[ "$app_status" == "more_info_required" ]] && ok "取消後申請回到待補件，可重新處理" \
  || bad "申請狀態為 ${app_status:-?}，預期 more_info_required"

step "8g. Email 驗證與密碼重設（經 MailHog 實際收信）"
MAILHOG="http://localhost:${MAILHOG_HTTP_PORT:-18025}"

if curl -s -m 5 -o /dev/null "$MAILHOG/api/v2/messages" 2>/dev/null; then
  curl -s -m 10 -X DELETE "$MAILHOG/api/v1/messages" >/dev/null 2>&1

  # 以獨立帳號測試，避免影響其他段落使用的 demo 帳號
  MAIL_USER="mailflow-$RANDOM@creditflow.test"
  MAIL_JAR="$WORK/mailflow.cookies"; : >"$MAIL_JAR"

  result="$(request POST /v1/auth/register "$MAIL_JAR" \
    "{\"email\":\"$MAIL_USER\",\"password\":\"a-good-password\",\"displayName\":\"信件測試\"}")"
  expect_code "註冊新帳號" 201 "$(code_of "$result")"
  verified_initial="$(json_get "$(body_of "$result")" 'd["emailVerified"]')"
  [[ "$verified_initial" == "False" ]] && ok "新帳號初始為未驗證" || bad "新帳號 emailVerified = ${verified_initial:-?}"

  # 未驗證不可送出申請
  result="$(request POST /v1/applications "$MAIL_JAR" "$APPLY_BODY")"
  expect_code "未驗證信箱無法送出申請" 403 "$(code_of "$result")"

  sleep 2
  VERIFY_TOKEN="$(curl -s -m 10 "$MAILHOG/api/v2/messages" | python3 -c "
import json,sys,re
d=json.load(sys.stdin)
for m in d.get('items', []):
    body = m['Content']['Body']
    found = re.search(r'verify-email\?token=([A-Za-z0-9_-]+)', body)
    if found:
        print(found.group(1)); break
" 2>/dev/null)"

  if [[ -n "${VERIFY_TOKEN:-}" ]]; then
    ok "MailHog 收到驗證信並取出 token"

    result="$(request POST /v1/auth/verify-email "$MAIL_JAR" "{\"token\":\"$VERIFY_TOKEN\"}")"
    expect_code "以信件 token 完成驗證" 200 "$(code_of "$result")"

    verified_after="$(json_get "$(body_of "$(request GET /v1/auth/me "$MAIL_JAR")")" 'd["emailVerified"]')"
    [[ "$verified_after" == "True" ]] && ok "驗證後狀態更新為已驗證" || bad "驗證後仍為未驗證"

    result="$(request POST /v1/applications "$MAIL_JAR" "$APPLY_BODY")"
    expect_code "驗證後可送出申請" 201 "$(code_of "$result")"

    result="$(request POST /v1/auth/verify-email "$MAIL_JAR" "{\"token\":\"$VERIFY_TOKEN\"}")"
    expect_code "驗證 token 不可重用" 422 "$(code_of "$result")"
  else
    bad "MailHog 未收到驗證信或無法取出 token"
  fi

  # 忘記密碼：對未註冊與已註冊的回應必須相同
  curl -s -m 10 -X DELETE "$MAILHOG/api/v1/messages" >/dev/null 2>&1
  missing_body="$(body_of "$(request POST /v1/auth/forgot-password "$WORK/anon2.cookies" \
    '{"email":"definitely-not-registered@creditflow.test"}')")"
  existing_body="$(body_of "$(request POST /v1/auth/forgot-password "$WORK/anon2.cookies" \
    "{\"email\":\"$MAIL_USER\"}")")"
  [[ "$missing_body" == "$existing_body" ]] && ok "忘記密碼不洩漏帳號是否存在（回應相同）" \
    || bad "回應不同，洩漏帳號存在性"

  sleep 2
  RESET_TOKEN="$(curl -s -m 10 "$MAILHOG/api/v2/messages" | python3 -c "
import json,sys,re
d=json.load(sys.stdin)
for m in d.get('items', []):
    found = re.search(r'reset-password\?token=([A-Za-z0-9_-]+)', m['Content']['Body'])
    if found:
        print(found.group(1)); break
" 2>/dev/null)"

  mail_count="$(curl -s -m 10 "$MAILHOG/api/v2/messages" | json_get "$(cat)" 'd["total"]' 2>/dev/null || \
    curl -s -m 10 "$MAILHOG/api/v2/messages" | python3 -c 'import json,sys;print(json.load(sys.stdin)["total"])')"
  [[ "${mail_count:-0}" == "1" ]] && ok "只為已註冊的信箱寄出重設信（共 $mail_count 封）" \
    || bad "寄出 ${mail_count:-?} 封信，預期 1"

  if [[ -n "${RESET_TOKEN:-}" ]]; then
    ok "MailHog 收到重設信並取出 token"

    # 確認舊 session 在重設前仍有效
    result="$(request GET /v1/auth/me "$MAIL_JAR")"
    expect_code "重設前舊 session 有效" 200 "$(code_of "$result")"

    result="$(request POST /v1/auth/reset-password "$WORK/anon2.cookies" \
      "{\"token\":\"$RESET_TOKEN\",\"password\":\"a-completely-new-password\"}")"
    expect_code "以信件 token 重設密碼" 200 "$(code_of "$result")"

    # 重設必須撤銷所有既有 session
    result="$(request GET /v1/auth/me "$MAIL_JAR")"
    expect_code "重設後舊 session 被撤銷" 401 "$(code_of "$result")"

    result="$(request POST /v1/auth/login "$WORK/old.cookies" \
      "{\"email\":\"$MAIL_USER\",\"password\":\"a-good-password\"}")"
    expect_code "舊密碼失效" 401 "$(code_of "$result")"

    result="$(request POST /v1/auth/login "$WORK/new.cookies" \
      "{\"email\":\"$MAIL_USER\",\"password\":\"a-completely-new-password\"}")"
    expect_code "新密碼可登入" 200 "$(code_of "$result")"

    result="$(request POST /v1/auth/reset-password "$WORK/anon2.cookies" \
      "{\"token\":\"$RESET_TOKEN\",\"password\":\"yet-another-password\"}")"
    expect_code "重設 token 不可重用" 422 "$(code_of "$result")"
  else
    bad "MailHog 未收到重設信或無法取出 token"
  fi
else
  printf '  \033[33m!\033[0m 跳過信件流程驗證（MailHog 未在 %s 運作）\n' "$MAILHOG"
fi

step "8h. 登入失敗鎖定（帳號維度）"
# 以獨立帳號測試，避免鎖定 demo 帳號影響其他段落
LOCK_USER="lockout-$RANDOM@creditflow.test"
LOCK_JAR="$WORK/lockout.cookies"; : >"$LOCK_JAR"
result="$(request POST /v1/auth/register "$LOCK_JAR" \
  "{\"email\":\"$LOCK_USER\",\"password\":\"a-good-password\",\"displayName\":\"鎖定測試\"}")"
expect_code "建立測試帳號" 201 "$(code_of "$result")"

# 連續輸錯直到觸發鎖定（門檻為 5 次）
lock_code=""
lock_retry=""
for attempt in $(seq 1 8); do
  code="$(curl -s -m 20 -o "$WORK/lockbody" -D "$WORK/lockheaders" -w '%{http_code}' \
    -X POST "$API/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$LOCK_USER\",\"password\":\"wrong-password\"}")"
  if [[ "$code" == "429" ]]; then
    lock_code="$code"
    lock_retry="$(grep -i '^retry-after:' "$WORK/lockheaders" | head -1 | cut -d: -f2- | tr -d '[:space:]')"
    ok "第 $attempt 次失敗後帳號被鎖定（HTTP 429）"
    break
  fi
  [[ "$code" == "401" ]] || bad "第 $attempt 次失敗回應為 $code，預期 401"
done
[[ "$lock_code" == "429" ]] || bad "連續 8 次失敗未觸發帳號鎖定"
[[ -n "${lock_retry:-}" ]] && ok "回應帶 Retry-After: $lock_retry 秒" || bad "429 未帶 Retry-After"

# 鎖定期間即使密碼正確也必須被拒，否則猜中密碼時鎖定就形同虛設
code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$API/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$LOCK_USER\",\"password\":\"a-good-password\"}")"
expect_code "鎖定期間正確密碼也被拒" 429 "$code"

# 不存在的帳號不會被鎖定（沒有紀錄可累加），因此不成為帳號列舉管道
missing_code=""
for attempt in $(seq 1 8); do
  missing_code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$API/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"email":"definitely-no-such-account@creditflow.test","password":"wrong"}')"
done
expect_code "不存在的帳號始終回 401（不因鎖定機制而不同）" 401 "$missing_code"

# 密碼重設應解除鎖定
if curl -s -m 5 -o /dev/null "$MAILHOG/api/v2/messages" 2>/dev/null; then
  curl -s -m 10 -X DELETE "$MAILHOG/api/v1/messages" >/dev/null 2>&1
  request POST /v1/auth/forgot-password "$WORK/anon3.cookies" "{\"email\":\"$LOCK_USER\"}" >/dev/null
  sleep 2
  LOCK_RESET_TOKEN="$(curl -s -m 10 "$MAILHOG/api/v2/messages" | python3 -c "
import json,sys,re
d=json.load(sys.stdin)
for m in d.get('items', []):
    found = re.search(r'reset-password\?token=([A-Za-z0-9_-]+)', m['Content']['Body'])
    if found:
        print(found.group(1)); break
" 2>/dev/null)"

  if [[ -n "${LOCK_RESET_TOKEN:-}" ]]; then
    result="$(request POST /v1/auth/reset-password "$WORK/anon3.cookies" \
      "{\"token\":\"$LOCK_RESET_TOKEN\",\"password\":\"unlocked-password\"}")"
    expect_code "以重設密碼解除鎖定" 200 "$(code_of "$result")"

    code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$API/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"$LOCK_USER\",\"password\":\"unlocked-password\"}")"
    expect_code "重設後可立即登入（鎖定已解除）" 200 "$code"
  else
    bad "未取得重設 token，無法驗證解除鎖定"
  fi
else
  printf '  \033[33m!\033[0m 跳過解除鎖定驗證（MailHog 未運作）\n'
fi

step "9. 跨使用者資料隔離"
result="$(request GET "/v1/loans/$LOAN_ID/schedule" "$REVIEWER_JAR")"
expect_code "他人合約回 404（不洩漏存在性）" 404 "$(code_of "$result")"

step "10. 註冊與登出"
NEW_EMAIL="verify-$(date +%s)@creditflow.test"
NEW_JAR="$WORK/new.cookies"; : >"$NEW_JAR"
result="$(request POST /v1/auth/register "$NEW_JAR" "{\"email\":\"$NEW_EMAIL\",\"password\":\"verify1234\",\"displayName\":\"新註冊測試\"}")"
expect_code "註冊成功" 201 "$(code_of "$result")"
new_role="$(json_get "$(body_of "$result")" 'd["role"]')"
[[ "$new_role" == "borrower" ]] && ok "自助註冊固定為 borrower（無法自封 reviewer）" || bad "註冊角色為 ${new_role:-?}"

result="$(request GET /v1/admin/applications "$NEW_JAR")"
expect_code "新註冊者無法存取風控後台" 403 "$(code_of "$result")"

result="$(request GET /v1/dashboard "$NEW_JAR")"
new_loans="$(json_get "$(body_of "$result")" 'len(d["loans"])')"
[[ "${new_loans:-x}" == "0" ]] && ok "新使用者看不到他人合約" || bad "新使用者看到 ${new_loans:-?} 筆合約，預期 0"

result="$(request POST /v1/auth/register "$NEW_JAR" "{\"email\":\"$NEW_EMAIL\",\"password\":\"verify1234\",\"displayName\":\"重複\"}")"
expect_code "重複 email 被拒" 409 "$(code_of "$result")"

result="$(request POST /v1/auth/register "$WORK/x.cookies" '{"email":"short@creditflow.test","password":"1234","displayName":"短密碼"}')"
expect_code "密碼過短被拒" 422 "$(code_of "$result")"

result="$(request POST /v1/auth/logout "$NEW_JAR" '{}')"
expect_code "登出成功" 200 "$(code_of "$result")"
result="$(request GET /v1/auth/me "$NEW_JAR")"
expect_code "登出後 session 失效" 401 "$(code_of "$result")"

step "11. 補件與婉拒路徑"
result="$(request POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")"
APP2="$(json_get "$(body_of "$result")" 'd["id"]')"
result="$(request PATCH "/v1/admin/applications/$APP2" "$REVIEWER_JAR" '{"action":"request_more_info","reason":"請補營業稅單"}')"
expect_code "要求補件成功" 200 "$(code_of "$result")"
st="$(json_get "$(body_of "$result")" 'd["status"]')"
[[ "$st" == "more_info_required" ]] && ok "狀態轉為 more_info_required" || bad "狀態為 ${st:-?}"
result="$(request PATCH "/v1/admin/applications/$APP2" "$REVIEWER_JAR" '{"action":"reject","reason":"逾期未補件"}')"
expect_code "補件後仍可婉拒（非終局狀態）" 200 "$(code_of "$result")"

result="$(request PATCH "/v1/admin/applications/LN-9999-999999" "$REVIEWER_JAR" '{"action":"approve"}')"
expect_code "不存在的案件回 404" 404 "$(code_of "$result")"
result="$(request PATCH "/v1/admin/applications/$APP_ID" "$REVIEWER_JAR" '{"action":"bogus"}')"
expect_code "非法動作被拒" 422 "$(code_of "$result")"

step "12. 還款引擎：單期繳款（驗收標準 1-3）"
# 第 8b 節已繳過第一期，這裡處理下一期
SCHED="$(body_of "$(request GET "/v1/loans/$LOAN_ID/schedule" "$BORROWER_JAR")")"
due_no="$(json_get "$SCHED" '[r["installmentNo"] for r in d["schedule"] if r["status"]=="due"][0]')"
[[ -n "${due_no:-}" ]] && ok "找到待繳期數：第 $due_no 期" || bad "合約沒有任何待繳期數"

# 未到期期數不得先繳
future_no="$(json_get "$SCHED" '[r["installmentNo"] for r in d["schedule"] if r["status"]=="scheduled"][0]')"
result="$(request POST "/v1/loans/$LOAN_ID/installments/$future_no/pay" "$BORROWER_JAR" '{}')"
expect_code "未到期期數拒繳（第 $future_no 期）" 422 "$(code_of "$result")"

result="$(request POST "/v1/loans/$LOAN_ID/installments/$due_no/pay" "$BORROWER_JAR" '{}')"
expect_code "繳納第 $due_no 期成功" 201 "$(code_of "$result")"
paid_amount="$(json_get "$(body_of "$result")" 'd["repayment"]["amount"]')"
paid_count="$(json_get "$(body_of "$result")" 'd["loan"]["paidInstallments"]')"
[[ "${paid_count:-0}" == "2" ]] && ok "loans.paid_installments 推進為 2（含第 8b 節那期）" \
  || bad "paid_installments = ${paid_count:-?}，預期 2"
ok "入帳金額 $paid_amount"

# 冪等：重複繳同一期必須被擋
result="$(request POST "/v1/loans/$LOAN_ID/installments/$due_no/pay" "$BORROWER_JAR" '{}')"
expect_code "重複繳同一期被拒（冪等保護）" 409 "$(code_of "$result")"

result="$(request GET "/v1/loans/$LOAN_ID/repayments" "$BORROWER_JAR")"
expect_code "讀取繳款紀錄" 200 "$(code_of "$result")"
rep_count="$(json_get "$(body_of "$result")" 'len(d)')"
[[ "${rep_count:-0}" == "2" ]] && ok "繳款紀錄 2 筆（未重複落帳）" || bad "繳款紀錄 ${rep_count:-?} 筆，預期 2"

# 繳畢後下一期應自動轉為待繳
SCHED="$(body_of "$(request GET "/v1/loans/$LOAN_ID/schedule" "$BORROWER_JAR")")"
paid_status="$(json_get "$SCHED" "[r['status'] for r in d['schedule'] if r['installmentNo']==$due_no][0]")"
[[ "$paid_status" == "paid" ]] && ok "第 $due_no 期狀態轉為 paid" || bad "第 $due_no 期狀態為 ${paid_status:-?}"
next_due="$(json_get "$SCHED" 'len([r for r in d["schedule"] if r["status"]=="due"])')"
[[ "${next_due:-0}" == "1" ]] && ok "下一期自動轉為待繳" || bad "待繳期數為 ${next_due:-?}，預期 1"

step "13. 跨使用者還款隔離"
result="$(request POST "/v1/loans/$LOAN_ID/installments/2/pay" "$REVIEWER_JAR" '{}')"
expect_code "他人合約無法代繳（回 404）" 404 "$(code_of "$result")"
result="$(request GET "/v1/loans/$LOAN_ID/repayments" "$REVIEWER_JAR")"
expect_code "他人繳款紀錄不可讀（回 404）" 404 "$(code_of "$result")"

step "14. 提前清償（驗收標準 5）"
result="$(request POST "/v1/loans/$LOAN_ID/settle" "$BORROWER_JAR" '{}')"
expect_code "提前清償成功" 201 "$(code_of "$result")"
SETTLE="$(body_of "$result")"
settled_flag="$(json_get "$SETTLE" 'd["settled"]')"
[[ "$settled_flag" == "True" ]] && ok "合約標記為已結清" || bad "settled = ${settled_flag:-?}"
loan_status="$(json_get "$SETTLE" 'd["loan"]["status"]')"
[[ "$loan_status" == "已結清" ]] && ok "合約狀態轉為「已結清」" || bad "合約狀態為 ${loan_status:-?}"
closed="$(json_get "$SETTLE" 'd["closedCount"]')"
waived="$(json_get "$SETTLE" 'd["waivedInterest"]')"
ok "一次結清 $closed 期，免除未到期利息 $waived"

full="$(json_get "$SETTLE" 'd["loan"]["paidInstallments"] == d["loan"]["totalInstallments"]')"
[[ "$full" == "True" ]] && ok "已繳期數 = 總期數" || bad "已繳期數未等於總期數"

SCHED="$(body_of "$(request GET "/v1/loans/$LOAN_ID/schedule" "$BORROWER_JAR")")"
unpaid="$(json_get "$SCHED" 'len([r for r in d["schedule"] if r["status"] != "paid"])')"
[[ "${unpaid:-x}" == "0" ]] && ok "攤還表已無未繳期數" || bad "仍有 ${unpaid:-?} 期未繳"

result="$(request POST "/v1/loans/$LOAN_ID/settle" "$BORROWER_JAR" '{}')"
expect_code "重複清償被拒" 409 "$(code_of "$result")"
result="$(request POST "/v1/loans/$LOAN_ID/installments/3/pay" "$BORROWER_JAR" '{}')"
expect_code "已結清合約無法再繳款" 409 "$(code_of "$result")"

result="$(request GET "/v1/loans/$LOAN_ID/repayments" "$BORROWER_JAR")"
# 用集合比對而非序列：單期繳款的筆數會隨前面的測試段落變動
kinds="$(json_get "$(body_of "$result")" '",".join(sorted(set(r["kind"] for r in d)))')"
prepay_count="$(json_get "$(body_of "$result")" 'len([r for r in d if r["kind"]=="prepayment"])')"
[[ "$kinds" == "installment,prepayment" ]] && ok "繳款紀錄含單期繳款與提前清償兩種類型" \
  || bad "繳款紀錄類型為 ${kinds:-?}"
[[ "${prepay_count:-0}" == "1" ]] && ok "提前清償僅一筆" || bad "提前清償 ${prepay_count:-?} 筆，預期 1"

step "15. 逾期判定與催收清單（驗收標準 6）"
# 新建一筆已撥款的合約，把首期繳款日改到過去以觸發逾期判定
LOAN3="$(disburse_new_loan overdue)"
[[ -n "${LOAN3:-}" ]] && ok "建立測試合約 $LOAN3（走完募資撥款流程）" || bad "無法建立測試合約"

if [[ -n "${LOAN3:-}" ]] && command -v docker >/dev/null 2>&1; then
  COMPOSE=(docker compose --env-file docker/.env -f docker/docker-compose.yml -f docker/docker-compose.develop.yml)
  # 把第 1 期繳款日推到 45 天前 → 應判定為 M2
  "${COMPOSE[@]}" exec -T db psql -U creditflow -d creditflow -q -c \
    "UPDATE loan_installments SET due_date = CURRENT_DATE - 45 WHERE loan_id = '$LOAN3' AND installment_no = 1;" \
    >/dev/null 2>&1

  result="$(request GET /v1/admin/overdue "$REVIEWER_JAR")"
  expect_code "風控員讀取催收清單" 200 "$(code_of "$result")"
  OVERDUE="$(body_of "$result")"
  found="$(json_get "$OVERDUE" "len([x for x in d if x['loanId']=='$LOAN3'])")"
  [[ "${found:-0}" -ge 1 ]] && ok "逾期期數出現在催收清單" || bad "催收清單未收錄該逾期期數"
  days="$(json_get "$OVERDUE" "[x['overdueDays'] for x in d if x['loanId']=='$LOAN3'][0]")"
  stage="$(json_get "$OVERDUE" "[x['stage'] for x in d if x['loanId']=='$LOAN3'][0]")"
  [[ "${days:-0}" == "45" ]] && ok "逾期天數正確計為 45 天" || bad "逾期天數為 ${days:-?}，預期 45"
  [[ "$stage" == "M2" ]] && ok "催收階段判定為 M2" || bad "催收階段為 ${stage:-?}，預期 M2"

  # 逾期期數可以繳（狀態 overdue 亦屬可繳）
  result="$(request POST "/v1/loans/$LOAN3/installments/1/pay" "$BORROWER_JAR" '{}')"
  expect_code "逾期期數可補繳" 201 "$(code_of "$result")"

  result="$(request GET /v1/admin/overdue "$REVIEWER_JAR")"
  gone="$(json_get "$(body_of "$result")" "len([x for x in d if x['loanId']=='$LOAN3' and x['installmentNo']==1])")"
  [[ "${gone:-x}" == "0" ]] && ok "補繳後該期離開催收清單" || bad "補繳後該期仍在催收清單"

  step "16. 還款率真實計算（驗收標準 7）"
  DASH="$(body_of "$(request GET /v1/dashboard "$BORROWER_JAR")")"
  rate="$(json_get "$DASH" 'd["summary"]["repaymentRate"]')"
  [[ -n "${rate:-}" ]] && ok "正常還款率 = ${rate}%（依已到期期數計算）" || bad "無法取得還款率"
else
  printf '  \033[33m!\033[0m 跳過逾期判定驗證（需要 docker 直接操作資料庫）\n'
fi

result="$(request GET /v1/admin/overdue "$BORROWER_JAR")"
expect_code "借款人無法讀取催收清單" 403 "$(code_of "$result")"

step "17. 路由邊界"
result="$(request GET "/v1/loans/$LOAN_ID/bogus" "$BORROWER_JAR")"
expect_code "未知子路由回 404" 404 "$(code_of "$result")"
result="$(request POST "/v1/loans/$LOAN_ID/installments/abc/pay" "$BORROWER_JAR" '{}')"
expect_code "非數字期數回 404" 404 "$(code_of "$result")"
result="$(request POST "/v1/loans/LN-0000-000000/installments/1/pay" "$BORROWER_JAR" '{}')"
expect_code "不存在的合約回 404" 404 "$(code_of "$result")"

step "18. CSRF 防護（驗收標準 4）"
result="$(request GET /v1/auth/csrf "$ANON_JAR")"
expect_code "可取得 CSRF token" 200 "$(code_of "$result")"
token="$(json_get "$(body_of "$result")" 'd["csrfToken"]')"
[[ -n "${token:-}" ]] && ok "token 已下發（長度 ${#token}）" || bad "未取得 CSRF token"

if grep -q 'creditflow_csrf' "$ANON_JAR"; then
  ok "CSRF cookie 已寫入 jar"
else
  bad "CSRF cookie 未下發"
fi

# 缺 header 的變更請求必須被擋
result="$(request_no_csrf POST /v1/applications "$BORROWER_JAR" "$APPLY_BODY")"
expect_code "POST 缺 CSRF header 被拒" 403 "$(code_of "$result")"
result="$(request_no_csrf PATCH "/v1/admin/applications/$APP_ID" "$REVIEWER_JAR" '{"action":"approve"}')"
expect_code "PATCH 缺 CSRF header 被拒" 403 "$(code_of "$result")"
result="$(request_no_csrf POST /v1/auth/logout "$BORROWER_JAR" '{}')"
expect_code "登出缺 CSRF header 被拒" 403 "$(code_of "$result")"

# token 不符也要被擋
BAD_JAR="$WORK/badcsrf.cookies"
cp "$BORROWER_JAR" "$BAD_JAR"
code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$API/v1/applications" \
  -b "$BAD_JAR" -H 'Content-Type: application/json' \
  -H 'X-CSRF-Token: totally-wrong-token' -d "$APPLY_BODY")"
expect_code "CSRF token 不符被拒" 403 "$code"

# GET 不需 token
result="$(request_no_csrf GET /v1/dashboard "$BORROWER_JAR")"
expect_code "GET 無需 CSRF token" 200 "$(code_of "$result")"

# 登入／註冊為豁免端點（尚無 session，CSRF 無實質意義）
EXEMPT_JAR="$WORK/exempt.cookies"; : >"$EXEMPT_JAR"
code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$API/v1/auth/login" \
  -c "$EXEMPT_JAR" -H 'Content-Type: application/json' \
  -d '{"email":"demo@creditflow.test","password":"demo1234"}')"
expect_code "登入為 CSRF 豁免端點" 200 "$code"

step "19. PII 加密（驗收標準 1-2）"
if command -v docker >/dev/null 2>&1; then
  COMPOSE=(docker compose --env-file docker/.env -f docker/docker-compose.yml -f docker/docker-compose.develop.yml)

  # 資料庫中不得再有明文身分證
  plaintext="$("${COMPOSE[@]}" exec -T db psql -U creditflow -d creditflow -tA -c \
    "SELECT COUNT(*) FROM applications WHERE id_number <> '' OR phone <> '';" 2>/dev/null | tr -d '[:space:]')"
  [[ "${plaintext:-x}" == "0" ]] && ok "資料庫已無明文 PII（0 筆）" || bad "仍有 ${plaintext:-?} 筆明文 PII"

  # 加密欄位必須有值，且不得包含可辨識的明文片段
  encrypted="$("${COMPOSE[@]}" exec -T db psql -U creditflow -d creditflow -tA -c \
    "SELECT COUNT(*) FROM applications WHERE id_number_enc IS NOT NULL;" 2>/dev/null | tr -d '[:space:]')"
  [[ "${encrypted:-0}" -ge 1 ]] && ok "加密欄位已填入（$encrypted 筆）" || bad "加密欄位為空"

  leaked="$("${COMPOSE[@]}" exec -T db psql -U creditflow -d creditflow -tA -c \
    "SELECT COUNT(*) FROM applications WHERE position('A123456789' in COALESCE(encode(id_number_enc,'escape'),'')) > 0;" 2>/dev/null | tr -d '[:space:]')"
  [[ "${leaked:-x}" == "0" ]] && ok "密文中不含明文片段" || bad "密文疑似未實際加密"
else
  printf '  \033[33m!\033[0m 跳過資料庫層 PII 驗證（需要 docker）\n'
fi

# API 回應必須是遮罩形式
result="$(request GET /v1/applications "$BORROWER_JAR")"
expect_code "讀取自己的申請" 200 "$(code_of "$result")"
masked="$(json_get "$(body_of "$result")" 'd["items"][0]["idNumberMasked"]')"
if [[ "$masked" == *"****"* && "$masked" != "A123456789" ]]; then
  ok "身分證以遮罩回傳（$masked）"
else
  bad "身分證未遮罩：${masked:-?}"
fi
phone_masked="$(json_get "$(body_of "$result")" 'd["items"][0]["phoneMasked"]')"
[[ "$phone_masked" == *"****"* ]] && ok "電話以遮罩回傳（$phone_masked）" || bad "電話未遮罩：${phone_masked:-?}"

raw_leak="$(json_get "$(body_of "$result")" '"A123456789" in str(d)')"
[[ "$raw_leak" == "False" ]] && ok "回應內容不含完整身分證" || bad "回應洩漏完整身分證"

step "20. PII 稽核揭露"
result="$(request POST "/v1/admin/applications/$APP_ID/reveal" "$REVIEWER_JAR" '{"reason":"verify-flow 稽核測試"}')"
expect_code "風控員可依理由取得完整 PII" 200 "$(code_of "$result")"
revealed="$(json_get "$(body_of "$result")" 'd["idNumber"]')"
[[ "$revealed" == "A123456789" ]] && ok "解密後取得正確原值" || bad "解密結果為 ${revealed:-?}"

result="$(request POST "/v1/admin/applications/$APP_ID/reveal" "$REVIEWER_JAR" '{"reason":""}')"
expect_code "未填理由被拒" 422 "$(code_of "$result")"
result="$(request POST "/v1/admin/applications/$APP_ID/reveal" "$BORROWER_JAR" '{"reason":"試圖越權"}')"
expect_code "借款人無法揭露 PII" 403 "$(code_of "$result")"

if command -v docker >/dev/null 2>&1; then
  logged="$("${COMPOSE[@]}" exec -T db psql -U creditflow -d creditflow -tA -c \
    "SELECT COUNT(*) FROM pii_access_log WHERE application_id = '$APP_ID';" 2>/dev/null | tr -d '[:space:]')"
  [[ "${logged:-0}" -ge 1 ]] && ok "揭露已寫入稽核紀錄（$logged 筆）" || bad "稽核紀錄未寫入"
fi

step "21. 登入限流（驗收標準 3）"
if [[ "$RUN_RATE_LIMIT_TEST" != "1" ]]; then
  printf '  \033[33m!\033[0m 已跳過：本測試會打滿登入配額，使腳本無法立即重跑\n'
  printf '     需要驗證時執行：VERIFY_RATE_LIMIT=1 %s\n' "$0"
else
# 注意：無法用偽造的 X-Forwarded-For 隔離配額——proxy 只轉發它實際觀察到的
# 對端位址（附加在鏈尾），後端取最後一跳，因此偽造的前綴會被忽略。
RL_JAR="$WORK/ratelimit.cookies"; : >"$RL_JAR"
got_429=0
retry_after=""
for attempt in $(seq 1 80); do
  code="$(curl -s -m 20 -o /dev/null -w '%{http_code}' -D "$WORK/headers" -X POST "$API/v1/auth/login" \
    -c "$RL_JAR" -H 'Content-Type: application/json' \
    -d '{"email":"nobody@creditflow.test","password":"wrong-password"}')"
  if [[ "$code" == "429" ]]; then
    got_429=1
    retry_after="$(grep -i '^retry-after:' "$WORK/headers" | head -1 | cut -d: -f2- | tr -d '[:space:]')"
    ok "第 $attempt 次嘗試觸發限流（HTTP 429）"
    break
  fi
done
[[ "$got_429" == "1" ]] || bad "連續 80 次登入失敗未觸發限流"
[[ -n "${retry_after:-}" ]] && ok "回應帶 Retry-After: $retry_after" || bad "429 未帶 Retry-After header"
  printf '  \033[33m!\033[0m 本節已用盡此來源 IP 的登入配額，腳本需等約一分鐘才能重跑\n'
fi

printf '\n\033[1m═══ 結果：通過 %d ／ 失敗 %d ═══\033[0m\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
