# CreditFlow 信達金融

CreditFlow 是一個 Nuxt 前端、Go API 與 PostgreSQL 的借貸平台專案。前端透過 Nuxt 同源 API proxy 連到 Go 後端，全程以 session cookie 驗身。

目前已打通的主流程：

**借款端**：註冊 → 送出申請 → 風控審核 → 核准上架募資 → 募滿撥款 →
逐期還款或提前清償 → 逾期自動判定並進入催收清單

**出借端**：註冊出借人 → 入金 → 在市集投標已通過授信的標的 →
標的募滿後平台自動撥款 → 借款人每期還款時**按投資占比收到本金與利息** →
於持倉追蹤實收與待收

## 技術結構

```text
frontend/
  pages/               路由頁面（每個頁面一個 URL）
  layouts/             全站版面（導覽列、頁尾、toast、對話框）
  components/          共用元件
  composables/         狀態與資料存取
  middleware/          路由守衛（auth、reviewer）
  types/               前後端共用的資料型別
backend/cmd/api/       Go HTTP API
backend/migrations/    PostgreSQL schema
backend/seed/          PostgreSQL demo data
docker/                compose 與環境樣板（含 document_data volume）
scripts/               deploy / migrate / seed / check / verify-flow
```

## 路由

| 路徑 | 說明 | 存取 |
| --- | --- | --- |
| `/` | 首頁與試算機 | 公開 |
| `/login` | 登入／註冊（`?mode=register` 直接開註冊） | 公開 |
| `/verify-email` | 信箱驗證（信件連結導向） | 公開 |
| `/forgot-password` | 索取密碼重設連結 | 公開 |
| `/reset-password` | 設定新密碼（信件連結導向） | 公開 |
| `/market` | 投資市集（真實募資標的） | 公開 |
| `/portfolio` | 我的投資（持倉、餘額、入金） | 需 investor |
| `/dashboard` | 我的儀表板 | 需登入 |
| `/apply` | 申請貸款（可由試算機帶入 `?amount=&term=`） | 需登入 |
| `/repay` | 未結清合約列表 | 需登入 |
| `/repay/:id` | 指定合約的攤還明細與繳款紀錄 | 需登入（僅本人） |
| `/admin` | 風控後台 | 需 reviewer |

每個頁面有獨立 URL，可直接輸入、分享或加入書籤；重新整理停留在同一頁，
瀏覽器上一頁／下一頁正常運作。未登入存取受保護頁面會導向
`/login?redirect=<原路徑>`，登入後自動回到原本要去的位置。

路由守衛在 SSR 階段就會驗證 session（`composables/useApiFetch.ts` 會轉發 cookie），
因此伺服器端直接渲染出正確內容，而非先給空殼再由瀏覽器補。

## 啟動環境

需要 Docker Compose v1.29+（`docker-compose`）或 v2（`docker compose`）。第一次啟動 develop 環境時，部署腳本會從 example 建立 runtime env；本機展示可直接讓腳本產生弱敏感值的替代值：

```bash
./scripts/deploy.sh develop --auto-secrets
```

完成後開啟 <http://localhost:3000>。develop override 也會提供：

- Nuxt：`http://localhost:3000`
- Go API：`http://localhost:8080`
- PostgreSQL：`localhost:5432`

production 部署：

```bash
./scripts/deploy.sh production --auto-secrets
```

正式環境建議先編輯 `docker/envs/.env.production`，填入正式網域與自行管理的強密鑰。若要手動執行資料庫流程：

```bash
./scripts/migrate.sh develop
./scripts/seed.sh develop
```

## 常用操作

```bash
docker-compose --env-file docker/.env \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.develop.yml ps

docker-compose --env-file docker/.env \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.develop.yml logs -f api
```

環境檔規則：只提交 `docker/envs/.env.<env>.example`；`docker/.env` 與 `docker/envs/.env.<env>` 都是 runtime 檔案，已加入 ignore。`deploy.sh` 會在 compose 啟動前複製環境檔並檢查弱敏感值；可用 `--auto-secrets` 自動產生，或用 `--skip-secrets` 明確跳過（production 另需 `--i-know-what-im-doing`）。

## 帳號與角色

系統有三種角色：

- `borrower`（借款人）：申請貸款、查看自己的合約與攤還表、還款
- `investor`（出借人）：入金、在市集投標、追蹤持倉與預估收益
- `reviewer`（風控人員）：審核申請、核准／婉拒／要求補件、查看催收清單

