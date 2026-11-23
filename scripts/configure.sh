#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
if [ -e .env.local ]; then
  echo '已有 .env.local，继续使用现有配置。'
  exit 0
fi
tmp=$(mktemp .env.initial.XXXXXX)
trap 'rm -f "$tmp"' EXIT
database_password=$(openssl rand -hex 24)
cat > "$tmp" <<CONFIG
DB_PASSWORD=$database_password
DATABASE_URL=postgres://alliance:$database_password@127.0.0.1:15484/alliance?sslmode=disable
BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
RESOURCE_KEY=$(openssl rand -hex 32)
API_ADDR=127.0.0.1:18085
COOKIE_SECURE=false
TEMPORAL_ADDR=127.0.0.1:17238
CONNECTOR_URL=http://127.0.0.1:18726
CONNECTOR_TOKEN=$(openssl rand -hex 32)
CONFIG
ln "$tmp" .env.local
echo '已生成 .env.local。首次开通时由安装负责人读取 BOOTSTRAP_TOKEN。'
