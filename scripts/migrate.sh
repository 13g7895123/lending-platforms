#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

environment="${1:-}"
[[ -n "$environment" ]] || fail "用法：./scripts/migrate.sh <develop|production>"
require_command docker
require_environment "$environment"
prepare_env_file

compose up -d db
wait_for_database 45

shopt -s nullglob
migration_files=("$PROJECT_ROOT"/backend/migrations/*.sql)
(( ${#migration_files[@]} > 0 )) || fail "backend/migrations 沒有 SQL migration"

for migration in "${migration_files[@]}"; do
  log "執行 migration：$(basename "$migration")"
  sql_in_db "$migration"
done

log "migration 完成"