自助註冊可選擇 `borrower` 或 `investor`；
`reviewer` 只能由 seed 或維運直接建立，請求中夾帶其他角色值一律降級為 `borrower`。

develop 環境的展示帳號（由 `backend/seed/000_demo_users.sql` 建立）：

| 角色 | 帳號 | 密碼 | 備註 |
| --- | --- | --- | --- |
| 借款人 | `demo@creditflow.test` | `demo1234` | 已有合約與一筆募資中申請 |
| 出借人 | `investor@creditflow.test` | `invest1234` | 預先入金 200 萬 |
| 風控人員 | `reviewer@creditflow.test` | `review1234` | |

> 這些帳號僅供本機展示，請勿套用到正式環境。

## 授信規則

核准時依申請人信用評分決定等級與適用利率，並以本息平均攤還生成攤還表：

| 等級 | 評分門檻 | 年利率 |
| --- | --- | --- |
| A | ≥ 750 | 4.88% |
| B | ≥ 650 | 6.80% |
| C | < 650 | 9.60% |

建議欄位（輔助風控人員判斷，非自動決策）：

- DBR ≥ 25% 或評分 < 600 → 建議婉拒
- DBR ≥ 22% 或評分 < 680 → 需補件
- 其餘 → 建議核准

## API

公開端點：

- `GET /health`：API 與 PostgreSQL readiness
- `POST /v1/auth/register`：註冊（固定為 borrower）並建立 session
- `POST /v1/auth/login`：登入
- `POST /v1/auth/logout`：登出並失效 session
- `POST /v1/auth/verify-email`：以信件 token 驗證信箱
- `POST /v1/auth/forgot-password`：寄出重設連結（不洩漏帳號是否存在）
- `POST /v1/auth/reset-password`：以 token 設定新密碼並撤銷所有 session
- `GET /v1/listings`：募資中的標的（`?status=funding|funded|disbursed|all`）；
  登入時會附帶「我已投入多少」
- `GET /v1/market/listings`：legacy 端點，已停用所有列

需登入：

- `GET /v1/auth/me`：當前登入者
- `POST /v1/auth/resend-verification`：重寄驗證信
- `GET /v1/dashboard`：自己的合約與摘要（借款總額、月付合計、評分、還款率）
- `GET /v1/applications`：自己送出的申請
- `POST /v1/applications`：建立貸款申請
- `GET /v1/applications/{id}/documents`：申請附件清單
- `POST /v1/applications/{id}/documents`：上傳附件（multipart，欄位名 `file`）
- `GET /v1/documents/{id}/download`：下載附件
- `DELETE /v1/documents/{id}`：刪除附件
- `GET /v1/loans/{id}/schedule`：自己合約的攤還明細
- `GET /v1/loans/{id}/repayments`：該合約的繳款紀錄
- `POST /v1/loans/{id}/installments/{no}/pay`：繳納指定期數
- `POST /v1/loans/{id}/settle`：提前清償（一次結清剩餘本金，未到期利息免除）

需 `investor` 角色：

- `POST /v1/listings/{id}/invest`：投標（`amount` + 選用 `requestId` 作冪等鍵）
- `GET /v1/investments`：持倉、餘額、加權平均年化與預估收益
- `POST /v1/investments/top-up`：入金（展示用，未接金流）
- `GET /v1/investments/distributions`：收款明細（每筆還款分得的本金與利息）

需 `reviewer` 角色：

- `GET /v1/admin/applications`：待審清單（可加 `?status=pending`），含後端試算的 DBR 與建議
- `PATCH /v1/admin/applications/{id}`：審核，`action` 為 `approve`／`reject`／`request_more_info`
- `GET /v1/admin/overdue`：逾期催收清單（含逾期天數、M1/M2/M3+ 階段與建議處理）

核准（`approve`）會建立募資標的並把申請轉為 `funding`，**此時尚無合約**；
重複核准回 409。標的募滿時才在同一個 transaction 內寫入 `loans` 與完整
`loan_installments`，因此不會出現「已撥款但沒有攤還表」的中間狀態。

## P2P 撮合

核准**不會**直接生成合約——資金必須先到位：

```text
申請 pending
  ↓ reviewer 核准
申請 funding + 標的上架（listings）
  ↓ 出借人投標累積
標的 funded_amount 達到 target_amount
  ↓ 同一 transaction 內自動撥款
合約 loans + 完整攤還表，申請與標的轉為 disbursed
```

