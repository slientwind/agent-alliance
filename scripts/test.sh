#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools/node/bin:$PWD/.tools/go/bin:$PATH"
export ALLIANCE_TEST_DATABASE_URL="${ALLIANCE_TEST_DATABASE_URL:-postgres://alliance:alliance-local-database@127.0.0.1:55432/alliance?sslmode=disable}"
export ALLIANCE_TEST_LANGFUSE_URL="${ALLIANCE_TEST_LANGFUSE_URL:-http://127.0.0.1:13100}"
mkdir -p artifacts
(cd backend && go test -race -timeout 180s -coverprofile=../artifacts/go-coverage.out ./...)
(cd backend && go build -o alliance-production .)
npm --prefix frontend run build
# Start ./scripts/dev.sh and the isolated Langfuse Compose stack first.
# Browser tests create their own accounts; resilience tests own a temporary DB schema and child processes.
(cd frontend && npx playwright test)
