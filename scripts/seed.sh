#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

environment="${1:-}"
[[ -n "$environment" ]] || fail "用法：./scripts/seed.sh <develop|production>"
require_command docker
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