規則：

- **不可超募**：`listings` 有 `funded_amount <= target_amount` 的 CHECK 約束，
  投標時以 `FOR UPDATE` 鎖定標的；超過剩餘額度的投標**只接受剩餘部分**而非整筆拒絕
- **冪等保護**：`investments.idempotency_key` 有唯一約束。
  同一人可對同一標的多次投標，但相同 `requestId` 重送會回 409
- **餘額檢查**：投標時鎖定出借人餘額，不足回 422；扣款與投標在同一 transaction
- **借款人不得投資自己的標的**（`investor` 角色本身也無權存取借款端點）
- 撥款沿用 `createLoanFromApplication`，確保攤還表計算只有一處實作

出借人餘額為簡化模型：`users.available_balance` 單一欄位，
入金端點直接增加餘額。**未接任何金流閘道**。

舊的 `market_listings` 靜態展示資料已由 `listings` 取代，
migration 005 將其標記為 legacy 並停用所有列（保留資料表供回滾）。

## 檔案上傳

申請文件為真實上傳：內容存於 docker volume，資料庫只存 metadata 與儲存路徑。

安全措施：

- **型別以 magic bytes 判定**（PDF `%PDF-`、JPEG `FF D8 FF`、PNG 八位元簽章）。
  副檔名與 `Content-Type` 都由客戶端提供，可任意偽造，因此完全不採信
- **儲存路徑完全由伺服器生成**（`YYYY/MM/<32 hex>.<依實際型別決定的副檔名>`）。
  使用者提供的檔名只存進資料庫供顯示，絕不參與路徑組合
- **雙層路徑穿越防護**：寫入前檢查解析結果仍在儲存根目錄內，
  即使 `storage_path` 遭篡改也不會讀寫到根目錄之外
- **不覆蓋既有檔案**：以 `O_EXCL` 開檔，路徑由隨機值生成，碰撞即視為異常
- 單檔上限 10 MiB、每份申請上限 20 份、相同內容（SHA-256）不可重複上傳
- 下載一律 `Content-Disposition: attachment` 加 `X-Content-Type-Options: nosniff`，
  非 ASCII 檔名依 RFC 5987 以 `filename*=UTF-8''…` 編碼

權限：

| 角色 | 列出／下載 | 上傳／刪除 |
| --- | --- | --- |
| 申請人本人 | ✓ | 僅 `pending` / `reviewing` / `more_info_required` 時 |
| reviewer | ✓（審核需要） | ✗（不得改動審核依據） |
| 其他人 | ✗（回 404，不洩漏存在性） | ✗ |

申請流程因此改為**兩階段**：填完個人資料時先建立申請（狀態 `pending`），
再把文件附加到該申請上。核准後文件即鎖定。

### 文字辨識（OCR）

上傳後在**背景**以 tesseract 擷取文字（`chi_tra+eng`），PDF 先以 `pdftoppm` 轉圖。
OCR 需要數秒，因此不阻塞上傳回應：文件初始狀態為 `pending`，worker 每 5 秒取件。

擷取後比對申請人填寫的姓名與身分證是否出現在文件中，結果為三態：

| 結果 | 意義 |
| --- | --- |
| `matched` | 文件中找到申請人填寫的值 |
| `not_found` | 擷取成功但找不到該值 |
| `unreadable` | 擷取出的文字過少（影像模糊或空白） |

比對前會正規化文字（去空白、全形轉半形、統一大小寫），
因為 OCR 常在字元間插入空白或輸出全形字元。

> **能力邊界**：OCR 能回答「這份文件裡有沒有出現申請人的姓名與身分證字號」，
> **不能**回答「這份文件是不是真的」。它無法辨識變造，因此不判斷真偽、
> 不自動核准或婉拒，結果僅供風控人工審核參考。前端也明確標示這一點。

OCR 失敗（檔案損壞、逾時）不影響文件本身：仍可下載，申請仍可審核。
`tesseract` 不存在時文件標記為 `skipped`（部署環境問題，不是文件問題），
API 照常運作。

## 還款分潤

借款人還款時，本金與利息在**同一個 transaction 內**按投資占比分給出借人，
入帳到可用餘額（可再投入其他標的）。

分配演算法（`backend/cmd/api/distribution.go`）：

