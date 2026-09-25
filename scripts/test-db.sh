#!/usr/bin/env bash
# 在既有環境的 PostgreSQL 中建立整合測試用的獨立資料庫。
#
# 整合測試會 TRUNCATE 所有業務資料表，因此必須與開發資料庫分開。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

environment="${1:-develop}"
test_database="${2:-creditflow_test}"

require_docker_compose
require_environment "$environment"
prepare_env_file

compose up -d db
wait_for_database 45

log "建立測試資料庫：$test_database"
# 已存在時 psql 會回錯，以 || true 容忍
compose exec -T db psql -U "${POSTGRES_USER:-creditflow}" -d postgres \
  -c "CREATE DATABASE ${test_database};" 2>/dev/null || log "測試資料庫已存在，略過建立"

log "完成。整合測試連線字串："
port="$(grep -s '^POSTGRES_PORT=' "$ENV_FILE" | cut -d= -f2- || echo 5432)"
password="$(grep -s '^POSTGRES_PASSWORD=' "$ENV_FILE" | cut -d= -f2-)"
user="$(grep -s '^POSTGRES_USER=' "$ENV_FILE" | cut -d= -f2- || echo creditflow)"
printf '  postgres://%s:%s@localhost:%s/%s?sslmode=disable\n' \
  "$user" "$password" "$port" "$test_database"
