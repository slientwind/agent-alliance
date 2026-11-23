#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools/bin:$PWD/.tools/go/bin:$PWD/.tools/node/bin:$PATH"
set -a
. ./.env.local
set +a
case "${1:-}" in
 api|worker) cd backend; exec go run "./cmd/$1" ;;
 retrieval) exec "${RETRIEVAL_PYTHON:-.tools/retrieval-venv/bin/python}" retrieval/server.py ;;
 preview) exec "${CONNECTOR_PYTHON:-.tools/connectors-venv/bin/python}" preview/server.py ;;
 tools) exec "${CONNECTOR_PYTHON:-.tools/connectors-venv/bin/python}" connectors/server.py ;;
 web) cd frontend; exec npm run dev ;;
 *) exit 2 ;;
esac