1. 依 `投資金額 / 標的募資總額` 的占比向下取整分配
2. 捨入殘差逐一補給投資金額最大者（相對誤差最小；金額相同時依 investment id 保持決定性）
3. 因此 `sum(分配結果) == 待分配金額` 在任何占比下都成立

關鍵性質：

- **金額守恆**：出借人餘額增加總額 == 借款人繳款金額，零誤差
- **全期繳畢後出借人收回全部本金**（逐期捨入的偏差不累積）
- **提前清償只分配本金**：未到期利息已免除，不會分給出借人
- 重複還款被冪等鍵擋下時不會重複分潤
- 背後沒有投資紀錄的合約（seed 或舊資料）不分潤，還款仍正常完成

出借人可查詢 `GET /v1/investments/distributions` 看每筆收款明細。
持倉頁同時顯示「已收利息」與「預估總收益」，差距即為尚未實現的部分。

攤還期數的狀態機：

```text
scheduled --(繳款日到期)--> due --(還款)--> paid
due       --(繳款日過期)--> overdue --(補繳)--> paid
```

`paid` 為終局狀態。規則：

- **只有 `due` 或 `overdue` 的期數可以還款**，未到期期數會回 422（要一次付完請用提前清償）
- **冪等保護**：`repayments.idempotency_key` 有唯一約束，同一期重複繳款回 409，不會重複落帳
- 繳畢一期後，下一期自動由 `scheduled` 轉為 `due`
- 全期繳畢時合約自動轉為「已結清」並寫入 `settled_at`
- 逾期判定以資料庫 `CURRENT_DATE` 為基準（避免應用伺服器時區誤判），繳款日當天不算逾期
- 逾期掃描在 API 啟動時執行一次，之後每 24 小時重跑；催收清單查詢前也會即時刷新

催收階段：M1 未滿 30 天、M2 30–59 天、M3+ 60 天以上。

## 資料庫 Migration

`backend/migrations/*.sql` 依檔名順序套用，每支只執行一次，
記錄於 `schema_migrations`（版本、內容 checksum、套用時間）。

```bash
./scripts/migrate.sh develop            # 套用未執行過的 migration
./scripts/migrate.sh develop --status    # 只顯示待套用清單，不執行
```

規則：

- **已套用的 migration 不可修改**。checksum 不符時 `migrate.sh` 會失敗並說明原因，
  因為各環境的 schema 會因此分歧。需要調整時請新增一支 migration
- **版本表導入前的既有環境會自動 bootstrap**：偵測到 `users` 表存在但版本表為空時，
  把既有 migration 標記為已套用而不重跑
- 版本表本身由 `migrate.sh` 直接建立（不能依賴自己來記錄自己），
  `008_schema_migrations.sql` 則讓全新環境也能在檔案中看到完整歷程

> 在導入版本表之前，每次部署都重跑所有 migration，因此舊檔案被迫預知未來的變更——
> 002 的 CHECK 約束必須含有 005 才引入的狀態值，否則重跑時會被既有資料列違反
> （曾實際造成部署失敗）。這與 proxy 的 allowlist 是同一類問題：
> 改動 A 需要同時改動不相關的 B。

整合測試不經過版本表：它每次啟動都 `DROP SCHEMA public CASCADE` 再從零套用，
因此每次執行都在驗證「全新安裝」這條路徑。

## 清單分頁

申請清單端點支援 `?limit=` 與 `?offset=`，回傳 `{ items, page }`：

```json
{
  "items": [ ... ],
  "page": { "total": 128, "limit": 50, "offset": 0, "hasMore": true }
}
```

- 預設 `limit=50`，上限 `200`（單一請求不該能拖垮資料庫或回應體積）
- **無效或超出範圍的參數一律夾到合法區間而非回錯誤**：
  分頁是呈現層細節，不該讓整個請求失敗
- `total` 反映套用篩選後的總筆數，因此與 `?status=` 可同時使用

適用於 `GET /v1/applications` 與 `GET /v1/admin/applications`。

## API Proxy

瀏覽器只跟 Nuxt 說話（`frontend/server/api/[...path].ts`），Go API 不對外曝露。

轉發策略是**預設全部轉發、明確排除少數**：

- **請求 header**：排除 hop-by-hop（`connection`、`transfer-encoding` 等）、
  `host`（指向 Nuxt 而非後端）、`content-length`（由 fetch 重算）、
  `x-forwarded-for`（另行組鏈）
