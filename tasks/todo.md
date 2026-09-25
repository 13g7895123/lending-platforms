# 2026-09-22 認證授權 → 申請審核閉環 → 合約生成

## 目標
讓「借款人送出申請 → 風控人員在後台看到並審核 → 核准後自動生成貸款合約 → 借款人儀表板看到該合約」這條主流程第一次真正跑通，且全程受身分驗證與角色授權保護。

## 風險等級
**高**（touches auth + PII + 資料模型變更）
- 影響元件：backend/cmd/api（全部）、backend/migrations、frontend/pages、frontend/server/api
- 回滾策略：migration 採新檔 002_*.sql（純 additive，不改動既有三表欄位語意）；程式碼以單一 commit 範圍，`git revert` 即可
- 基線捕捉：改動前 `GET /v1/dashboard`、`GET /v1/market/listings` 回應 shape 需保持相容（前端 fallback 仰賴）

## Checkpoint A — 認證授權
- [x] A1 migration 002：users 表（角色 borrower/reviewer）、sessions 表、applications 加 user_id、loans 加 user_id
- [x] A2 密碼雜湊（golang.org/x/crypto/bcrypt，已在 go.sum 的 indirect 依賴中）
- [x] A3 POST /v1/auth/register、POST /v1/auth/login、POST /v1/auth/logout、GET /v1/auth/me
- [x] A4 session cookie（HttpOnly + SameSite=Lax + Secure by env）
- [x] A5 authMiddleware（requireAuth / requireRole）掛上受保護路由
- [x] A6 /v1/dashboard 改為只回傳當前 user 的合約
- [x] A7 Nuxt proxy 轉發 cookie（目前 header allowlist 未含 cookie → 必修）
- [x] A8 seed 加入 demo 帳號（borrower / reviewer）

## Checkpoint B — 申請審核閉環
- [x] B1 POST /v1/applications 綁定當前 user_id（取代匿名寫入）
- [x] B2 GET /v1/applications（借款人：自己的；reviewer：全部 + status filter）
- [x] B3 GET /v1/admin/applications（reviewer only，待審列表，含 DBR 試算）
- [x] B4 PATCH /v1/admin/applications/{id}（approve / reject / request_more_info）
- [x] B5 application_reviews 稽核表（誰、何時、做了什麼、理由）
- [x] B6 前端 admin 頁改讀真 API，移除硬編碼 pendingApplications

## Checkpoint C — 合約生成
- [x] C1 核准時於同一 transaction 生成 loans 紀錄（PMT 計算月付金）
- [x] C2 loan_installments 表 + 攤還表生成（本息平均攤還）
- [x] C3 GET /v1/loans/{id}/schedule（攤還明細，取代前端硬算）
- [x] C4 dashboard summary 改為真實聚合（月付金 = 未結清合約 sum）
- [x] C5 前端 repay 頁改讀真攤還表

## Checkpoint D — 驗證
- [x] D1 Go 單元測試：PMT、攤還表加總、validateApplication、密碼雜湊
- [x] D2 Go 整合測試：auth 流程、授權拒絕、審核閉環（需 DB）
- [x] D3 go vet + go build
- [x] D4 nuxt typecheck
- [x] D5 端到端手動驗證腳本（curl 走完整條流程）

## 驗收標準
1. 未登入呼叫 /v1/dashboard → 401
2. borrower 呼叫 /v1/admin/applications → 403
3. borrower 送出申請 → reviewer 在 /v1/admin/applications 看得到同一筆 id
4. reviewer approve → loans 表出現對應合約，borrower dashboard 看得到
5. 該合約的 /v1/loans/{id}/schedule 期數 = 申請期數，本金加總 = 核貸金額（誤差 < 1 元）
6. 既有 /v1/market/listings 回應 shape 不變

## Working Notes
- PMT 公式已存在於 frontend index.vue:80，後端需對齊同一公式
- 既有 loans.id 格式 LN-YYYY-NNNNNN，沿用
- migration 無版本表，scripts/migrate.sh 逐檔套用，002 需可重複執行（IF NOT EXISTS）
- CHECK 約束需與 Go validate 保持一致

## Results（2026-09-22 完成）

### 變更範圍
新增：
- `backend/migrations/002_auth_and_workflow.sql`：users / sessions / application_reviews / loan_installments，
  applications 與 loans 加 user_id，loans 加 application_id 與 monthly_payment（全部 additive 且可重複執行）
- `backend/cmd/api/auth.go`：bcrypt 密碼、SHA-256 雜湊後才落地的 session、requireAuth / requireRole
- `backend/cmd/api/applications.go`：申請 CRUD、後端 DBR 試算、審核 transaction、合約生成
- `backend/cmd/api/loans.go`：真實 dashboard 聚合、攤還表查詢
- `backend/cmd/api/finance.go`：PMT 與攤還表生成（整數結算，末期吸收捨入殘差）
- `backend/cmd/api/*_test.go`：34 個測試案例
- `backend/seed/000_demo_users.sql`：demo 帳號
- `frontend/types/api.ts`、`frontend/composables/useAuth.ts`、`frontend/composables/useFormat.ts`
- `scripts/verify-flow.sh`：51 項端到端斷言

修改：
- `backend/cmd/api/main.go`：388 行單體拆為 5 個檔案，路由分公開／需登入／需 reviewer 三層
- `frontend/server/api/[...path].ts`：雙向轉發 cookie（原本 header allowlist 未含 cookie，不修認證會完全失效）
- `frontend/pages/index.vue`：加登入頁、admin 改讀真 API、repay 改讀真攤還表、移除所有硬編碼假資料
- `docker/docker-compose.yml` + env 樣板：加 SECURE_COOKIES

### 驗證結果
- `go test ./...` ok（34 案例）、`go vet ./...` 無輸出
- `npx nuxt typecheck` exit 0、`npx nuxt build` exit 0
- `./scripts/verify-flow.sh`：**51 通過 / 0 失敗**
- 六項驗收標準全數達成

### 過程中發現並修正的既有缺陷
1. **月付金魔術數字**：專案原本到處寫死 14982（前端 + seed），但 50萬/4.88%/36期
   的年金公式精確值是 14958.52。14982 不對應任何實際利率，已全部改為計算值。
   seed 另兩筆（12362 → 12355、11090 → 11091）亦修正。
2. **nullable user_id 導致後台清單 500**：migration 為向後相容讓 user_id 可為 NULL，
   但認證上線前送出的匿名申請會讓 `scanApplications` 的 string scan 失敗，
   整份後台清單崩潰。改用 `*string` 接收；孤兒申請另外禁止核准（無從判斷合約歸屬），
   只能婉拒或要求重新送出。已加迴歸測試。

### Risk & Rollback
- migration 002 純 additive，未改動既有三表欄位語意
- 既有 `GET /v1/market/listings` 回應 shape 經斷言確認未變
- 回滾：`git revert` 本次變更；資料庫端 002 建立的表可直接 DROP，不影響 001 的資料
- `docker/.env` 與 `docker/envs/.env.develop` 仍為 runtime 檔案，未進版控（僅 .example 在版控中）

---

# 2026-09-22 還款引擎（P1-4）

## 目標
讓攤還表的狀態真正會推進：借款人可還款、系統可判定逾期、結清可自動收尾。
做完後「正常還款率」「逾期案件」「提前清償」「催收」才有真實資料可依附。

## 風險等級
**高**（涉及金流語意與金額計算，且需嚴格 idempotency）
- 影響元件：backend/cmd/api（loans/repayments）、migrations 003、frontend repay 頁與 admin 頁
- 回滾策略：migration 003 純 additive；程式碼單一變更範圍可 git revert
- 基線捕捉：既有 /v1/loans/{id}/schedule 回應 shape 需保持相容

## 核心不變量（必須由測試守住）
1. 同一期不得重複扣款 → 需 idempotency（狀態機 + 唯一約束）
2. repayments 金額加總 == 該期 amount_due（部分繳款不在本次範圍，明確拒絕）
3. 全期繳畢 → loans.status 自動轉「已結清」、paid_installments == total_installments
4. 提前清償 → 剩餘各期一次標記 paid，記錄單筆 prepayment 交易
5. 逾期判定只影響 due_date < today 且未繳的期數，且不可重複累加

## Checkpoint A — 資料模型
- [x] A1 migration 003：repayments 交易表（含 idempotency_key 唯一約束）
- [x] A2 loan_installments 加 paid_amount，狀態機加註解
- [x] A3 loans 加 settled_at

## Checkpoint B — 還款 API
- [x] B1 POST /v1/loans/{id}/installments/{no}/pay（單期還款，transaction + FOR UPDATE）
- [x] B2 重複繳款回 409；未到期期數拒繳（只能繳 due/overdue）
- [x] B3 繳畢自動推進 loans.paid_installments 與 status
- [x] B4 POST /v1/loans/{id}/settle（提前清償，一次結清剩餘本金）
- [x] B5 GET /v1/loans/{id}/repayments（繳款紀錄）

## Checkpoint C — 逾期判定
- [x] C1 markOverdueInstallments：due_date 過期且未繳 → overdue
- [x] C2 啟動時執行一次 + 每日排程（沿用既有 startSessionCleanup 模式）
- [x] C3 逾期天數與催收階段（M1 <30d / M2 30-60d / M3+ >60d）計算函式
- [x] C4 GET /v1/admin/overdue（reviewer only，催收清單）

## Checkpoint D — 前端
- [x] D1 repay 頁：立即還款接真 API，繳款後重載
- [x] D2 repay 頁：提前清償接真 API
- [x] D3 repay 頁：繳款紀錄區塊
- [x] D4 admin 頁：恢復「逾期案件」tab，讀真 API
- [x] D5 dashboard：正常還款率有真實分母後驗證顯示

## Checkpoint E — 驗證
- [x] E1 單元測試：逾期天數/催收階段、狀態機轉換合法性
- [x] E2 verify-flow.sh 擴充：還款→結清→逾期完整路徑
- [x] E3 go test / vet / typecheck / build 全綠

