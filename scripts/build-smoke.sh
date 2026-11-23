#!/bin/sh
set -eu

for utility in soffice pdftotext; do
    if ! command -v "$utility" >/dev/null 2>&1; then
        echo "完整走查需要可执行的 $utility，请将其加入 PATH 或项目的 .tools/bin。" >&2
        exit 1
    fi
done

root=$(pwd)
mkdir -p "$root/.tools"
build=$(mktemp -d "$root/.tools/smoke-build.XXXXXXXX")
trap 'rm -rf "$build"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir "$build/bin" "$build/frontend" "$build/checks"
mkdir -p "$build/services/connectors" "$build/services/preview"
cp connectors/*.py connectors/requirements.txt "$build/services/connectors/"
cp preview/*.py "$build/services/preview/"
cp scripts/smoke*.py "$build/checks/"
cp -R backend/internal/database/schema "$build/checks/"
mkdir "$build/checks/monitor" "$build/checks/identity"
cp backend/internal/identity/member_directory.sql backend/internal/identity/member_history.sql "$build/checks/identity/"
cp backend/internal/monitor/grant_catalog.sql backend/internal/monitor/scope_report.sql "$build/checks/monitor/"
# Only this isolated binary routes Feishu replies to a loopback protocol fixture.
FEISHU_FIXTURE_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
export FEISHU_FIXTURE_PORT
(cd backend && go build -ldflags "-X alliance/internal/channels.feishuOrigin=http://127.0.0.1:$FEISHU_FIXTURE_PORT" -o "$build/bin/api" ./cmd/api && go build -o "$build/bin/worker" ./cmd/worker)
cp -R frontend/src frontend/tests frontend/package.json frontend/package-lock.json frontend/index.html frontend/tsconfig.json frontend/vite.config.js "$build/frontend/"
(cd "$build/frontend" && npm ci && npm run build && npx playwright install chromium)
python3 -m venv "$build/connectors"
"$build/connectors/bin/pip" install -r "$build/services/connectors/requirements.txt"
SMOKE_BUILD_DIR="$build" python3 "$build/checks/smoke-system.py"