- **回應 header**：排除 hop-by-hop、`set-cookie`（需逐筆 append）、
  `content-encoding` 與 `content-length`（$fetch 解壓後已不符，由 Nitro 重算）
- **請求主體**：一律以原始位元組轉發，不解析。JSON 與 multipart 都只是位元組，
  該解析它的是後端
- **回應主體**：依回應實際的 `content-type` 決定是否轉為字串，**不看請求路徑**

> 這支檔案曾因 allowlist 造成五次靜默失效（cookie、CSRF token、`Retry-After`、
> `X-Forwarded-For`、multipart），每次都是後端新增了有語意的 header 或內容型別，
> proxy 卻不知道要轉發。改為排除清單後，後端新增的 header 預設就能通過；
> 端到端驗證有一節專門鎖住這個性質。

唯一刻意加工的是 `X-Forwarded-For`：把 proxy 觀察到的對端位址附加在鏈尾，
後端取最後一跳，因此客戶端偽造的前綴會被忽略。

## 帳號生命週期

### Email 驗證

註冊後自動寄出驗證信。**未驗證不阻擋登入**（否則使用者無法要求重寄），
但**不可送出貸款申請**——授信與撥款必須先確認信箱屬於本人。

- 驗證連結有效 24 小時，**僅能使用一次**
- 重新寄送會讓先前的連結失效
- 既有帳號由 migration 標記為已驗證（回溯要求會把現有使用者鎖在門外）

### 密碼重設

- `POST /v1/auth/forgot-password` 對**未註冊的信箱也回傳成功**，
  否則這個端點會變成帳號列舉工具
- 重設連結有效 1 小時（比驗證信短：它能直接接管帳號），僅能使用一次
- **重設成功後撤銷該帳號的所有 session**：帳號若已被接管，改密碼必須同時把對方踢出
- token 只存 SHA-256 雜湊（與 session 相同策略），資料庫外洩不可用於重設
- 驗證與重設的 token **用途隔離**：驗證 token 不能用來重設密碼，反之亦然
- 兩個端點都套用限流，避免被用來轟炸他人信箱

這三個端點（`verify-email`、`forgot-password`、`reset-password`）是 CSRF 豁免的：
呼叫它們的人尚未登入，要求 CSRF token 會讓流程無法使用。
防護來自 token 本身是高熵且單次使用的秘密。

### 郵件

以標準庫 `net/smtp` 寄送，不新增依賴。`SMTP_HOST` 留空時**不寄信而是寫入日誌**，
讓沒有郵件伺服器的環境仍能測試完整流程。

develop 環境的 compose 會啟動 **MailHog**（SMTP sink + HTTP API），
因此端到端驗證能真的收信、取出連結並走完流程，而非假設它會動：

```bash
# 網頁介面查看攔截到的信件
open http://localhost:18025
```

信件標頭值會清掉 CR/LF，避免標頭注入（夾帶額外收件人）。

## 安全機制

### PII 加密

身分證字號與電話以 **AES-256-GCM** 加密後才寫入資料庫（`applications.id_number_enc` / `phone_enc`）。
GCM 同時提供機密性與完整性，被篡改的密文會在解密時失敗而非回傳錯誤資料。

- 金鑰由 `PII_ENCRYPTION_KEY` 注入（base64 編碼的 32 bytes），**絕不進版控**
- `production` 缺金鑰時 API **拒絕啟動**；`develop` 會退回內建測試金鑰並記錄警告
- 每次加密使用新的隨機 nonce，故同一明文兩次加密的密文不同（無法以密文比對推測是否同一人）
- API 回應一律是遮罩形式（`A12****789` / `****678`）
- 需完整值時走 `POST /v1/admin/applications/{id}/reveal`，**必須帶 reason**，
  每次呼叫都寫入 `pii_access_log` 稽核表

### 收入最小化揭露

年收入與月支出**不以原值回傳 API**，改為級距（`120～200 萬`、`3～5 萬`）。

這兩個欄位被 DBR 計算使用，每筆清單查詢都要讀，因此不適合像身分證那樣加密
（熱路徑會多 N 次解密，且 SQL 層無法以收入篩選或排序）。
資料庫維持明文，但 API 只給風控判讀真正需要的資訊：**DBR 與級距**。

完整金額與身分證、電話一樣走 `reveal` 稽核端點，同樣要求理由並記錄。

