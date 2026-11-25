#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools/bin:$PWD/.tools/go/bin:$PWD/.tools/node/bin:$PATH"
set -a
. ./.env.local
set +a
case "${1:-}" in
 api) cd backend; exec go run ./cmd/api ;;
 worker)
   api_address=${API_ADDR:-127.0.0.1:18085}
   api_port=${api_address##*:}
   if [ -n "${CONNECTOR_TOKEN:-}" ]; then
     export HOT_STREAM_PUBLISH_URL=${HOT_STREAM_PUBLISH_URL:-http://127.0.0.1:${api_port:-18085}/internal/project-stream}
   fi
   cd backend
   exec go run ./cmd/worker ;;
 retrieval) exec "${RETRIEVAL_PYTHON:-.tools/retrieval-venv/bin/python}" retrieval/server.py ;;
 preview) exec "${CONNECTOR_PYTHON:-.tools/connectors-venv/bin/python}" preview/server.py ;;
 tools) exec "${CONNECTOR_PYTHON:-.tools/connectors-venv/bin/python}" connectors/server.py ;;
 web) cd frontend; exec npm run dev ;;
 *) exit 2 ;;
esac
