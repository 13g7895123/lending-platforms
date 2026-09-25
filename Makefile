# CreditFlow 開發與驗證指令入口。
# CI 與本機使用同一組指令，避免「CI 過了本機沒過」或反之。

SHELL := /bin/bash
.DEFAULT_GOAL := help

BACKEND_DIR := backend
FRONTEND_DIR := frontend
ENVIRONMENT ?= develop

# 整合測試用的資料庫。預設指向 develop 環境旁的獨立測試庫，
# 避免整合測試的 TRUNCATE 清掉開發資料。
TEST_DATABASE_URL ?= postgres://creditflow:$(shell grep -s '^POSTGRES_PASSWORD=' docker/envs/.env.$(ENVIRONMENT) | cut -d= -f2-)@localhost:$(shell grep -s '^POSTGRES_PORT=' docker/envs/.env.$(ENVIRONMENT) | cut -d= -f2- || echo 5432)/creditflow_test?sslmode=disable

.PHONY: help
help: ## 顯示可用指令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- 後端

.PHONY: backend-fmt
backend-fmt: ## 檢查 Go 格式（不修改檔案）
	@output="$$(cd $(BACKEND_DIR) && gofmt -l .)"; \
	if [[ -n "$$output" ]]; then \
		echo "以下檔案格式不符，請執行 make backend-fmt-fix："; echo "$$output"; exit 1; \
	fi
	@echo "gofmt: 格式正確"

.PHONY: backend-fmt-fix
backend-fmt-fix: ## 自動修正 Go 格式
	cd $(BACKEND_DIR) && gofmt -w .

.PHONY: backend-vet
backend-vet: ## 執行 go vet（含整合測試的 build tag）
	cd $(BACKEND_DIR) && go vet ./...
	cd $(BACKEND_DIR) && go vet -tags=integration ./...

.PHONY: backend-test
backend-test: ## 執行後端單元測試
	cd $(BACKEND_DIR) && go test ./...

.PHONY: backend-build
backend-build: ## 編譯後端
	cd $(BACKEND_DIR) && go build ./...

.PHONY: test-integration
test-integration: ## 執行整合測試（需要 PostgreSQL；無 DB 時會跳過）
	cd $(BACKEND_DIR) && TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags=integration -count=1 ./...

.PHONY: test-db-create
test-db-create: ## 在 develop 的 PostgreSQL 中建立測試資料庫
	./scripts/test-db.sh $(ENVIRONMENT)

# ---------------------------------------------------------------- 前端

.PHONY: frontend-install
frontend-install: ## 安裝前端依賴（CI 用 npm ci）
	cd $(FRONTEND_DIR) && npm ci

.PHONY: frontend-typecheck
frontend-typecheck: ## 前端型別檢查
	cd $(FRONTEND_DIR) && npx nuxt typecheck

.PHONY: frontend-build
frontend-build: ## 建置前端
	cd $(FRONTEND_DIR) && npx nuxt build

# ---------------------------------------------------------------- 組合指令

.PHONY: backend
backend: backend-fmt backend-vet backend-test backend-build ## 後端全部檢查

.PHONY: frontend
frontend: frontend-typecheck frontend-build ## 前端全部檢查

.PHONY: check
check: backend frontend ## 不需資料庫的全部檢查

.PHONY: verify
verify: check test-integration ## 全部檢查 + 整合測試

.PHONY: verify-flow
verify-flow: ## 端到端驗證（需要 develop 環境已啟動）
	./scripts/verify-flow.sh

# ---------------------------------------------------------------- 環境

.PHONY: up
up: ## 啟動 develop 環境
	./scripts/deploy.sh $(ENVIRONMENT) --auto-secrets

.PHONY: down
down: ## 停止環境
	docker compose --env-file docker/.env \
		-f docker/docker-compose.yml \
		-f docker/docker-compose.$(ENVIRONMENT).yml down

.PHONY: logs
logs: ## 追蹤 API 日誌
	docker compose --env-file docker/.env \
		-f docker/docker-compose.yml \
		-f docker/docker-compose.$(ENVIRONMENT).yml logs -f api
