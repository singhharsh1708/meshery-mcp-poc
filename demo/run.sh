#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

echo '{"token":"demo-session-jwt","meshery-provider":"Meshery"}' > /tmp/mcp-demo-auth.json

go build -o meshery-mcp-poc . 
lsof -ti :9099 2>/dev/null | xargs -r kill -9 2>/dev/null || true

go run demo/mock_meshery.go & MOCK=$!
trap 'kill $MOCK 2>/dev/null || true' EXIT
until curl -sf -o /dev/null "http://127.0.0.1:9099/healthz"; do sleep 0.1; done

DEMO_PACE="${DEMO_PACE:-0.8}" python3 demo/drive.py ./meshery-mcp-poc /tmp/mcp-demo-auth.json http://127.0.0.1:9099
