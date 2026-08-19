#!/usr/bin/env bash
# Runs the full demo: mock Meshery, then a real MCP session against the binary.
set -euo pipefail
cd "$(dirname "$0")/.."

echo '{"token":"demo-session-jwt","meshery-provider":"Meshery"}' > /tmp/mcp-demo-auth.json

go build -o meshery-mcp-poc . 
go run demo/mock_meshery.go & MOCK=$!
trap 'kill $MOCK 2>/dev/null || true' EXIT
sleep 2

# DEMO_PACE controls the pause between steps, in seconds. Default is readable;
# set DEMO_PACE=0 to run flat out.
DEMO_PACE="${DEMO_PACE:-2.5}" python3 demo/drive.py ./meshery-mcp-poc /tmp/mcp-demo-auth.json http://127.0.0.1:9099
