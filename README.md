# CreditFlow 信達金融

CreditFlow 是一個 Nuxt 前端、Go API 與 PostgreSQL 的借貸平台展示專案。原本的單頁 HTML 已改成 Vue/Nuxt 狀態管理；申請送出、儀表板與投資市集會透過 Nuxt 同源 API proxy 連到 Go 後端。

## 技術結構

```text
frontend/              Nuxt 3 + Vue 3
backend/cmd/api/       Go HTTP API
backend/migrations/    PostgreSQL schema
backend/seed/          PostgreSQL demo data
docker/                compose 與環境樣板
scripts/               deploy / migrate / seed
```

## 啟動環境

需要 Docker Compose v2。第一次啟動 develop 環境時，部署腳本會從 example 建立 runtime env；本機展示可直接讓腳本產生弱敏感值的替代值：

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
docker compose --env-file docker/.env \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.develop.yml ps

docker compose --env-file docker/.env \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.develop.yml logs -f api
```

環境檔規則：只提交 `docker/envs/.env.<env>.example`；`docker/.env` 與 `docker/envs/.env.<env>` 都是 runtime 檔案，已加入 ignore。`deploy.sh` 會在 compose 啟動前複製環境檔並檢查弱敏感值；可用 `--auto-secrets` 自動產生，或用 `--skip-secrets` 明確跳過（production 另需 `--i-know-what-im-doing`）。

## API

- `GET /health`：API 與 PostgreSQL readiness
- `GET /v1/dashboard`：儀表板摘要與貸款合約
- `GET /v1/market/listings`：投資市集標的
- `POST /v1/applications`：建立貸款申請
