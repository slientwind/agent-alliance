#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools/node/bin:$PWD/.tools/go/bin:$PATH"
docker compose -p agent-alliance -f deploy/compose.yaml up -d --wait postgres
export ALLIANCE_DATABASE_URL="${ALLIANCE_DATABASE_URL:-postgres://alliance:alliance-local-database@127.0.0.1:55432/alliance?sslmode=disable}"
export ALLIANCE_MASTER_KEY="${ALLIANCE_MASTER_KEY:-MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=}"
export ALLIANCE_ADDR=127.0.0.1:8180
export ALLIANCE_PUBLIC_URL=http://127.0.0.1:8180
export ALLIANCE_GRPC_ADDR=127.0.0.1:8181
export ALLIANCE_GRPC_PUBLIC_ADDR=http://127.0.0.1:8181
export ALLIANCE_INSECURE_GRPC=true
export ALLIANCE_DEMO=true
export ALLIANCE_ALLOW_REGISTRATION=true
export ALLIANCE_INSECURE_COOKIES=true
export ALLIANCE_ALLOW_PRIVATE=true
export ALLIANCE_STATIC="$PWD/frontend/dist"
export ALLIANCE_IMPORT_JSON="$PWD/data/state.json"
(cd backend && go build -o alliance-production .)
backend/alliance-production & API_PID=$!
ALLIANCE_MODE=worker ALLIANCE_WORKER_ID=local-worker-1 backend/alliance-production & WORKER1_PID=$!
ALLIANCE_MODE=worker ALLIANCE_WORKER_ID=local-worker-2 backend/alliance-production & WORKER2_PID=$!
python3 scripts/mock-server.py & MOCK_PID=$!
(cd frontend && exec node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5180 --strictPort --config vite.production.config.js) & WEB_PID=$!
trap 'kill "$API_PID" "$WEB_PID" "$WORKER1_PID" "$WORKER2_PID" "$MOCK_PID" 2>/dev/null || true' EXIT INT TERM
wait
