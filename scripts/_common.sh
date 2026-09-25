#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DOCKER_DIR="$PROJECT_ROOT/docker"
ENV_DIR="$DOCKER_DIR/envs"
COMPOSE_COMMAND=()

log() {
  printf '[creditflow] %s\n' "$*"
}

fail() {
  printf '[creditflow] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "找不到必要指令：$1"
}

require_docker_compose() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    COMPOSE_COMMAND=(docker compose)
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_COMMAND=(docker-compose)
    return 0
  fi
  fail "找不到 Docker Compose：請安裝 docker compose 或 docker-compose"
}

require_environment() {
  local environment="${1:-}"
  [[ -n "$environment" ]] || fail "請提供環境，例如：develop 或 production"
  case "$environment" in
    develop|production) ;;
    *) fail "不支援的環境：$environment（可用 develop、production）" ;;
  esac
  ENVIRONMENT="$environment"
  ENV_FILE="$ENV_DIR/.env.$environment"
  EXAMPLE_FILE="$ENV_DIR/.env.$environment.example"
  COMPOSE_FILE="$DOCKER_DIR/docker-compose.yml"
  OVERRIDE_FILE="$DOCKER_DIR/docker-compose.$environment.yml"
  [[ -f "$EXAMPLE_FILE" ]] || fail "找不到環境樣板：$EXAMPLE_FILE"
  COMPOSE_FILES=(-f "$COMPOSE_FILE")
  [[ -f "$OVERRIDE_FILE" ]] && COMPOSE_FILES+=(-f "$OVERRIDE_FILE")
}

prepare_env_file() {
  if [[ ! -f "$ENV_FILE" ]]; then
    cp "$EXAMPLE_FILE" "$ENV_FILE"
    log "已從樣板建立 runtime env：$ENV_FILE"
    log "敏感值目前是 placeholder；部署時請使用 --auto-secrets 或自行修改"
  else
    sync_env_with_example
  fi
  cp "$ENV_FILE" "$DOCKER_DIR/.env"
}

# sync_env_with_example 把樣板新增的鍵補進既有 runtime env。
#
# 沒有這一步，新版程式碼新增的環境變數永遠不會出現在既有環境的 runtime env
# （prepare_env_file 只在檔案不存在時才複製），部署後會以預設值靜默啟動或直接失敗。
# 既有鍵的值一律保留，不覆蓋使用者自訂內容。
sync_env_with_example() {
  local added=()
  local line key
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* || "$line" != *=* ]] && continue
    key="${line%%=*}"
    if ! grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
      printf '%s\n' "$line" >> "$ENV_FILE"
      added+=("$key")
    fi
  done < "$EXAMPLE_FILE"

  if (( ${#added[@]} > 0 )); then
    log "已從樣板補入新環境變數：${added[*]}"
    log "如含敏感值，請使用 --auto-secrets 或手動填入 $ENV_FILE"
  fi
}

compose() {
  "${COMPOSE_COMMAND[@]}" --env-file "$DOCKER_DIR/.env" "${COMPOSE_FILES[@]}" "$@"
}

wait_for_database() {
  local attempts="${1:-30}"
  local attempt=1
  while (( attempt <= attempts )); do
    if compose exec -T db sh -c 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    ((attempt += 1))
  done
  fail "PostgreSQL 在等待時間內沒有 ready"
}

wait_for_api() {
  local attempts="${1:-30}"
  local attempt=1
  while (( attempt <= attempts )); do
    if compose exec -T api wget --quiet --output-document=/dev/null http://127.0.0.1:8080/health >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    ((attempt += 1))
  done
  fail "Go API 在等待時間內沒有 ready"
}

sql_in_db() {
  local sql_file="$1"
  [[ -f "$sql_file" ]] || fail "找不到 SQL 檔案：$sql_file"
  compose exec -T db sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < "$sql_file"
}

# sql_query <sql> → 以無標頭、無對齊的格式印出查詢結果。
# 供腳本讀取單一值（例如版本表的內容）使用。
sql_query() {
  local statement="$1"
  compose exec -T db sh -c \
    'psql -v ON_ERROR_STOP=1 -tA -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<<"$statement"
}

# sql_exec <sql> → 執行不需回傳值的敘述。
sql_exec() {
  local statement="$1"
  compose exec -T db sh -c \
    'psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<<"$statement" >/dev/null
}

# sql_literal <value> → 轉義為 SQL 字串常量（單引號加倍）。
sql_literal() {
  printf "'%s'" "${1//\'/\'\'}"
}