## 驗收標準
1. 繳第一期 → 該期轉 paid，loans.paid_installments = 1
2. 重複繳同一期 → 409，不產生第二筆 repayments
3. 繳未到期期數 → 422
4. 繳完全部期數 → loans.status = '已結清'、settled_at 有值
5. 提前清償 → 剩餘期全轉 paid、狀態已結清、repayments 有一筆 prepayment
6. 人為把 due_date 改到過去 → 逾期判定後轉 overdue，催收清單看得到
7. dashboard 正常還款率 = paid / (paid + overdue) 真實計算
8. 既有 schedule 回應 shape 不變

## Results（還款引擎完成）

### 新增
- `backend/migrations/003_repayments.sql`：repayments 表（idempotency_key 唯一約束）、
  loan_installments 加 paid_amount/overdue_days、loans 加 settled_at
- `backend/cmd/api/repayments.go`：單期還款、提前清償、繳款紀錄、合約進度同步
- `backend/cmd/api/overdue.go`：逾期掃描（每日 + 啟動時）、催收清單
- `backend/cmd/api/collections.go`：逾期天數、M1/M2/M3+ 階段、冪等鍵生成
- `backend/cmd/api/collections_test.go`：23 個新測試案例

### 修改
- `main.go`：新增 dbExecutor 介面、loanRoutes 子路由分派、啟動 overdue sweep
- `loans.go`：loanSchedule 改由 loanRoutes 傳入已解析的 loanID
- `index.vue`：repay 頁接真還款 API + 繳款紀錄區塊、admin 恢復逾期 tab（讀真 API）
- `types/api.ts`：Repayment / PayResponse / SettleResponse / OverdueItem / installmentStatusMeta

### 驗證
- go test 99 subtest ok、go vet 無輸出、gofmt 乾淨
- nuxt typecheck exit 0、nuxt build exit 0
- verify-flow.sh：**84 通過 / 0 失敗**（原 51 → 新增 33 項）
- 八項驗收標準全數達成

### 關鍵設計決策
1. **冪等鍵而非狀態檢查**：單期還款用 `installment:{loanID}:{no}` 作唯一鍵，
   靠資料庫唯一約束擋重複，而非只靠讀取後判斷狀態（後者在併發下會漏）。
   同時保留狀態檢查作為快速路徑與明確錯誤訊息。
2. **逾期以 CURRENT_DATE 判定**：不用應用伺服器時間，避免容器時區造成 off-by-one。
3. **提前清償免除未到期利息**：只收剩餘本金，符合台灣消金實務；
   免除金額回傳於 waivedInterest 供前端顯示。
4. **只允許 due/overdue 還款**：未到期期數不可先繳，避免與提前清償語意重疊。

### 已知限制（已寫入 README）
- 未接金流閘道，還款只記錄交易不實際扣款
- 不支援部分繳款
- 逾期補繳後違約歷史不保留 → 還款率會回到 100%
- 催收清單無聯繫紀錄與委外流程

---

# 2026-09-22 P0 安全閘門：PII 加密 + Rate Limit + CSRF

## 目標
補上三個上線阻斷項。做完後 P0 清空。

## 風險等級
**高**（PII 加密涉及既有資料遷移；CSRF 可能擋掉正常請求）
- 回滾策略：加密採「新欄位並存」策略，不刪舊欄位，可隨時切回
- 基線：既有 API 回應需保持相容（除了 PII 改為遮罩）

## Checkpoint A — PII 加密
- [x] A1 決定方案：應用層 AES-256-GCM（金鑰由環境變數注入，不進版控）
- [x] A2 migration 004：applications 加 id_number_enc / phone_enc（bytea）
- [x] A3 crypto.go：加解密 + 金鑰載入 + 遮罩函式
- [x] A4 寫入路徑改為加密存放
- [x] A5 讀取路徑：借款人看遮罩、風控看遮罩（完整值需另開稽核端點，本次不做）
- [x] A6 資料遷移：既有明文搬進加密欄位並清空明文
- [x] A7 金鑰缺失時的行為：production 拒絕啟動，develop 用固定測試金鑰並警告

## Checkpoint B — Rate Limit
- [x] B1 記憶體 token bucket（單機夠用，多副本需 Redis，先記為限制）
- [x] B2 登入／註冊：依 IP 限流（防暴力破解）
- [x] B3 全域：依 IP 限流（防濫用）
- [x] B4 回 429 並帶 Retry-After

## Checkpoint C — CSRF
- [x] C1 SameSite 由 Lax 改 Strict（同源 proxy 架構下不影響正常使用）
- [x] C2 double-submit cookie token：非 GET 請求須帶 X-CSRF-Token
- [x] C3 Nuxt proxy 轉發該 header
- [x] C4 前端取得並附帶 token

## Checkpoint D — 驗證
- [x] D1 單元測試：加解密往返、遮罩、token bucket、CSRF 比對
- [x] D2 verify-flow 擴充：限流觸發 429、缺 CSRF token 被拒
- [x] D3 確認既有 84 項斷言不回歸

## 驗收標準
1. 資料庫中 id_number/phone 不再有明文
2. API 回應的身分證為遮罩格式（A12****789）
3. 連續登入失敗達門檻 → 429 + Retry-After
4. 非 GET 請求缺 CSRF token → 403
5. 既有 84 項驗證全數仍通過

## Results（P0 安全閘門完成）

### 新增
- `backend/migrations/004_pii_encryption.sql`：id_number_enc/phone_enc（bytea）、pii_access_log
- `backend/cmd/api/crypto.go`：AES-256-GCM 加解密、遮罩、金鑰載入、明文一次性搬遷
- `backend/cmd/api/ratelimit.go`：token bucket、clientIP（取 XFF 最後一跳）
- `backend/cmd/api/csrf.go`：double-submit cookie、token 輪替
- `backend/cmd/api/{crypto,ratelimit,csrf}_test.go`：54 個新測試案例
- `frontend/composables/useCsrf.ts`：統一處理變更狀態請求的 token 附帶

### 修改
- `main.go`：config 擴充、middleware 鏈（限流→CORS→CSRF→requestID）、啟動容忍欄位未就緒
- `auth.go`：SameSite Lax→Strict、登入/註冊後輪替 CSRF token、登出清 CSRF cookie
- `applications.go`：寫入改加密、讀取改遮罩、新增 reveal 稽核端點
- `scripts/_common.sh`：新增 sync_env_with_example
- `scripts/deploy.sh`：修正金鑰長度（33→32 bytes）、migration 後重啟 API
- `frontend/server/api/[...path].ts`：轉發 x-csrf-token 與 retry-after

### 驗證
- go test 153 subtest ok、go vet 無輸出、gofmt 乾淨
- nuxt typecheck exit 0、nuxt build exit 0
- verify-flow.sh：**108 通過 / 0 失敗**（原 84 → 新增 24 項）
- 五項驗收標準全數達成；資料庫 22 筆密文、0 筆明文

### 過程中發現並修正的四個缺陷
1. **develop fallback 金鑰長度錯誤**（31 bytes ≠ 32）→ API 啟動失敗。
   已加 TestDevelopFallbackPIIKeyIsValid 守住。
2. **新增的環境變數永遠不會進入既有 runtime env**：prepare_env_file 只在檔案不存在時複製。
   任何人加新環境變數都會踩到。已加 sync_env_with_example。
3. **deploy.sh 先啟容器後 migration** → API 啟動期的資料搬遷找不到欄位而進入重啟迴圈。
   已讓啟動容忍未就緒，並在 migration 後重啟 API 以在同一次部署內完成搬遷。
4. **Nuxt proxy 未轉發 Retry-After** → 429 的重試資訊在 proxy 層丟失。已補轉發。
5. **登入與註冊共用限流桶** → 正常註冊會擠掉登入配額。已改為分開計桶。

### Risk & Rollback
- migration 004 採「新欄位並存」，未 DROP 明文欄位，可回滾到舊版程式碼（會讀到空字串而非崩潰）
- 明文欄位已清空，回滾後舊版顯示空值而非洩漏
- 限流與 CSRF 可用環境變數調整，無需改碼

---

# 2026-09-22 整合測試 + CI（P2-8/9）

## 目標
為 DB 互動、middleware、transaction 建立可在 CI 執行的自動化測試，
作為後續前端大重構的安全網。

## 順序理由
原訂順序為「vue-router 拆頁 → 整合測試」，但拆 1200 行單檔沒有自動化保護風險過高，
故對調：先建安全網再重構。

## 風險等級
**低**（只新增測試與 CI 設定，不改動產品程式碼）

## Checkpoint A — 整合測試基礎設施
- [x] A1 決定方案：build tag 隔離（`//go:build integration`），用真實 PostgreSQL
- [x] A2 testMain：連線、套用 migration、每個測試前清空資料
- [x] A3 測試用 HTTP client：httptest.Server + cookie jar + CSRF 自動附帶
- [x] A4 helper：建立使用者、登入、送申請、核准

## Checkpoint B — 整合測試案例
- [x] B1 auth：註冊/登入/登出/session 失效/角色隔離
- [x] B2 middleware：401/403/CSRF 403/限流 429
- [x] B3 申請審核閉環：送出→後台可見→核准→合約生成
- [x] B4 transaction 原子性：核准失敗不留半套資料
- [x] B5 還款冪等：併發重複繳款只成立一次
- [x] B6 PII：加密落地、遮罩回傳、reveal 稽核
- [x] B7 逾期判定：改 due_date 後掃描結果正確

## Checkpoint C — CI
- [x] C1 .github/workflows/ci.yml
- [x] C2 job: backend（vet + fmt + 單元測試）
- [x] C3 job: integration（PostgreSQL service container + 整合測試）
- [x] C4 job: frontend（typecheck + build）
- [x] C5 Makefile 統一本機與 CI 的指令入口

