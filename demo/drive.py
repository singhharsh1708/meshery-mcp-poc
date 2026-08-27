#!/usr/bin/env python3
"""Drive the meshery-mcp-poc binary over stdio with real MCP JSON-RPC.

Every byte below is a genuine request/response exchange with the server
process; nothing is simulated.
"""
import json
import os
import subprocess
import sys
import time

PACE = float(os.environ.get("DEMO_PACE", "0.8"))

BIN = sys.argv[1] if len(sys.argv) > 1 else "./meshery-mcp-poc"
TOKEN = sys.argv[2] if len(sys.argv) > 2 else "/tmp/mcp-demo-auth.json"
URL = sys.argv[3] if len(sys.argv) > 3 else "http://127.0.0.1:9099"

proc = subprocess.Popen(
    [BIN],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    env={"MESHERY_URL": URL, "MESHERY_TOKEN_PATH": TOKEN, "PATH": "/usr/bin:/bin"},
    text=True, bufsize=1,
)

_id = 0

def call(method, params=None, note=None):
    global _id
    _id += 1
    req = {"jsonrpc": "2.0", "id": _id, "method": method}
    if params is not None:
        req["params"] = params
    if note:
        print(f"\n\033[1m# {note}\033[0m")
        time.sleep(PACE * 0.6)
    print(f"\033[36m-> {json.dumps(req)}\033[0m")
    time.sleep(PACE * 0.4)
    proc.stdin.write(json.dumps(req) + "\n")
    proc.stdin.flush()
    line = proc.stdout.readline()
    if not line:
        print("!! no response (server exited)")
        print(proc.stderr.read())
        sys.exit(1)
    resp = json.loads(line)
    print(f"\033[32m<- {json.dumps(resp, indent=2)[:1400]}\033[0m")
    time.sleep(PACE)
    return resp

def notify(method, params=None):
    req = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        req["params"] = params
    proc.stdin.write(json.dumps(req) + "\n")
    proc.stdin.flush()

print("=" * 72)
print("meshery-mcp-poc :: live MCP session over stdio")
print("=" * 72)
time.sleep(PACE)

r = call("initialize", {
    "protocolVersion": "2025-06-18",
    "capabilities": {},
    "clientInfo": {"name": "demo-client", "version": "1.0"},
}, "Handshake. Note the server's advertised capabilities.")
notify("notifications/initialized")
time.sleep(0.2)

call("tools/list", {}, "Tools the server exposes.")

call("tools/call", {
    "name": "meshery_list_kubernetes_contexts", "arguments": {}
}, "Entry point: returns the three distinct Meshery cluster identifiers.")

call("tools/call", {
    "name": "meshery_list_kubernetes_resources",
    "arguments": {"clusterId": "ksid-9c2e", "namespace": "payments"},
}, "Cluster-scoped read. Meshery returns a Secret; the server strips it.")

call("tools/call", {
    "name": "meshery_list_kubernetes_resources",
    "arguments": {"clusterId": "ksid-9c2e", "kind": "Secret"},
}, "Asking for Secrets is refused rather than silently widened.")

call("resources/templates/list", {}, "Parameterised resources (RFC 6570).")

call("resources/read", {
    "uri": "meshery://clusters/ksid-9c2e/topology"
}, "Live topology graph. Secret component filtered, evaluated flag reported.")

call("resources/read", {
    "uri": "meshery://clusters//topology"
}, "Empty template variable must not widen the query to every cluster.")

call("resources/read", {
    "uri": "meshery://designs/d-1001/topology"
}, "Design graph, decoded from patternFile as a JSON string.")

call("resources/subscribe", {
    "uri": "meshery://clusters/ksid-9c2e/topology"
}, "Resource subscription.")

call("prompts/list", {}, "Guided workflows.")

call("prompts/get", {
    "name": "debug_cluster",
    "arguments": {"cluster_id": "ksid-9c2e", "symptoms": "pods restarting in payments"},
}, "A prompt that steers the model onto real tools and warns about the traps.")

proc.stdin.close()
proc.wait(timeout=5)
print("\n" + "=" * 72)
print("session complete")
print("=" * 72)
