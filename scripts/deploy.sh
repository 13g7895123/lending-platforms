#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/_common.sh
source "$SCRIPT_DIR/_common.sh"

usage() {
  cat <<'EOF'
用法：./scripts/deploy.sh <develop|production> [選項]

選項：
  --auto-secrets       自動替換環境檔中的弱敏感值
  --skip-secrets       明確跳過弱敏感值檢查（production 需再加確認旗標）
  --fail-on-weak       偵測弱敏感值時直接失敗（預設行為）
  --i-know-what-im-doing  允許 production 使用 --skip-secrets
EOF
}

environment="${1:-}"
if [[ -z "$environment" || "$environment" == "-h" || "$environment" == "--help" ]]; then
  usage
  exit $([[ -z "$environment" ]] && echo 1 || echo 0)
fi
shift

AUTO_SECRETS=0
SKIP_SECRETS=0
FAIL_ON_WEAK=0
ACK_INSECURE=0
while (($#)); do
  case "$1" in
    --auto-secrets) AUTO_SECRETS=1 ;;
    --skip-secrets) SKIP_SECRETS=1 ;;
    --fail-on-weak) FAIL_ON_WEAK=1 ;;
    --i-know-what-im-doing) ACK_INSECURE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "未知選項：$1" ;;
  esac
  shift
done

require_command docker
require_environment "$environment"
prepare_env_file

secret_requirement() {
  local key="$1"
  case "$key" in
    *_ENCRYPTION_KEY) echo 44 ;;
    *_SECRET|*_SIGNING_KEY|*_JWT_*) echo 64 ;;
    *_TOKEN|*_API_KEY) echo 48 ;;
    *_PASSWORD) echo 24 ;;
    *) echo 32 ;;
  esac
}

weak_value() {
  local key="$1"
  local value="$2"
  local normalized="${value,,}"
  local minimum
  minimum="$(secret_requirement "$key")"
  case "$normalized" in
    change-me|change_me|changeme|replace_with_*|replace-me|password|secret|admin|root|123456|develop|your-*|yourpassword|example|placeholder)
      return 0
      ;;
  esac
  (( ${#value} < 16 )) && return 0
  (( ${#value} < minimum )) && return 0
  return 1
}

collect_weak_keys() {
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* || "$line" != *=* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    value="${value%$'\r'}"
    value="${value#\"}"
    value="${value%\"}"
    value="${value#\'}"
    value="${value%\'}"
    case "$key" in
      *_PASSWORD|*_SECRET|*_SIGNING_KEY|*_JWT_*|*_TOKEN|*_API_KEY|*_ENCRYPTION_KEY)
        weak_value "$key" "$value" && printf '%s\n' "$key"
        ;;
    esac
  done < "$ENV_FILE"
}

generate_secret() {
  local key="$1"
  local length
  length="$(secret_requirement "$key")"
  if command -v openssl >/dev/null 2>&1; then
    case "$key" in
      *_ENCRYPTION_KEY) openssl rand -base64 33 | tr -d '\n' ;;
      *) openssl rand -hex $(( (length + 1) / 2 )) | cut -c1-"$length" ;;
    esac
  else
    tr -dc 'A-Za-z0-9_-' < /dev/urandom | head -c "$length"
  fi
}

replace_env_value() {
  local key="$1"
  local value="$2"
  local temporary_file="${ENV_FILE}.tmp"
  awk -v target="$key" -v replacement="$value" 'BEGIN { replaced = 0 } $0 ~ "^" target "=" { print target "=" replacement; replaced = 1; next } { print } END { if (!replaced) print target "=" replacement }' "$ENV_FILE" > "$temporary_file"
  mv "$temporary_file" "$ENV_FILE"
}

handle_secrets() {
  local weak_keys
  weak_keys="$(collect_weak_keys || true)"
  [[ -z "$weak_keys" ]] && return 0

  if (( AUTO_SECRETS )); then
    while IFS= read -r key; do
      [[ -z "$key" ]] && continue
      replace_env_value "$key" "$(generate_secret "$key")"
      log "已自動產生 $key"
    done <<< "$weak_keys"
    weak_keys="$(collect_weak_keys || true)"
    [[ -z "$weak_keys" ]] || fail "自動產生後仍有弱敏感值：${weak_keys//$'\n'/, }"
    cp "$ENV_FILE" "$DOCKER_DIR/.env"
    return 0
  fi

  if (( SKIP_SECRETS )); then
    if [[ "$environment" == "production" && $ACK_INSECURE -ne 1 ]]; then
      fail "production 使用 --skip-secrets 必須同時指定 --i-know-what-im-doing"
    fi
    log "警告：已跳過弱敏感值檢查：${weak_keys//$'\n'/, }"
    return 0
  fi

  if (( FAIL_ON_WEAK )) || [[ "$environment" == "production" ]]; then
    fail "偵測到弱敏感值：${weak_keys//$'\n'/, }。請使用 --auto-secrets 或更新 $ENV_FILE"
  fi
  fail "偵測到弱敏感值：${weak_keys//$'\n'/, }。請使用 --auto-secrets、--skip-secrets，或更新 $ENV_FILE"
}

handle_secrets
log "驗證 Docker Compose 設定"
compose config --quiet
log "建置並啟動 $environment 環境"
compose up -d --build
wait_for_database 45

"$SCRIPT_DIR/migrate.sh" "$environment"
"$SCRIPT_DIR/seed.sh" "$environment"
wait_for_api 45

log "部署完成：$(grep '^FRONTEND_PORT=' "$ENV_FILE" | cut -d= -f2- || echo 3000)"
log "查看狀態：docker compose --env-file docker/.env -f docker/docker-compose.yml ps"