## 驗收標準
1. `go test -tags=integration ./...` 在有 DB 時全綠
2. 無 DB 時整合測試被正確跳過（不是失敗）
3. CI 三個 job 定義完整且指令可在本機重現
4. 整合測試覆蓋所有 middleware 與 transaction 邊界
5. 既有 153 subtest 與 108 項 verify-flow 不回歸

## Results（整合測試 + CI 完成）

### 新增
- `backend/cmd/api/integration_main_test.go`：TestMain、migration 自動套用、resetDatabase
- `backend/cmd/api/integration_client_test.go`：帶 cookie jar 與自動 CSRF 的測試 client、fixture helper
- `backend/cmd/api/integration_auth_test.go`：認證與授權邊界
- `backend/cmd/api/integration_middleware_test.go`：CSRF、限流、request ID、路由邊界
- `backend/cmd/api/integration_workflow_test.go`：審核閉環、併發核准、跨使用者隔離
- `backend/cmd/api/integration_repayment_test.go`：還款、併發冪等、清償、逾期、還款率
- `backend/cmd/api/integration_pii_test.go`：加密落地、遮罩、reveal 稽核、明文搬遷
- `scripts/check.sh`：統一驗證入口（本機無 make 亦可用）
- `scripts/test-db.sh`：建立獨立測試資料庫
- `Makefile`：有 make 的環境可用
- `.github/workflows/ci.yml`：backend / integration / frontend / end-to-end 四個 job

### 驗證
- 單元測試 153 斷言（`-race` 通過）
- **整合測試 188 斷言**（`-race` 通過，82 秒）
- 端到端 108 斷言無回歸
- 無 DB 時整合測試正確跳過（exit 0，5 秒內放棄；CI 環境等 30 秒）

### 關鍵設計決策
1. **build tag 隔離**（`//go:build integration`）而非 testcontainers：
   不新增依賴，CI 用 service container，本機用既有 develop 的 PostgreSQL。
2. **獨立測試資料庫** `creditflow_test`：整合測試會 TRUNCATE 所有業務表，
   不可與開發資料共用。
3. **跳過而非失敗**：無 DB 的環境仍能跑單元測試。
   但 CI 額外加一步確認整合測試真的執行，避免綠燈卻沒驗證。
4. **測試 client 模擬瀏覽器**：cookie jar + 自動附帶 CSRF token，
   因此測試走的是與前端相同的完整 middleware 鏈，而非繞過。
5. **併發測試是重點**：5 並行核准、8 並行繳款，驗證 FOR UPDATE 與冪等鍵
   在真實併發下的正確性 —— 這是單元測試無法覆蓋的部分。

### 過程中發現的問題
- 我在 ci.yml 把 `steps:` 打成 `steps`（缺冒號），YAML 解析器抓到。
  已改為每次寫完 workflow 都用 PyYAML 驗證語法。
- 本機環境沒有 `make`，故主入口改為 `scripts/check.sh`，Makefile 保留給 CI 與其他環境。

---

# 2026-09-22 vue-router 拆頁（P2-12）

## 目標
把 1200+ 行的 index.vue 單體拆成多個路由頁面，解決三個實質缺陷：
1. 無法分享連結（所有頁面都是 /）
2. 無法用瀏覽器上一頁／下一頁
3. 重新整理會回到首頁

## 風險等級
**中**（大範圍前端重構，但有 108 項端到端驗證作為安全網）
- 回滾策略：單一變更範圍，git revert 即可
- 基線：verify-flow.sh 的 108 項須全數維持通過

## 路由設計
```
/            首頁（公開）
/login       登入／註冊（公開）
/dashboard   我的儀表板（需登入）
/apply       申請貸款（需登入）
/market      投資市集（公開）
/repay       還款管理（需登入）
/repay/:id   指定合約的還款管理（需登入，可分享）
/admin       風控後台（需 reviewer）
```

## Checkpoint A — 基礎設施
- [x] A1 layouts/default.vue：header、footer、nav、toast、modal 容器
- [x] A2 middleware/auth.ts：需登入頁面的路由守衛
- [x] A3 middleware/reviewer.ts：reviewer 專屬頁面守衛
- [x] A4 plugins：app 啟動時取 session 與 CSRF token
- [x] A5 composables/useToast.ts、useModal.ts：跨頁共用的通知與對話框

## Checkpoint B — 抽出共用元件
- [x] B1 components/AppHeader.vue、AppFooter.vue
- [x] B2 components/ToastMessage.vue、ModalDialog.vue
- [x] B3 components/LoanCalculator.vue（首頁試算機）
- [x] B4 components/StatusTag.vue（狀態標籤，多處重複）
- [x] B5 composables/useDashboard.ts、useApplications.ts、useLoans.ts、useAdmin.ts

## Checkpoint C — 拆頁
- [x] C1 pages/index.vue（首頁）
- [x] C2 pages/login.vue
- [x] C3 pages/dashboard.vue
- [x] C4 pages/apply.vue
- [x] C5 pages/market.vue
- [x] C6 pages/repay/index.vue + pages/repay/[id].vue
- [x] C7 pages/admin.vue

## Checkpoint D — 驗證
- [x] D1 typecheck + build
- [x] D2 verify-flow.sh 108 項不回歸
- [x] D3 手動確認：直接輸入 URL 可達、重新整理留在原頁、上一頁可用
- [x] D4 未登入存取受保護頁面 → 導向 /login

## 驗收標準
1. 每個頁面有獨立 URL，可直接輸入或分享
2. 重新整理停留在同一頁（不回首頁）
3. 瀏覽器上一頁／下一頁正常
4. 未登入存取 /dashboard → 導向 /login
5. borrower 存取 /admin → 拒絕
6. verify-flow.sh 108 項全數通過
7. 單一檔案不超過 350 行

## Results（拆頁完成）

### 成果
`index.vue` 1286 行 → 8 個頁面，最大 249 行：

| 檔案 | 行數 |
| --- | --- |
| pages/index.vue | 50 |
| pages/login.vue | 123 |
| pages/dashboard.vue | 163 |
| pages/apply.vue | 249 |
| pages/market.vue | 117 |
| pages/repay/index.vue | 56 |
| pages/repay/[id].vue | 224 |
| pages/admin.vue | 247 |

### 新增
- `layouts/default.vue`：導覽列、頁尾、toast 與對話框容器
- `middleware/auth.ts`、`middleware/reviewer.ts`：路由守衛
- `plugins/session.client.ts`：啟動時取 session 與 CSRF token
- `components/`：AppHeader、AppFooter、ToastMessage、ModalDialog、LoanCalculator、StatusTag
- `composables/`：useToast、useModal、useDashboard、useLoans、useMarket、useAdmin、useStatus、useApiFetch

### 驗證
- typecheck exit 0、build exit 0
- 端到端 108 項無回歸、整合測試無回歸
- 路由實測：7 個路徑各自可達；未登入 302 至 `/login?redirect=…`；
  borrower 存取 `/admin` 導向 `/dashboard`；reviewer 可進 `/admin`
- SSR 實測：`/admin` 在伺服器端就渲染出「待審件 (2)」，非空殼
- `/repay/<不存在的id>` 回 404，有效合約回 200

### 過程中發現並修正的兩個缺陷
1. **SSR 不會自動帶 cookie** → 路由守衛在伺服器端一律看不到 session，
   已登入者存取 `/dashboard` 被錯誤導向登入頁。
   建立 `useApiFetch.ts` 統一轉發，五個 composable 全部改用，避免日後遺漏。
2. **錯誤頁回 200** → `/repay/<不存在>` 顯示友善訊息但狀態碼是 200，
   監控與爬蟲會誤判為正常頁面。已在 SSR 階段設為 404。

### 設計決策
- **對話框以資料描述取代 slot**：ModalDialog 在 layout 中渲染，頁面無法傳 slot，
  故 ModalState 帶 `input` 設定與 `onConfirm(inputValue)` 回呼。
  好處是 ModalDialog 不需要知道任何業務邏輯，也不需集中的 kind switch。
- **`/repay` 導向第一筆合約**：讓它同時是列表頁與方便的入口。
- **跨頁共用狀態用 useState**：儀表板與還款頁共用同一份合約清單，繳款後只需重載一次。

---

# 2026-09-22 投標與撮合（P1-5）

## 目標
讓投資市集成為真實的 P2P 撮合：標的來自已核准的申請，出借人投標累積資金，
滿額後自動撥款生成合約。取代目前孤立的靜態 market_listings。

## 已確認的決策（使用者選擇）
1. **撥款模型**：核准後先募資 → 滿額才生成合約與攤還表
   （改變既有「核准即發約」行為，需調整既有測試與 verify-flow 斷言）
2. **角色**：註冊時可選 borrower 或 investor；reviewer 仍只能由 seed 建立

## 風險等級
**高**（改變核心流程的既有行為 + 涉及資金計算）
- 影響元件：審核流程、market_listings、合約生成、前端市集頁與儀表板
- 回滾策略：migration 005 純 additive；舊 market_listings 保留但標記為 legacy
- 基線：既有 108 項 verify-flow 中與「核准回傳 loan」相關的斷言需明確改寫

## 狀態機
```
applications: pending → approved（上架標的）→ funded → disbursed
                     ↘ rejected / more_info_required

listings:     funding → funded → disbursed
                     ↘ cancelled（逾期未募滿）
```

## Checkpoint A — 資料模型
- [x] A1 migration 005：users.role 加 investor
- [x] A2 listings 表（取代 market_listings，關聯 application_id）
- [x] A3 investments 表（出借人投標，含冪等鍵）
- [x] A4 investor_balances 表（出借人可用餘額，簡化為單一帳戶）
- [x] A5 applications.status 加 funding / funded