金鑰產生：`openssl rand -base64 32`（或由 `deploy.sh --auto-secrets` 自動產生）。

既有明文資料由 API 啟動時的一次性搬遷處理（加密需要金鑰，SQL migration 無法完成）。
因 `deploy.sh` 先啟動容器才跑 migration，部署腳本會在 migration 後重啟 API 讓搬遷完成。

### CSRF

採 double-submit cookie：後端下發可被 JS 讀取的 `creditflow_csrf` cookie，
前端把值放進 `X-CSRF-Token` header。跨站頁面受同源政策限制讀不到 cookie，
因此無法補上相符的 header。

- session cookie 為 `HttpOnly` + `SameSite=Strict`，與 CSRF token 形成雙重防護
- 所有非 GET/HEAD/OPTIONS 請求都需要 token，缺少或不符回 **403**
- `/v1/auth/login` 與 `/v1/auth/register` 為豁免端點（尚無 session，CSRF 無實質意義）
- 登入／註冊成功後輪替 token，避免攻擊者預先植入已知值
- 前端由 `useCsrf().mutate()` 統一處理，呼叫端不需自行管理

### 登入失敗鎖定

IP 限流擋不住輪替 IP 的攻擊者，因此另外按**帳號**記錄失敗次數：

| 累計失敗 | 鎖定時長 |
| --- | --- |
| 5 次 | 5 分鐘 |
| 10 次 | 30 分鐘 |
| 15 次 | 2 小時 |
| 之後每 5 次 | 加倍，上限 24 小時 |

- **鎖定期間即使密碼正確也拒絕**：否則攻擊者猜中密碼時鎖定就形同虛設
- **成功登入清空計數**：合法使用者偶爾打錯不該累積到被鎖
- **密碼重設成功即解除鎖定**：擁有者已透過信件證明身分
- 回 **429 + `Retry-After`**，訊息刻意不提及帳號——
  「被鎖定」這個回應本身若只對存在的帳號出現，就成了帳號存在的證據
- 記錄最後失敗時間與來源 IP，供風控查閱異常樣態

> **為何是暫時鎖定而非永久**：永久鎖定會讓攻擊者能以故意輸錯密碼
> 封鎖任意帳號，把防護本身變成 DoS 工具。遞增時長在防護與可用性之間取平衡，
> 且合法擁有者隨時能以密碼重設立即解鎖。

### 限流

記憶體 token bucket，依來源 IP 計數：

| 範圍 | 預設值 | 環境變數 |
| --- | --- | --- |
| 全域 | 300 次/分鐘 | `RATE_LIMIT_PER_MINUTE` |
| 登入 | 20 次/分鐘 | `AUTH_RATE_LIMIT_PER_MINUTE` |
| 註冊 | 20 次/分鐘 | 同上（與登入**分開計桶**） |

超限回 **429** 並帶 `Retry-After`。登入與註冊各自計桶，避免正常註冊擠掉登入配額。

> **限制**：限流狀態只存在單一行程內。多副本部署時每個副本各自計數，
> 實際允許量為設定值乘以副本數。要精確限流需改用 Redis 等共享存放。

來源 IP 取 `X-Forwarded-For` 的**最後一段**（最接近本服務的一跳）而非第一段 ——
客戶端可偽造前綴，但最後一跳由我們自己的 proxy 附加。可用 `TRUST_PROXY=false` 關閉。

## 驗證

統一入口（CI 與本機使用同一組指令）：

```bash
./scripts/check.sh              # 不需資料庫的全部檢查（後端 + 前端）
./scripts/check.sh backend      # gofmt、build、vet、單元測試
./scripts/check.sh frontend     # typecheck、build
./scripts/check.sh integration  # 整合測試（需要 PostgreSQL）
./scripts/check.sh all          # 全部 + 整合測試
```

有 `make` 的環境也可用 `make check` / `make verify` / `make help`（見 `Makefile`）。

### 測試分層

| 層次 | 指令 | 數量 | 需要 |
| --- | --- | --- | --- |
| 單元測試 | `go test ./...` | 153 斷言 | 無 |
| 整合測試 | `go test -tags=integration ./...` | 230+ 斷言 | PostgreSQL |
| 端到端 | `./scripts/verify-flow.sh` | 215 斷言 | 完整 docker 環境 |

**單元測試**涵蓋純函式：PMT 與攤還表不變量、密碼雜湊、加解密與遮罩、
token bucket、CSRF 比對、逾期天數與催收階段。

