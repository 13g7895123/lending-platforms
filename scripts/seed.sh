#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

environment="${1:-}"
[[ -n "$environment" ]] || fail "用法：./scripts/seed.sh <develop|production>"
# seed 會建立密碼公開的展示帳號（含 reviewer）並在每次執行時重設其密碼與餘額，
# 套用到正式環境等同開放風控後台給所有人。
[[ "$environment" != "production" ]] || fail "seed 僅供展示環境，不可套用到 production"
require_docker_compose
require_environment "$environment"
prepare_env_file

compose up -d db
wait_for_database 45

shopt -s nullglob
seed_files=("$PROJECT_ROOT"/backend/seed/*.sql)
(( ${#seed_files[@]} > 0 )) || fail "backend/seed 沒有 SQL seed"

for seed_file in "${seed_files[@]}"; do
  log "執行 seed：$(basename "$seed_file")"
  sql_in_db "$seed_file"
done

log "seed 完成"