## Checkpoint B — 撮合引擎
- [x] B1 核准時建立 listing 而非直接發約
- [x] B2 POST /v1/listings/{id}/invest（投標，transaction + FOR UPDATE）
- [x] B3 超額投標處理：只接受剩餘額度，不可超募
- [x] B4 滿額自動撥款：生成 loans + 攤還表（沿用既有 createLoanFromApplication）
- [x] B5 GET /v1/listings（公開市集，來自真實申請）
- [x] B6 GET /v1/investments（出借人持倉）
- [x] B7 出借人餘額：入金端點（展示用）與投標扣款

## Checkpoint C — 前端
- [x] C1 註冊頁加角色選擇
- [x] C2 市集頁改讀 /v1/listings，投標接真 API
- [x] C3 出借人儀表板（持倉、收益、餘額）
- [x] C4 借款人儀表板顯示募資進度
- [x] C5 導覽列依角色顯示不同項目

## Checkpoint D — 驗證
- [x] D1 單元測試：投標金額計算、超額處理
- [x] D2 整合測試：完整撮合流程、併發投標不超募、滿額觸發撥款
- [x] D3 調整既有測試：核准不再直接回傳 loan
- [x] D4 verify-flow 改寫受影響斷言
- [x] D5 全部檢查通過

## 驗收標準
1. reviewer 核准 → 標的上架，applications.status = 'funding'，此時尚無 loan
2. investor 投標 → investments 有紀錄，listing.funded_amount 增加，餘額扣除
3. 併發投標不可超募：總投標額 ≤ 標的金額
4. 募滿 → 自動生成 loan 與攤還表，status = 'disbursed'
5. borrower 在儀表板看到募資進度，撥款後看到合約
6. investor 看到持倉與預估收益
7. 既有還款、逾期、PII 流程不受影響

## Results（撮合完成）

### 新增
- `backend/migrations/005_marketplace.sql`：users 加 investor 角色與 available_balance、
  listings 表（含 funded_amount <= target_amount 的 CHECK）、investments 表（冪等鍵）
- `backend/cmd/api/marketplace.go`：市集列表、投標、撥款、持倉、入金
- `backend/cmd/api/marketplace_test.go`：募資百分比、收益估算、冪等鍵
- `backend/cmd/api/integration_marketplace_test.go`：完整撮合流程、併發不超募、角色邊界
- `frontend/composables/useInvestor.ts`、`frontend/middleware/investor.ts`
- `frontend/pages/portfolio.vue`：出借人儀表板

### 修改
- 核准流程：`reviewTransitions["approve"]` 由 `approved` 改為 `funding`，
  改呼叫 `createListingFromApplication` 而非 `createLoanFromApplication`
- `auth.go`：investor 角色、註冊可選角色、user 加 availableBalance、optionalAuth
- `applications.go`：summary 加 listingId/fundedAmount/fundedPercent
- `market.vue`、`login.vue`、`dashboard.vue`、`AppHeader.vue`：接真撮合、角色選擇、募資進度
- seed：加 investor 帳號（預先入金 200 萬）與一筆募資中標的；移除 legacy 靜態標的

### 驗證
- 單元測試全綠、整合測試全綠（含 race）
- 端到端 **127 通過 / 0 失敗**，且可連續重跑三次皆通過
- 開啟限流測試時 130 項

### 關鍵設計決策
1. **三層防超募**：應用層裁切金額 + `FOR UPDATE` 鎖定 + 資料庫 CHECK 約束。
   8 個併發投標實測不超募，且只撥款一次。
2. **超額投標裁切而非拒絕**：對出借人較友善，且避免超募。
3. **撥款沿用既有 createLoanFromApplication**：攤還表計算只有一處實作。
4. **legacy 資料表保留但停用**：migration 005 標記 comment 並 UPDATE active=FALSE，
   可隨時回滾；seed 改為 DELETE 而非重新灌入。

### 過程中發現並修正的四個缺陷
1. **migration 重跑時 CHECK 約束衝突**：002 的 status/role CHECK 不含後續 migration
   新增的值，重跑 002 會被既有資料列違反。已放寬 002 的約束定義並加註說明。
2. **Nuxt proxy 未轉發真實來源 IP** → 後端限流把 proxy 後面的所有使用者
   視為同一來源，共用一份配額。已讓 proxy 把對端位址附加在 XFF 鏈尾。
3. **sync_env_with_example 只補新鍵，不更新既有鍵的值** →
   我改了 `.example` 的 `AUTH_RATE_LIMIT_PER_MINUTE` 三次（10→20→60），
   runtime env 始終是最初的 10，導致長時間誤判為「腳本設計問題」。
4. **限流測試讓端到端腳本無法重跑**：已改為 opt-in（`VERIFY_RATE_LIMIT=1`），
   develop 環境配額放寬，production 維持嚴格並加註代理注意事項。

### 既有測試的行為變更（10 個整合測試 + 2 個單元測試 + verify-flow 第 2/7 節）
安全網如預期抓到「核准不再直接發約」，已全部更新：
- `approveApplication` 回傳 listing 而非 loan
- 新增 `fundListingFully` 與 `approveAndDisburse` helper
- `TestIntegrationConcurrentApprovalCreatesOneLoan` → `...CreatesOneListing`
- verify-flow 新增 `disburse_new_loan` helper

---

# 2026-09-22 還款分潤（資金閉環）

## 目標
借款人還款時，按投資占比把本金與利息分配給出借人，讓資金真正流回出借端。
目前「預估收益」純為估算，出借人收不到任何錢。

## 為何優先於檔案上傳
撮合已讓資金流出（出借人扣款→借款人撥款），但沒有流回的路徑。
這是資金模型的邏輯缺口，比檔案上傳更有實質意義。

## 風險等級
**高**（涉及金額分配與整數捨入，分錯錢比不分錢更糟）
- 回滾策略：migration 006 純 additive；分潤在既有還款 transaction 內新增步驟
- 基線：既有 127 項端到端與所有整合測試須維持通過

## 核心不變量（必須由測試守住）
1. 單期分潤總額 == 該期實收金額（本金與利息各自加總，零誤差）
2. 分潤占比依各出借人的投資金額 / 標的目標金額
3. 捨入殘差由最大投資者吸收，不可憑空產生或消失金額
4. 提前清償同樣要分潤（只分本金，免除的利息不分）
5. 重複還款被擋下時不可重複分潤（沿用既有冪等鍵）
6. 出借人餘額增加總額 == 借款人繳款總額

## Checkpoint A — 資料模型
- [x] A1 migration 006：distributions 表（每筆還款對每位出借人的分配）
- [x] A2 investments 加 principal_returned / interest_earned 累計欄位

## Checkpoint B — 分潤引擎
- [x] B1 distributeRepayment：按占比分配，殘差歸最大投資者
- [x] B2 接進 payInstallment 的 transaction
- [x] B3 接進 settleLoan 的 transaction（只分本金）
- [x] B4 出借人餘額增加

## Checkpoint C — 出借人可見性
- [x] C1 GET /v1/investments 回傳已收本金／已收利息／待收
- [x] C2 GET /v1/investments/distributions（收款明細）
- [x] C3 portfolio 頁顯示實收 vs 預估

## Checkpoint D — 驗證
- [x] D1 單元測試：分配演算法（含捨入、單一/多位出借人、極端占比）
- [x] D2 整合測試：完整資金閉環、金額守恆、清償分潤
- [x] D3 端到端擴充
- [x] D4 既有測試不回歸

## 驗收標準
1. 借款人繳一期 → 出借人餘額依占比增加，總額等於該期實收
2. 多位出借人 → 各自占比正確，加總零誤差
3. 提前清償 → 只分剩餘本金，免除利息不分配
4. 出借人持倉顯示實收本金／利息，與 distributions 加總一致
5. 全期繳畢 → 出借人收回本金總額 == 原投資額

## Results（分潤完成）

### 新增
- `backend/migrations/006_distributions.sql`：distributions 表（repayment_id + investment_id 唯一）、
  investments 加 principal_returned / interest_earned
- `backend/cmd/api/distribution.go`：allocateProportionally（整數占比分配 + 殘差歸最大投資者）、
  distributeRepayment
- `backend/cmd/api/distribution_test.go`：7 組分配情境（含 36 期累積零誤差）
- `backend/cmd/api/integration_distribution_test.go`：資金閉環、清償只分本金、重複不重分
- `GET /v1/investments/distributions`：收款明細

### 修改
- `repayments.go`：payInstallment 與 settleLoan 在同一 transaction 內分潤
- `marketplace.go`：portfolio 加實收本金／利息／待收本金
- `portfolio.vue`：KPI 改為「已收利息 vs 預估」「待收本金」，新增收款明細表
- `verify-flow.sh`：新增第 8b 節驗證金額守恆

### 驗證
- 單元測試全綠、整合測試全綠
- 端到端 **137 通過 / 0 失敗**，可連續重跑
- 實測：出借人餘額增加 17950 == 借款人繳款金額；本金 15510 + 利息 2440 零誤差

### 關鍵設計決策
1. **整數占比分配 + 殘差歸最大投資者**：先向下取整，殘差補給金額最大者。
   保證 sum == total 且相對誤差最小。金額相同時依 investment id 決勝，確保決定性。
2. **本金與利息分開分配**：兩者各自套用同一演算法，因此清償時可以只分本金。
3. **本金與利息都回到可用餘額**：出借人可再投入其他標的，形成循環。
4. **冗餘累計欄位**：investments 的 principal_returned / interest_earned 可由
   distributions 聚合，但出借人清單是高頻查詢，冗餘欄位避免每次掃全表。
5. **無投資紀錄的合約不分潤而非報錯**：seed 與舊資料的合約資金來源不明，
   還款仍須正常完成。

### 過程中的斷言錯誤（非程式錯誤）
我一開始在 verify-flow 用 `totalPrincipalReturned`（跨所有投資的累計，含前次執行殘留）
去比對單期實收，當然不相等。已改為比對「本合約本期的分潤」。
真正的守恆檢查是「出借人餘額增量 == 借款人繳款額」，那條一開始就通過。

---

