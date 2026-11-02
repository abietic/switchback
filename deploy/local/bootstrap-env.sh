#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
env_file="$root/deploy/local/.env"
data_root="$root/.switchback/local"
provider_root="$data_root/provider"
kimi_key="$root/spikes/worker-runtime/.spike-data/final-kimi/env/upstream-model.key"
deepseek_key="$root/spikes/worker-runtime/.spike-data/variants/deepseek-openai/env/upstream-model.key"
command -v openssl >/dev/null 2>&1 || { printf '%s\n' "openssl is required" >&2; exit 1; }
mkdir -p "$data_root" "$provider_root"
umask 077
hex() { openssl rand -hex "$1"; }
if [ ! -f "$env_file" ]; then
  {
    printf 'SWITCHBACK_HOST_DATA_ROOT=%s\n' "$data_root"
    printf 'SWITCHBACK_DOCKER_SOCKET=/var/run/docker.sock\n'
    printf 'SWITCHBACK_WORKER_IMAGE=unbuilt\n'
    printf 'SWITCHBACK_POSTGRES_PASSWORD=%s\n' "$(hex 24)"
    printf 'LANGFUSE_SALT=%s\n' "$(hex 16)"
    printf 'LANGFUSE_ENCRYPTION_KEY=%s\n' "$(hex 32)"
    printf 'LANGFUSE_NEXTAUTH_SECRET=%s\n' "$(hex 32)"
    printf 'LANGFUSE_CLICKHOUSE_PASSWORD=%s\n' "$(hex 24)"
    printf 'LANGFUSE_MINIO_PASSWORD=%s\n' "$(hex 24)"
    printf 'LANGFUSE_REDIS_PASSWORD=%s\n' "$(hex 24)"
    printf 'LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-%s\n' "$(hex 16)"
    printf 'LANGFUSE_INIT_PROJECT_SECRET_KEY=sk-lf-%s\n' "$(hex 24)"
    printf 'LANGFUSE_INIT_USER_PASSWORD=%s\n' "$(hex 24)"
  } > "$env_file"
fi

ensure_value() {
  name=$1
  value=$2
  if grep -q "^${name}=" "$env_file"; then
    return
  fi
  printf '%s=%s\n' "$name" "$value" >> "$env_file"
}

ensure_key_path() {
  name=$1
  preferred=$2
  fallback=$3
  if grep -q "^${name}=" "$env_file"; then
    return
  fi
  if [ ! -f "$preferred" ]; then
    : > "$fallback"
    preferred=$fallback
  fi
  printf '%s=%s\n' "$name" "$preferred" >> "$env_file"
}

ensure_value SWITCHBACK_ASSISTANT_LITELLM_PROXY_KEY "sk-switchback-assistant-$(hex 24)"
ensure_value SWITCHBACK_GRAFANA_PASSWORD "$(hex 24)"
ensure_key_path SWITCHBACK_KIMI_KEY_FILE "$kimi_key" "$provider_root/kimi.key"
ensure_key_path SWITCHBACK_DEEPSEEK_KEY_FILE "$deepseek_key" "$provider_root/deepseek.key"
printf '%s\n' "Local environment is ready: $env_file"
