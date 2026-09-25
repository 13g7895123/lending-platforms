#!/usr/bin/env bash
# 統一的驗證入口：CI 與本機執行同一組指令。
#
# 用法：
#   ./scripts/check.sh              # 不需資料庫的全部檢查
#   ./scripts/check.sh backend      # 只跑後端
#   ./scripts/check.sh frontend     # 只跑前端
#   ./scripts/check.sh integration  # 只跑整合測試（需要 PostgreSQL）
#   ./scripts/check.sh all          # 全部 + 整合測試
#
# 環境變數：
#   TEST_DATABASE_URL  整合測試的資料庫；未設定時會從 develop 環境檔推導
#   ENVIRONMENT        推導連線字串時使用的環境（預設 develop）

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

BACKEND_DIR="$PROJECT_ROOT/backend"
FRONTEND_DIR="$PROJECT_ROOT/frontend"
ENVIRONMENT="${ENVIRONMENT:-develop}"

FAILED=()

heading() { printf '\n\033[1m━━━ %s ━━━\033[0m\n' "$*"; }
pass()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
failure() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAILED+=("$1"); }

# run <label> <command...>
run() {
  local label="$1"; shift
  local output
  if output="$("$@" 2>&1)"; then
    pass "$label"
    return 0
  fi
  failure "$label"
  printf '%s\n' "$output" | sed 's/^/      /' | tail -25
  return 1
}

# derive_test_database_url 由環境檔推導測試資料庫連線字串。
derive_test_database_url() {
  local env_file="$PROJECT_ROOT/docker/envs/.env.$ENVIRONMENT"
  [[ -f "$env_file" ]] || return 1
  local user password port
  user="$(grep -s '^POSTGRES_USER=' "$env_file" | cut -d= -f2-)"
  password="$(grep -s '^POSTGRES_PASSWORD=' "$env_file" | cut -d= -f2-)"
  port="$(grep -s '^POSTGRES_PORT=' "$env_file" | cut -d= -f2-)"
  [[ -n "$user" && -n "$password" && -n "$port" ]] || return 1
  printf 'postgres://%s:%s@localhost:%s/creditflow_test?sslmode=disable' \
    "$user" "$password" "$port"
}

check_backend() {
  heading "後端"

  local unformatted
  unformatted="$(cd "$BACKEND_DIR" && gofmt -l . 2>/dev/null)"
  if [[ -n "$unformatted" ]]; then
    failure "gofmt"
    printf '%s\n' "$unformatted" | sed 's/^/      /'
  else
    pass "gofmt"
  fi

  run "go build"               bash -c "cd '$BACKEND_DIR' && go build ./..."
  run "go vet"                 bash -c "cd '$BACKEND_DIR' && go vet ./..."
  run "go vet (integration)"   bash -c "cd '$BACKEND_DIR' && go vet -tags=integration ./..."
  run "go test（單元）"         bash -c "cd '$BACKEND_DIR' && go test ./..."
}

check_frontend() {
  heading "前端"
  if [[ ! -d "$FRONTEND_DIR/node_modules" ]]; then
    failure "node_modules 不存在（請先執行 npm ci）"
    return
  fi
  run "nuxt typecheck" bash -c "cd '$FRONTEND_DIR' && npx nuxt typecheck"
  run "nuxt build"     bash -c "cd '$FRONTEND_DIR' && npx nuxt build"
}

check_integration() {
  heading "整合測試"
  local database_url="${TEST_DATABASE_URL:-}"
  if [[ -z "$database_url" ]]; then
    database_url="$(derive_test_database_url || true)"
  fi
  if [[ -z "$database_url" ]]; then
    printf '  \033[33m!\033[0m 找不到測試資料庫連線字串；設定 TEST_DATABASE_URL 或執行 ./scripts/test-db.sh\n'
    return
  fi
  run "go test -tags=integration" \
    bash -c "cd '$BACKEND_DIR' && TEST_DATABASE_URL='$database_url' go test -tags=integration -count=1 ./..."
}

target="${1:-check}"
case "$target" in
  backend)     check_backend ;;
  frontend)    check_frontend ;;
  integration) check_integration ;;
  check)       check_backend; check_frontend ;;
  all)         check_backend; check_frontend; check_integration ;;
  *)
    printf 'error: 未知的目標「%s」\n' "$target" >&2
    printf '可用目標：backend, frontend, integration, check, all\n' >&2
    exit 2
    ;;
esac

printf '\n'
if (( ${#FAILED[@]} > 0 )); then
  printf '\033[1;31m✗ %d 項檢查失敗：%s\033[0m\n' "${#FAILED[@]}" "${FAILED[*]}"
  exit 1
fi
printf '\033[1;32m✓ 全部檢查通過\033[0m\n'