# 2026-09-22 檔案上傳（P1 最後一項）

## 目標
取代「從寫死清單挑檔名」的假上傳，實作真實的檔案上傳、儲存與下載。

## 範圍決策
不引入 S3/MinIO（會增加一個需要維運的服務）。改用：
- 檔案存於容器內的 volume（docker volume，與 postgres_data 同層級）
- 資料庫只存 metadata 與儲存路徑
- 若日後要換物件儲存，只需替換 storage 層

## 風險等級
**中**（檔案上傳是常見攻擊面：路徑穿越、型別偽造、容量耗盡）
- 回滾策略：migration 007 純 additive；documents 欄位保留舊的字串陣列
- 安全重點：副檔名白名單 + magic bytes 驗證 + 大小上限 + 隨機檔名（不用使用者提供的名稱）

## Checkpoint A — 儲存層
- [x] A1 migration 007：application_documents 表
- [x] A2 storage.go：儲存路徑生成（隨機檔名，避免路徑穿越）、寫入、讀取、刪除
- [x] A3 magic bytes 驗證（PDF/JPEG/PNG），不信任 Content-Type 與副檔名
- [x] A4 docker volume 與 compose 掛載

## Checkpoint B — API
- [x] B1 POST /v1/applications/{id}/documents（multipart 上傳）
- [x] B2 GET /v1/applications/{id}/documents（清單）
- [x] B3 GET /v1/documents/{id}/download（下載，僅本人或 reviewer）
- [x] B4 DELETE /v1/documents/{id}（僅本人，僅在申請仍可編輯時）
- [x] B5 reviewer 可讀所有申請的文件（審核需要）

## Checkpoint C — 前端
- [x] C1 真實 file input（含拖放）
- [x] C2 上傳進度與錯誤處理
- [x] C3 申請流程改為先建立申請再上傳文件
- [x] C4 風控後台可查看與下載文件

## Checkpoint D — 驗證
- [x] D1 單元測試：magic bytes 偵測、檔名生成、大小驗證
- [x] D2 整合測試：上傳/下載/刪除、跨使用者隔離、型別偽造被拒
- [x] D3 端到端擴充

## 驗收標準
1. 上傳真實檔案後可下載且內容一致
2. 偽造副檔名（.pdf 但內容是文字）被拒
3. 超過大小上限被拒
4. 他人文件不可下載（404）
5. reviewer 可讀所有文件（審核需要）
6. 檔名為隨機生成，使用者提供的名稱只作為顯示用

## Results（檔案上傳完成）

### 新增
- `backend/migrations/007_documents.sql`：application_documents 表（內容去重、路徑唯一）
- `backend/cmd/api/storage.go`：magic bytes 偵測、路徑生成、雙層穿越防護、RFC 5987 編碼
- `backend/cmd/api/documents.go`：上傳／清單／下載／刪除，含權限與狀態鎖定
- `backend/cmd/api/storage_test.go`：型別偵測、檔名清理、路徑穿越、Content-Disposition
- `backend/cmd/api/integration_documents_test.go`：完整往返、偽造拒絕、權限邊界、決議後鎖定
- `frontend/composables/useDocuments.ts`、`frontend/components/DocumentUploader.vue`

### 修改
- `backend/Dockerfile`：預先建立儲存目錄並 chown 給 nobody
- `docker/docker-compose.yml`：新增 document_data volume
- `frontend/server/api/[...path].ts`：multipart 原樣轉發、二進位下載不當 JSON 解析
- `apply.vue`：改為兩階段（先建立申請再上傳），移除假的 addDocument
- `admin.vue`：待審列表可展開查看附件

### 驗證
- 單元測試全綠、整合測試全綠
- 端到端 **155 通過 / 0 失敗**
- 實測：中文檔名上傳→下載 byte-for-byte 一致、型別偽造回 422

### 過程中發現並修正的三個缺陷
1. **docker volume 權限**：API 以 nobody 執行，但具名 volume 在映像中無對應路徑時
   會以 root 建立且權限 755，導致寫入失敗（500）。
   已在 Dockerfile 預先建立目錄並 chown。
2. **Nuxt proxy 破壞 multipart**：`readBody` 會嘗試解析並破壞邊界字串；
   回應被當 JSON 解析也會破壞二進位下載。已依 content-type 分流處理。
3. **非 ASCII 檔名變亂碼**：HTTP 標頭只能放 ASCII，中文檔名直接寫入
   `Content-Disposition` 會變成 `,f��.pdf`。已依 RFC 5987 同時提供
   `filename=`（ASCII fallback）與 `filename*=UTF-8''…`。

### 設計決策
- **不引入 S3/MinIO**：會多一個需要維運的服務。改用 docker volume，
  儲存層介面獨立（`documentStorage`），日後要換物件儲存只需替換該層。
- **先寫檔再寫資料庫**：資料庫失敗時刪檔。反之若先寫資料庫，
  程序當掉會留下指向不存在檔案的紀錄（使用者看到壞掉的下載連結）。
- **刪除時先刪紀錄再刪檔**：刪檔失敗只留下無主檔案（可由清理工作處理），
  比留下壞連結好。
- **reviewer 可讀不可寫**：審核需要看文件，但不該能改動審核依據。

---

# 2026-09-23 Proxy 重構：allowlist → 排除清單

## 目標
消除 proxy 反覆出問題的根本原因：它以 allowlist 逐項轉發 header 與內容型別，
每次後端新增有語意的 header 或非 JSON 內容就會靜默丟失。

## 歷史記錄（同一支檔案的五個缺陷）
1. cookie 未轉發 → 認證完全失效
2. x-csrf-token 未轉發 → CSRF 防護無法使用
3. retry-after 未轉發 → 429 的重試資訊丟失
4. x-forwarded-for 未轉發 → 限流把所有使用者視為同一來源
5. multipart 被 readBody 破壞、二進位回應被當 JSON 解析

第 6 個潛在缺陷已存在：`wantsBinary` 以 `path.includes('/download')` 判斷，
任何新的二進位端點都會再踩一次。

## 風險等級
**中**（proxy 是所有 API 流量的必經路徑，但有 155 項端到端斷言護著）
- 回滾策略：單一檔案變更，git revert 即可
- 基線：155 項端到端斷言須全數維持通過

## 設計原則轉換
- **請求 header**：從「列出要轉發的」改為「列出不可轉發的」（hop-by-hop 與會誤導後端的）
- **回應 header**：同上，排除 hop-by-hop 與 Nitro 會自行處理的
- **內容型別**：不再判斷路徑，改為一律用 raw body 轉發、依回應的 content-type 決定處理方式

## Checkpoint A — 重構
- [x] A1 請求 header 改為排除清單（hop-by-hop + host + content-length）
- [x] A2 body 一律以 raw 轉發，不再依 content-type 分流
- [x] A3 回應 header 改為排除清單
- [x] A4 二進位判斷改為依回應 content-type，不看路徑
- [x] A5 保留 XFF 鏈附加邏輯（這是刻意的行為，非 allowlist 問題）

## Checkpoint B — 驗證
- [x] B1 155 項端到端斷言不回歸
- [x] B2 新增斷言：任意 header 可穿透、JSON/multipart/二進位三種型別都正確
- [x] B3 typecheck + build

## 驗收標準
1. 後端新增任意回應 header 無需改 proxy 即可到達客戶端
2. JSON 請求／回應行為不變
3. multipart 上傳正常
4. 二進位下載正常，且不依賴路徑中的 `/download`
5. hop-by-hop header 不被轉發（避免連線語意錯亂）
6. 155 項端到端斷言全數通過

## Results（proxy 重構完成）

### 變更
只動一支檔案 `frontend/server/api/[...path].ts`：

| 面向 | 之前 | 之後 |
| --- | --- | --- |
| 請求 header | allowlist 六項 | 排除 hop-by-hop + host + content-length + XFF |
| 請求 body | 依 content-type 分流（readBody / readRawBody） | 一律 readRawBody 原樣轉發 |
| 回應 header | allowlist 二～六項（依路徑切換） | 排除 hop-by-hop + set-cookie + encoding/length |
| 回應 body | 依 `path.includes('/download')` 判斷 | 依回應的 content-type 判斷 |

### 驗證
- 端到端 **160 通過 / 0 失敗**（原 155 + 新增 5 項透通性斷言），可連續重跑
- 單元測試、整合測試、typecheck、build 全綠
- **關鍵驗證**：在後端暫時加一個全新的 `X-Brand-New-Header`，未修改 proxy 即穿透到客戶端，
  確認 allowlist 的結構性問題已消除（驗證後已移除探測 header）

### 順帶修掉的既有缺失
CORS header（`access-control-allow-headers` 等）先前不在 allowlist 內，
從未穿透 proxy。改為排除清單後自動修正。

### 一個我一開始寫錯的斷言
我原本斷言「proxy 不該回傳 content-length」，但實測回傳了 35。
查證後確認：blocklist 確實阻止了「轉發後端的值」，Nitro 隨後依實際 body 重算
（JSON 35、二進位 29，兩者皆正確）。斷言改為驗證「值正確」而非「不存在」。

### 潛在的第六個缺陷（已一併預防）
`wantsBinary` 原以 `path.includes('/download')` 判斷，任何新的二進位端點
都會再踩一次。已改為依回應的 content-type 判斷。

---

# 2026-09-23 Migration 版本表

## 目標
讓每支 migration 只執行一次，解除「舊 migration 必須預知未來變更」的耦合。

## 問題證據
`migrate.sh` 每次部署都重跑所有 `*.sql`，因此 002 被迫寫入 005 才引入的值：
- `users_role_check` 必須含 `investor`（005 新增）
- `applications_status_check` 必須含 `funding/funded/disbursed`（005 新增）

否則重跑 002 時，既有資料列會違反舊版約束而部署失敗（已實際發生過一次）。
這與剛修的 proxy allowlist 是同一類問題：改動 A 需要同時改動不相關的 B。