**整合測試**對真實 PostgreSQL 驗證 DB 互動、middleware 與 transaction 邊界：

- 認證：註冊／登入／登出、session 於伺服器端失效、過期 session 被拒、
  email 大小寫不敏感、角色無法自我提升
- 授權：匿名 401、跨角色 403、跨使用者一律 404（不洩漏存在性）
- middleware：CSRF 四種繞過方式、限流 429 與 `Retry-After`、
  登入與註冊分桶、request ID 傳遞、路由邊界
- **併發正確性**：5 個並行核准只建立 1 個標的、8 個並行繳款只落 1 筆帳、
  8 個並行投標不超募且只撥款一次（`FOR UPDATE` + 冪等鍵 + CHECK 約束三層保護）
- 業務流程：審核閉環、婉拒不生成合約、孤兒申請不可核准、
  逐期繳完自動結清、提前清償免除未到期利息、逾期判定與催收階段
- PII：密文不含明文、同明文密文相異、遮罩回傳、reveal 需理由且寫稽核、
  舊明文搬遷可重複執行
- 撮合：核准只上架不發約、超額投標被裁切、募滿自動撥款、
  餘額不足被拒、角色邊界（borrower/reviewer 皆不可投標）
- 分潤：單期分潤總額等於實收、多位出借人占比正確、全期繳畢收回全部本金、
  清償不分配免除利息、重複還款不重複分潤
- 檔案上傳：內容 byte-for-byte 往返一致、偽造副檔名被拒、路徑穿越被拒、
  超過大小與數量上限被拒、決議後鎖定、跨角色權限邊界
- Proxy 透通性：後端 header 自動穿透、hop-by-hop 不轉發、
  content-length 與實際 body 一致、自訂 request header 可達後端
- 揭露與分頁：收入原值不出現在清單、DBR 仍正確、reveal 可取得完整值、
  limit 夾住上限、無效參數不致失敗、相鄰頁面無重複
- 募資期限：14 天期限、退款金額等於投入金額、餘額完全回復、重複取消被拒、
  已取消標的拒絕投標、取消後申請回到可重新處理
- OCR：上傳不被阻塞、背景完成辨識、相符文件回報 matched、
  無關文件回報 not_found（未誤判）
- 信件流程（經 MailHog 實際收信）：驗證信寄達並可完成驗證、未驗證不可申請、
  忘記密碼不洩漏帳號存在性、重設後舊 session 被撤銷與舊密碼失效、token 不可重用
- 登入鎖定：達門檻回 429 與 Retry-After、鎖定期間正確密碼也被拒、
  不存在的帳號回應不變、密碼重設可解除鎖定

執行整合測試需要一個**獨立的測試資料庫**（測試會 TRUNCATE 所有業務資料表）：

```bash
./scripts/test-db.sh develop    # 在 develop 的 PostgreSQL 中建立 creditflow_test
./scripts/check.sh integration  # 自動從環境檔推導連線字串
```

未設定 `TEST_DATABASE_URL` 或連不上資料庫時，整合測試會**跳過而非失敗**，
讓沒有 DB 的環境仍能跑單元測試。CI 另有一步專門確認整合測試真的執行了，
避免「綠燈卻什麼都沒驗證」。

### CI

`.github/workflows/ci.yml` 有四個 job：

| Job | 內容 |
| --- | --- |
| `backend` | gofmt、build、vet（含 integration tag）、單元測試（`-race`） |
| `integration` | PostgreSQL service container + 整合測試（`-race`）+ 確認未被跳過 |
| `frontend` | `npm ci`、typecheck、build |
| `end-to-end` | `deploy.sh` 啟動完整環境 + `verify-flow.sh`；失敗時輸出服務日誌 |

## 尚未實作

以下功能目前只有 UI，點擊會明確提示「尚未開放」，不會產生任何資料變更：

- 機器學習風控模型（目前為規則式評分）

還款引擎的已知限制：

- 還款只記錄交易，**未接任何金流閘道**（沒有實際扣款）
- 不支援部分繳款：一期只能全額繳納
- 逾期補繳後該期轉為 `paid`，**違約歷史不會保留**，因此還款率會回到 100%。
  真實信貸系統需額外保存違約記錄以影響信用評分
- 催收清單只呈現建議處理方式，沒有聯繫紀錄與委外流程

仍待處理：

