#!/usr/bin/env bash
# 套用資料庫 migration。
#
# 每支 migration 只會套用一次，記錄於 schema_migrations 表。
# 在導入版本表之前，本腳本每次都重跑所有檔案，因此舊的 migration 被迫
# 預知未來的變更（例如 002 的 CHECK 必須含有 005 才引入的狀態值）。
#
# 用法：
#   ./scripts/migrate.sh <develop|production>
#   ./scripts/migrate.sh develop --status    # 只顯示狀態，不套用

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

environment=""
status_only=0

while (( $# > 0 )); do
  case "$1" in
    --status) status_only=1 ;;
    -*) fail "未知的選項：$1" ;;
    *) environment="$1" ;;
  esac
  shift
done

[[ -n "$environment" ]] || fail "用法：./scripts/migrate.sh <develop|production> [--status]"
require_docker_compose
require_environment "$environment"
prepare_env_file

compose up -d db
wait_for_database 45

# 版本表本身必須先存在才能記錄其他 migration，因此直接建立而不經由記錄機制。
# 內容與 008_schema_migrations.sql 一致（該檔案讓全新環境也能看到完整歷程）。
ensure_version_table() {
  sql_exec "
    CREATE TABLE IF NOT EXISTS schema_migrations (
        version TEXT PRIMARY KEY,
        checksum TEXT NOT NULL,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );
  "
}

checksum_of() {
  sha256sum "$1" | cut -d' ' -f1
}

# bootstrap_existing_schema 處理「版本表導入前就已存在」的資料庫。
#
# 這些環境已套用過 001–007，重跑會失敗（舊版 CHECK 約束會被既有資料違反）。
# 以 users 表是否存在作為判斷依據：它由 002 建立，若存在則表示既有 migration
# 都已套用過，直接記錄為已完成而不重跑。
bootstrap_existing_schema() {
  local recorded
  recorded="$(sql_query "SELECT COUNT(*) FROM schema_migrations;" | tr -d '[:space:]')"
  [[ "$recorded" == "0" ]] || return 0

  local has_users
  has_users="$(sql_query "
    SELECT COUNT(*) FROM information_schema.tables
    WHERE table_schema = 'public' AND table_name = 'users';
  " | tr -d '[:space:]')"
  [[ "$has_users" == "1" ]] || return 0

  log "偵測到版本表導入前既有的 schema，將既有 migration 標記為已套用"
  local migration version checksum
  for migration in "${migration_files[@]}"; do
    version="$(basename "$migration")"
    checksum="$(checksum_of "$migration")"
    sql_exec "
      INSERT INTO schema_migrations (version, checksum)
      VALUES ($(sql_literal "$version"), $(sql_literal "$checksum"))
      ON CONFLICT (version) DO NOTHING;
    "
    log "  已標記：$version"
  done
}

shopt -s nullglob
migration_files=("$PROJECT_ROOT"/backend/migrations/*.sql)
(( ${#migration_files[@]} > 0 )) || fail "backend/migrations 沒有 SQL migration"

ensure_version_table
bootstrap_existing_schema

applied=0
skipped=0

for migration in "${migration_files[@]}"; do
  version="$(basename "$migration")"
  checksum="$(checksum_of "$migration")"

  recorded_checksum="$(sql_query "
    SELECT checksum FROM schema_migrations
    WHERE version = $(sql_literal "$version");
  " | tr -d '[:space:]')"

  if [[ -n "$recorded_checksum" ]]; then
    # 已套用的 migration 被修改，表示各環境的 schema 可能已分歧。
    # 正確做法是新增一支 migration，而不是改舊的。
    if [[ "$recorded_checksum" != "$checksum" ]]; then
      fail "$version 已於此資料庫套用過，但內容已變更（checksum 不符）。
  已套用：$recorded_checksum
  目前檔案：$checksum
  已套用的 migration 不可修改；請改為新增一支 migration。
  若確定要覆寫（僅限開發環境），執行：
    DELETE FROM schema_migrations WHERE version = '$version';"
    fi
    skipped=$(( skipped + 1 ))
    continue
  fi

  if (( status_only )); then
    log "待套用：$version"
    applied=$(( applied + 1 ))
    continue
  fi

  log "執行 migration：$version"
  sql_in_db "$migration"
  sql_exec "
    INSERT INTO schema_migrations (version, checksum)
    VALUES ($(sql_literal "$version"), $(sql_literal "$checksum"));
  "
  applied=$(( applied + 1 ))
done

if (( status_only )); then
  log "狀態檢查完成：$applied 支待套用，$skipped 支已套用"
  exit 0
fi

log "migration 完成：套用 $applied 支，跳過 $skipped 支"