## 風險等級
**高**（migration 機制是部署的必經路徑，改錯會讓既有環境無法升級）
- 回滾策略：008 只新增 schema_migrations 表；migrate.sh 可還原
- **既有環境必須能平順升級**：001–007 已套用過，不可重跑也不可跳過記錄

## 設計
- `schema_migrations(version, checksum, applied_at)` 記錄已套用的檔案
- migrate.sh 比對檔名與 checksum：未記錄則套用、已記錄則跳過
- **checksum 不符時失敗**：已套用的 migration 不該被修改，否則各環境 schema 會分歧
- 首次導入時把既有的 001–007 標記為已套用（bootstrap），不重跑

## Checkpoint A — 版本表
- [x] A1 migration 008：schema_migrations 表
- [x] A2 migrate.sh：套用前先建表（bootstrap 不依賴自己）
- [x] A3 bootstrap 邏輯：偵測既有 schema，把 001–007 標記為已套用
- [x] A4 checksum 驗證與明確的錯誤訊息

## Checkpoint B — 解除耦合
- [x] B1 002 的 role CHECK 還原為當時的真實內容（borrower/reviewer）
- [x] B2 002 的 status CHECK 還原為當時的真實內容
- [x] B3 移除「預知未來」的註解

## Checkpoint C — 驗證
- [x] C1 既有環境升級：不重跑 001–007，008 正常套用
- [x] C2 全新環境：001–008 依序套用一次
- [x] C3 重複執行 migrate.sh：全部跳過
- [x] C4 修改已套用的 migration → 明確失敗
- [x] C5 整合測試的 applyMigrations 仍可用（它刻意重跑，需保持 idempotent）
- [x] C6 160 項端到端不回歸

## 驗收標準
1. 既有 develop 環境可平順升級，資料不損失
2. 重複 migrate.sh 全部跳過，耗時明顯下降
3. 全新資料庫從零套用成功
4. 002 不再提及 005 的狀態值
5. 修改已套用的 migration 會被擋下並說明原因

## Results（版本表完成）

### 新增
- `backend/migrations/008_schema_migrations.sql`：版本表
- `scripts/_common.sh`：`sql_query`、`sql_exec`、`sql_literal` 輔助函式
- `scripts/migrate.sh`：改為依版本表決定是否套用，含 bootstrap 與 checksum 守衛，
  新增 `--status` 選項

### 解除的耦合
`002_auth_and_workflow.sql` 還原為當時的真實內容：
- `users_role_check` 不再含 `investor`（005 才引入）
- `applications_status_check` 不再含 `funding/funded/disbursed`（005 才引入）

### 驗證（五條路徑全部實測）
1. **既有環境升級**：bootstrap 把 001–008 標記為已套用，不重跑，資料完整
2. **重複執行**：8 支全部跳過
3. **全新環境**（`down -v` 後重新部署）：8 支依序套用一次，seed 成功，端到端 160/0
4. **修改已套用的 migration**：checksum 守衛擋下並印出補救方式
5. **全新 DB 與既有 DB 的 schema 一致**：約束定義相同、皆 14 張表

- 端到端 **160 通過 / 0 失敗**（既有環境與全新環境皆是）
- 整合測試可連續重複執行
- 單元測試、typecheck、build、bash -n 全綠

### 過程中發現的連帶問題
解除 002 的耦合後，**整合測試立刻失敗** ——它刻意重跑所有 migration，
而收斂型的變更（把 CHECK 改窄）無法對已含 `funding` 資料的 DB 重複套用。

原本 `applyMigrations` 的註解寫「所有 migration 都是 idempotent，故可重複執行」，
這個前提在收斂型變更下並不成立。已改為每次 `DROP SCHEMA public CASCADE` 再從零套用。
**附帶好處**：整合測試現在每次都在驗證「全新安裝」這條路徑。

### 一個需要判斷的決定
002 的修改觸發了我自己設計的 checksum 守衛。我先查證實際 schema
（約束定義已由 005 加寬，與 002 的檔案內容無關），確認這次變更是
**純文件性質、不影響資料庫狀態**後，才手動更新記錄的 checksum。

守衛的預設立場（「不可修改，請新增一支」）是對的；這次是明確的例外，
且我是在驗證過 DB 狀態不受影響之後才繞過它，不是因為它擋路就關掉。

---

# 2026-09-23 收入欄位最小化揭露 + 清單分頁

## 目標
兩個獨立的 P2 項目：
1. 收入與支出不再以原值回傳 API；改回傳區間，完整值走既有的 reveal 稽核端點
2. `adminApplications` 的 `LIMIT 200` 硬編碼改為真實分頁

## 決策（已與使用者確認）
收入被 DBR 計算使用（每筆清單查詢都要讀），因此**不採用** AES-GCM 加密——
那會讓熱路徑多 N 次解密，且 SQL 層無法以收入篩選或排序。
實測確認真正的缺陷是「原值被回傳到所有回應但前端完全沒用」，
故改為最小化揭露：資料庫維持明文，API 只給區間與 DBR。

## 風險等級
**低**（移除回應欄位 + 新增分頁參數，皆為既有端點的收斂）
- 回滾策略：單一變更範圍
- 基線：160 項端到端斷言

## Checkpoint A — 收入最小化揭露
- [x] A1 incomeBracket 函式：把金額歸入區間標籤
- [x] A2 applicationSummary 移除 AnnualIncome/MonthlyExpenses，改為 IncomeRange
- [x] A3 reveal 端點加入完整收入與支出（已有稽核紀錄）
- [x] A4 前端型別同步；風控後台顯示區間
- [x] A5 確認 DBR 計算不受影響（它在後端算，不需要前端拿到原值）

## Checkpoint B — 分頁
- [x] B1 分頁參數解析（limit/offset，含上下限與預設值）
- [x] B2 adminApplications 回傳 items + total + hasMore
- [x] B3 myApplications 同樣分頁
- [x] B4 其餘有 LIMIT 的端點檢視（listings/overdue/distributions/repayments）
- [x] B5 前端處理新的回應結構

## Checkpoint C — 驗證
- [x] C1 單元測試：區間歸類、分頁參數邊界
- [x] C2 整合測試：收入不外洩、reveal 可取得、分頁正確切分
- [x] C3 端到端擴充
- [x] C4 160 項不回歸

## 驗收標準
1. 任何清單 API 回應都不含 annualIncome/monthlyExpenses 原值
2. 風控仍可依 DBR 與收入區間判讀
3. reveal 端點可取得完整收入，且寫入稽核紀錄
4. 分頁：limit 超出範圍被夾住、offset 可翻頁、total 正確
5. 既有 160 項斷言全數通過

## Results（兩項皆完成）

### 新增
- `backend/cmd/api/disclosure.go`：incomeBracket / expenseBracket、分頁參數解析與 meta
- `backend/cmd/api/disclosure_test.go`：級距邊界、參數夾制、hasMore 計算
- `backend/cmd/api/integration_disclosure_test.go`：收入不外洩、reveal 可取得、
  offset 翻頁不重複不遺漏、篩選與分頁併用

### 修改
- `applicationSummary`：`AnnualIncome`/`MonthlyExpenses` 改為私有欄位（僅供 DBR 計算），
  對外改為 `IncomeRange`/`ExpenseRange`
- `reveal` 端點加入完整收入與支出，稽核欄位一併更新
- `myApplications` 與 `adminApplications` 改為分頁，回傳 `{items, page}`
- 前端型別、useDashboard、useAdmin、admin.vue 同步

### 驗證
- 端到端 **172 通過 / 0 失敗**（原 160 + 12 項），可連續重跑
- 單元測試、整合測試、typecheck、build 全綠

### 設計決策：為何不加密收入
原計畫寫的是「加密」，但實作前先確認用途後改變了做法：

`annual_income` 被 DBR 計算使用，**每筆清單查詢都要讀**。若比照身分證加密：
- 熱路徑會多 N 次解密（N = 清單筆數）
- SQL 層無法再以收入篩選或排序

實測後發現真正的缺陷不是「沒加密」，而是「原值被回傳到所有 API 回應，
但前端完全沒有顯示它」——曝露面大於用途。
因此改為最小化揭露：資料庫維持明文（DBR 需要），API 只給級距與 DBR。
此決策已與使用者確認。

### 一個我寫錯的測試資料
`expenseBracket` 的測試我把 32000 期望為「1～3 萬」、30000 期望為「3～5 萬」，
兩者互相矛盾（32000 > 30000 卻歸到更低的級距）。實作是對的，測試資料寫錯。
已補上完整的邊界案例（下界、上界、邊界歸屬）。

---

# 2026-09-23 標的募資期限與取消退款

## 目標
讓募資有期限：逾期未募滿的標的自動取消，並把已投入的資金退回出借人。
目前 `cancelled` 狀態存在於 CHECK 約束但沒有任何程式碼會設定它（死狀態），
且沒有退款路徑——若標的永遠募不滿，出借人的錢會被永久鎖住。

## 風險等級
**高**（涉及退款，錢退錯或退兩次比不退更糟）
- 回滾策略：migration 009 純 additive
- 基線：172 項端到端斷言

## 核心不變量（必須由測試守住）
1. 退款總額 == 該標的的 funded_amount（零誤差）
2. 每位出借人退回的金額 == 其投入金額（非按比例，是原額退回）
3. 取消只能發生在 funding 狀態；已撥款的標的不可取消
4. 重複取消不可重複退款（狀態機 + 冪等）
5. 取消後申請回到可再次審核的狀態（借款人不該因平台募資失敗而被婉拒）
6. 已取消的標的不可再投標

## Checkpoint A — 資料模型
- [x] A1 migration 009：listings 加 funding_deadline、cancelled_at、cancel_reason
- [x] A2 既有標的補上 deadline（created_at + 預設天數）

## Checkpoint B — 取消與退款
- [x] B1 cancelExpiredListings：掃出逾期未募滿者
- [x] B2 退款：逐筆 investments 原額退回 available_balance
- [x] B3 申請狀態回到 more_info_required（附說明），讓風控可重新處理
- [x] B4 接進既有的每日排程（沿用 startOverdueSweep 的模式）
- [x] B5 reviewer 手動取消端點（募資明顯無望時不必等到期）

## Checkpoint C — 可見性
- [x] C1 listing 回傳剩餘天數
- [x] C2 市集顯示倒數；即將到期的標的標示
- [x] C3 出借人持倉顯示已退款的投資

## Checkpoint D — 驗證
- [x] D1 單元測試：期限計算、剩餘天數
- [x] D2 整合測試：退款金額守恆、重複取消、已撥款不可取消、取消後不可投標
- [x] D3 端到端擴充
- [x] D4 172 項不回歸

## 驗收標準
1. 逾期未募滿 → 標的轉 cancelled，出借人拿回全額
2. 退款總額等於 funded_amount，每人拿回自己投入的金額
3. 已撥款的標的不受期限影響
4. 重複執行取消不會重複退款
5. 取消後申請可被風控重新處理

## Results（募資期限完成）

### 新增
- `backend/migrations/009_funding_deadline.sql`：listings 加 funding_deadline/cancelled_at/cancel_reason、
  investment_refunds 表（每筆投資只能退款一次的唯一約束）
- `backend/cmd/api/funding_deadline.go`：期限計算、取消退款、每日排程、reviewer 手動取消
- `backend/cmd/api/funding_deadline_test.go`、`integration_funding_deadline_test.go`
- `POST /v1/admin/listings/{id}/cancel`：風控手動取消（需理由）
- `writeInternalError`：500 回應同時把真因寫進日誌

### 驗證
- 端到端 **185 通過 / 0 失敗**（原 172 + 13 項）
- 整合測試涵蓋：退款金額守恆、原額退回（非按比例）、重複取消不重複退款、
  已撥款標的不可取消、逾期標的拒絕投標、無投資人的標的也能取消
- 單元測試、typecheck、build 全綠

### 三個過程中發現的缺陷
1. **seed 缺少新欄位** → 009 把 funding_deadline 設為 NOT NULL，seed 的 listing 插入失敗。
   已補上並讓期限以執行時間為基準（重跑 seed 不會立刻被逾期掃描取消）。
2. **啟動排程早於 migration** → 與 PII 搬遷同一個問題。已加 fundingDeadlineReady 就緒檢查。
3. **pgx 無法把 int 編碼進字串拼接**：`($12 || ' days')::interval` 讓參數被推論為 text。
   改為 `$12::int * INTERVAL '1 day'`。

### 順帶修掉的觀測性缺口
第 3 點原本只回傳 `{"error":"failed to create funding listing"}`，伺服器端毫無紀錄，
只能靠猜測排查。已新增 `writeInternalError`：對客戶端維持通用訊息（不洩漏內部細節），
但把真正的原因寫進結構化日誌。加上這個之後，同一個錯誤一行就定位了。

### 設計決策
- **退款為原額退回而非按比例**：標的從未撥款，本金完全沒動用，出借人投入多少拿回多少。
- **取消後申請回到 more_info_required 而非 rejected**：募資失敗是平台端的結果，
  不代表授信條件不符，借款人應該能被重新處理。
- **每個標的各自一個 transaction**：一個標的退款失敗不該讓其他標的也回滾。
- **投標時也檢查期限**：排程每日執行，期限剛過但尚未掃到時仍須拒絕投標，
  否則會出現「投進即將被退款的標的」。

---

# 2026-09-23 OCR 文件驗證

## 目標
從上傳的文件中擷取文字，做「可驗證的一致性檢查」，取代目前只驗檔案格式的狀態。

## 範圍界定（重要）
OCR 很容易過度承諾。**明確不做**的事：
- 不判斷文件真偽（OCR 無法辨識變造；這需要專門的防偽技術）
- 不自動核准或婉拒（結果僅供風控參考，決策權仍在人）
- 不做版面理解（不解析表格結構、不定位欄位座標）

**實際做的**：擷取文字 → 比對申請人填寫的資料是否出現在文件中 →
以「相符 / 未找到 / 無法辨識」三種結果供風控參考。

這是誠實的定位：OCR 能回答「這份文件裡有沒有出現申請人的姓名與身分證字號」，
不能回答「這份文件是不是真的」。

## 技術選擇
- Alpine 安裝 `tesseract-ocr` + `eng` + `chi_tra`（已實測可行，5.5.0）
- 以 `exec.Command` 呼叫 CLI 而非 cgo 綁定：不新增編譯依賴，且容器已有二進位
- PDF 需先轉圖：`poppler-utils` 的 `pdftoppm`
- **非同步處理**：OCR 耗時數秒，不可阻塞上傳請求

## 風險等級
**中**（涉及執行外部程序與處理不受信任的檔案內容）
- 安全重點：不把使用者輸入放進 shell、逾時、輸出大小上限、暫存檔清理
- 回滾策略：migration 010 純 additive；OCR 失敗不影響上傳與審核

## Checkpoint A — 資料模型與工具
- [x] A1 migration 010：application_documents 加 ocr_status/ocr_text/ocr_checked_at、
      document_verifications 表（比對結果）
- [x] A2 Dockerfile 安裝 tesseract + 語言包 + poppler-utils
- [x] A3 ocr.go：呼叫 tesseract、PDF 轉圖、逾時與清理

## Checkpoint B — 比對邏輯
- [x] B1 正規化：全形轉半形、去空白、統一大小寫
- [x] B2 比對申請人姓名與身分證字號是否出現在文字中
- [x] B3 三態結果：matched / not_found / unreadable

## Checkpoint C — 非同步處理
- [x] C1 上傳後排入佇列（以 ocr_status = pending 標記）
- [x] C2 背景 worker 逐筆處理
- [x] C3 API 回傳 OCR 狀態與比對結果

## Checkpoint D — 驗證
- [x] D1 單元測試：正規化、比對邏輯（不依賴 tesseract）
- [x] D2 整合測試：狀態流轉、失敗不影響上傳
- [x] D3 端到端：上傳含文字的圖片 → 等待處理 → 確認擷取結果
- [x] D4 185 項不回歸

## 驗收標準
1. 上傳後 ocr_status 為 pending，不阻塞上傳回應
2. 背景處理完成後狀態轉為 done/failed，並記錄擷取文字
3. 比對結果正確標示姓名與身分證是否出現
4. OCR 失敗（檔案損壞、逾時）不影響文件本身可下載
5. 風控後台可看到 OCR 結果
6. 明確標示「僅供參考，不判斷真偽」

## Results（OCR 完成）

### 新增
- `backend/migrations/010_document_ocr.sql`：ocr_status/ocr_text/ocr_error、
  document_verifications 表（每份文件每個項目只保留最新結果）
- `backend/cmd/api/ocr.go`：tesseract 呼叫、PDF 轉圖、文字正規化與三態比對
- `backend/cmd/api/ocr_worker.go`：背景 worker，以 FOR UPDATE SKIP LOCKED 原子取件
- `backend/cmd/api/ocr_test.go`、`integration_ocr_test.go`
- Dockerfile 安裝 tesseract-ocr + eng + chi_tra + poppler-utils

### 驗證（實際跑過 OCR，非樁）
- 以 PIL 產生含 `A123456789` 的身分證圖片 → 上傳 → 5 秒內背景完成 →
  `{"applicant_name":"matched","id_number":"matched"}`
- 資料庫中的 ocr_text：`NATIONAL ID CARD | Name: CHEN CHIEN HUNG | ID No: A123456789 | ...`
- 上傳與申請人無關的文件 → `not_found`（未誤判為相符）
- 端到端 **191 通過 / 0 失敗**，可連續重跑
- 容器內 tesseract 5.5.0、chi_tra 語言包、pdftoppm 皆確認可用

### 範圍界定（誠實的定位）
明確**不做**：不判斷文件真偽、不自動核准或婉拒、不做版面理解。
OCR 能回答「文件裡有沒有出現申請人的姓名與身分證」，
不能回答「這份文件是不是真的」——它無法辨識變造。
前端與 README 都明確標示這一點。

### 設計決策
1. **exec.Command 呼叫 CLI 而非 cgo 綁定**：不新增編譯依賴，容器已有二進位。
   全程不經 shell，檔名由伺服器生成於暫存目錄。
2. **背景處理**：OCR 數秒，上傳必須立即回應。初始狀態 pending，worker 每 5 秒取件。
3. **FOR UPDATE SKIP LOCKED 原子取件**：多副本部署時不會重複處理同一份文件。
   已加測試斷言「同一份文件不可被取走兩次」。
4. **三態而非二態**：unreadable 與 not_found 必須分開——
   前者是影像問題，後者是內容不符，混在一起會讓風控誤判。
5. **工具不可用時標記 skipped 而非 failed**：缺少 tesseract 是部署環境的問題。
6. **保留 ocr_text 原文**：供風控人工覆核，而非只存比對結果。

### 一個我自己造成的誤報
第 19 節的「回應洩漏完整身分證」失敗了。查證後發現是我先前手動測試 OCR 時，
為了讓姓名與圖片文字相符，把 `applicantName` 設成了 `A123456789`，
該筆資料留在資料庫中被斷言掃到。

程式行為正確（身分證本身仍正確遮罩為 `A12****789`），是測試資料污染了斷言。
刪除該筆資料後 191/0。這也說明那條斷言確實有效——它抓到了回應中的明文身分證字串。

---

# 2026-09-23 Email 驗證與密碼重設

## 目標
補上帳號生命週期的兩個缺口：確認 email 屬於本人、忘記密碼時能自行重設。

## 為何這項可以做到「可驗證」
先前把它列為「需要郵件服務」的已知限制。但用 MailHog（SMTP sink + HTTP API）
可以在本機完整驗證信件真的寄出、內容正確、連結可用——
不是只寫個介面然後假設它會動。
`net/smtp` 在標準庫中，不新增編譯依賴。

## 風險等級
**高**（密碼重設是帳號接管的主要攻擊面）
- 回滾策略：migration 011 純 additive
- 基線：191 項端到端斷言

## 安全要求（密碼重設是攻擊目標）
1. token 只存雜湊（與 session 相同策略），資料庫外洩不可用於重設
2. token 單次使用：用過即失效
3. 短時效（重設 1 小時、驗證 24 小時）
4. **不洩漏帳號是否存在**：對未註冊的 email 也回傳成功
5. 重設成功後**撤銷所有既有 session**：攻擊者可能已登入
6. 限流：避免被用來轟炸他人信箱
7. token 以 crypto/rand 產生，長度足夠

## Checkpoint A — 資料模型與郵件
- [x] A1 migration 011：users 加 email_verified_at、email_tokens 表
- [x] A2 mailer.go：SMTP 寄信，含逾時與錯誤處理
- [x] A3 開發環境無 SMTP 時寫入日誌而非失敗
- [x] A4 compose 加 MailHog（僅 develop）

## Checkpoint B — Email 驗證
- [x] B1 註冊後自動寄驗證信
- [x] B2 POST /v1/auth/verify-email（以 token 驗證）
- [x] B3 POST /v1/auth/resend-verification（重寄，限流）
- [x] B4 未驗證不阻擋登入，但申請貸款需已驗證

## Checkpoint C — 密碼重設
- [x] C1 POST /v1/auth/forgot-password（不洩漏帳號存在性）
- [x] C2 POST /v1/auth/reset-password（token + 新密碼）
- [x] C3 重設後撤銷所有 session
- [x] C4 token 單次使用

## Checkpoint D — 前端
- [x] D1 /verify-email 與 /reset-password 頁面
- [x] D2 登入頁加「忘記密碼」
- [x] D3 未驗證時的提示與重寄按鈕

## Checkpoint E — 驗證
- [x] E1 單元測試：token 產生與雜湊、時效判定
- [x] E2 整合測試：完整流程、token 單次使用、撤銷 session、不洩漏存在性
- [x] E3 端到端：經 MailHog API 取出真實信件並完成流程
- [x] E4 191 項不回歸

## 驗收標準
1. 註冊後 MailHog 收到驗證信，內含可用的 token
2. 驗證後 email_verified_at 有值
3. 未驗證的帳號不能送出貸款申請
4. 忘記密碼對未註冊 email 也回 200（不洩漏存在性）
5. 重設後舊密碼失效、所有 session 被撤銷、token 不可重用
6. 過期 token 被拒

## Results（email 驗證與密碼重設完成）

### 新增
- `backend/migrations/011_email_verification.sql`：users 加 email_verified_at、
  email_tokens 表（雜湊儲存、單次使用、用途隔離）
- `backend/cmd/api/mailer.go`：net/smtp 寄信、標頭注入防護、無 SMTP 時寫日誌
- `backend/cmd/api/email_tokens.go`：token 簽發與消耗、四個端點
- `backend/cmd/api/mailer_test.go`、`integration_email_test.go`
- 前端 `/verify-email`、`/forgot-password`、`/reset-password` 三頁
- `docker-compose.develop.yml` 加 MailHog（SMTP sink + HTTP API）

### 驗證（經 MailHog 實際收信，非樁）
- 註冊 → MailHog 收到「請驗證您的 CreditFlow 電子信箱」→ 取出連結 token →
  驗證成功 → emailVerified 轉 true → 可送出申請 → token 重用回 422
- 忘記密碼：未註冊與已註冊的**回應完全相同**，但只為已註冊者寄出 1 封信
- 重設：舊 session 被撤銷（401）、舊密碼失效（401）、新密碼可登入（200）、token 不可重用
- 端到端 **208 通過 / 0 失敗**（原 191 + 17 項），可連續重跑
- 單元測試、整合測試、typecheck、build 全綠

### 安全設計
1. **token 只存 SHA-256 雜湊**（與 session 相同）：資料庫外洩不可用於接管帳號
2. **單次使用**：以 `UPDATE ... RETURNING` 一次完成「檢查 + 標記已使用」，
   先查再改會讓同一 token 在併發下被用兩次
3. **用途隔離**：驗證 token 不能重設密碼，反之亦然（已加測試）
4. **重設後撤銷所有 session**：帳號若已被接管，改密碼必須同時把對方踢出
5. **不洩漏帳號存在性**：forgot-password 對未註冊信箱也回 200，回應內容完全相同
6. **重設時效（1h）短於驗證（24h）**：重設連結能直接接管帳號
7. **標頭注入防護**：主旨與收件人清掉 CR/LF，避免夾帶額外收件人

### 過程中發現的設計錯誤
我一開始把三個端點都納入 CSRF 保護，結果 forgot-password 直接回 403 ——
**呼叫這個端點的人正是「無法登入」的人**，他們沒有 session 也沒有 CSRF token。
verify-email 與 reset-password 同理（使用者從信件連結進來）。

已改為 CSRF 豁免，並在程式碼與 README 說明防護來自別處
（token 本身是高熵單次使用的秘密 + 限流 + 不洩漏存在性）。

### 為何這項能做到「可驗證」
先前把它列為「需要郵件服務」的已知限制而跳過。
MailHog 提供 SMTP sink 與 HTTP API，因此端到端可以真的收信、
解析出連結、走完整個流程——而不是寫個介面然後假設它會動。
`net/smtp` 在標準庫中，沒有新增編譯依賴。

---

# 2026-09-23 登入失敗鎖定

## 目標
補上帳號維度的暴力破解防護。目前只有 IP 限流：攻擊者輪替 IP 即可對單一帳號
無限次嘗試，因為失敗次數從未按帳號記錄。

## 風險等級
**中**（鎖定機制本身可被濫用為 DoS：攻擊者故意輸錯讓他人無法登入）
- 回滾策略：migration 012 純 additive
- 基線：208 項端到端斷言

## 設計考量
1. **暫時鎖定而非永久**：永久鎖定讓攻擊者能以故意輸錯來封鎖任意帳號。
   採遞增延長（5 次→5 分鐘、10 次→30 分鐘、15 次→2 小時）。
2. **成功登入即清空計數**：合法使用者偶爾打錯不該累積到被鎖。
3. **鎖定期間不洩漏帳號存在性**：對不存在的帳號回相同訊息，
   否則「被鎖定」這個回應本身就是帳號存在的證據。
   → 決定：鎖定時回 429 + Retry-After，訊息不提及帳號。
4. **密碼重設成功後解除鎖定**：合法擁有者證明了身分，不該還被鎖著。
5. 記錄最後失敗時間與來源，供風控查閱。

## Checkpoint A — 資料模型
- [x] A1 migration 012：users 加 failed_login_count、locked_until、last_failed_login_at
- [x] A2 lockout.go：鎖定時長計算、記錄失敗、清除計數

## Checkpoint B — 接入登入流程
- [x] B1 登入前檢查是否鎖定
- [x] B2 密碼錯誤時累加並可能鎖定
- [x] B3 成功登入清空計數
- [x] B4 密碼重設成功時解除鎖定

## Checkpoint C — 可見性
- [x] C1 鎖定時回 429 與 Retry-After
- [x] C2 前端顯示剩餘等待時間與「忘記密碼」引導

## Checkpoint D — 驗證
- [x] D1 單元測試：鎖定時長遞增、閾值邊界
- [x] D2 整合測試：達閾值被鎖、鎖定期間正確密碼也被拒、
      到期自動解鎖、成功登入清空、重設解除鎖定、不洩漏帳號存在性
- [x] D3 端到端擴充
- [x] D4 208 項不回歸

## 驗收標準
1. 連續失敗達閾值後鎖定，回 429 + Retry-After
2. 鎖定期間即使密碼正確也被拒
3. 鎖定到期後可正常登入
4. 中途成功登入會清空計數
5. 對不存在的帳號不因鎖定機制而回應不同
6. 密碼重設成功後鎖定解除

## Results（登入鎖定完成）

### 新增
- `backend/migrations/012_login_lockout.sql`：users 加 failed_login_count、
  locked_until、last_failed_login_at/ip
- `backend/cmd/api/lockout.go`：遞增時長計算、記錄失敗、清除計數
- `backend/cmd/api/lockout_test.go`、`integration_lockout_test.go`
- 登入頁顯示剩餘等待時間與「重設密碼可立即解除」的引導

### 驗證
- 端到端 **215 通過 / 0 失敗**（原 208 + 7 項），可連續重跑
- 實測：第 5 次失敗觸發鎖定、Retry-After 300 秒、鎖定期間正確密碼也被拒 429、
  不存在的帳號始終回 401、密碼重設後可立即登入
- 單元測試、整合測試、typecheck、build 全綠

### 設計決策
1. **暫時鎖定且時長遞增，而非永久鎖定**：永久鎖定會讓攻擊者能以故意輸錯
   封鎖任意帳號，把防護本身變成 DoS 工具。
2. **鎖定期間即使密碼正確也拒絕**：否則攻擊者猜中密碼時鎖定就形同虛設。
3. **不存在的帳號不會被鎖定**：沒有紀錄可累加，因此永遠回 401。
   若對不存在的帳號也回 429，「被鎖定」就成了帳號存在的證據。
4. **成功登入清空計數**：合法使用者偶爾打錯不該累積。
5. **密碼重設解除鎖定**：擁有者已透過信件證明身分，提供合法的自救管道。
6. **時長有上限（24 小時）**：無限增長等同永久鎖定。

### 測試設計
單元測試守住三個不變量：時長單調遞增、有上限、門檻設定合理
（第一道門檻不可低於 3 次——會困擾正常使用者；不可高於 10 次——失去防護意義）。
整合測試特別驗證「不洩漏帳號存在性」：比對存在與不存在帳號的狀態碼與回應主體。
