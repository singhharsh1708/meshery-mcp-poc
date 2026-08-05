# meshery-mcp-poc

A small, read-only [Model Context Protocol](https://modelcontextprotocol.io) server for [Meshery](https://meshery.io), built as a proof-of-concept for the LFX Term 3 2026 "Meshery MCP Server" project ([cncf/mentoring#2019](https://github.com/cncf/mentoring/issues/2019)).

It speaks MCP over stdio and lets an AI client (Claude Desktop, MCP Inspector, …) read from a local Meshery Server. It exposes:

- **`meshery_list_designs`** — lists Meshery designs via `GET /api/pattern`
- **`meshery_list_kubernetes_resources`** — lists MeshSync-discovered Kubernetes resources via `GET /api/system/meshsync/resources`
- **`meshery_list_kubernetes_connections`** — lists the Kubernetes cluster connections Meshery is managing via `GET /api/integrations/connections?kind=kubernetes`
- **`meshery://meshsync/summary`** (resource) — the MeshSync resource summary via `GET /api/system/meshsync/resources/summary`

No mutating endpoints are registered. Secrets are never surfaced, and `spec`/`status`/`labels`/`annotations` are never requested from MeshSync, so Secret data and last-applied-config never reach the model.

## Build

```bash
go build -o meshery-mcp-poc .
```

Requires Go 1.25+. Depends only on `github.com/modelcontextprotocol/go-sdk` v1.7.0.

## Point it at a local Meshery

```bash
mesheryctl system start -p docker      # Meshery Server on http://localhost:9081 + Operator + MeshSync
mesheryctl system login                # pick the local "Meshery" provider -> writes ~/.meshery/auth.json
mesheryctl system config minikube      # (or kind|docker-desktop) so MeshSync has cluster data
```

Auth uses the two cookies (`token`, `meshery-provider`) that `mesheryctl system login` writes to `~/.meshery/auth.json` — the same way `mesheryctl` authenticates. Override with `MESHERY_URL` and `MESHERY_TOKEN_PATH`.

Sanity-check the endpoints the server calls:

```bash
curl -s -b "token=$(jq -r .token ~/.meshery/auth.json); meshery-provider=$(jq -r '.["meshery-provider"]' ~/.meshery/auth.json)" \
  http://localhost:9081/api/system/meshsync/resources | jq '.pageSize,.totalCount,(.resources[0]|{kind,apiVersion,metadata})'
```

## Use it from an MCP client

**Claude Desktop** — `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "meshery": {
      "command": "/Users/harsh/meshery-mcp-poc/meshery-mcp-poc",
      "env": {
        "MESHERY_URL": "http://localhost:9081",
        "MESHERY_TOKEN_PATH": "/Users/harsh/.meshery/auth.json"
      }
    }
  }
}
```

**MCP Inspector** (second client, easy to screenshot):

```bash
npx @modelcontextprotocol/inspector ./meshery-mcp-poc
```

## Tests

```bash
go test ./... -race
```

Unit tests (`meshery/client_test.go`) cover request paths, cookie auth, response parsing, Secret exclusion, and that `spec`/`status`/`labels`/`annotations` are never requested. An end-to-end test (`server_test.go`) wires the MCP server to a client over the SDK's in-memory transport and drives it against a mock Meshery, checking the tools return data and that Secrets never reach the output.

## Scope

This is a deliberately small vertical slice of the funded project: the Go REST client + cookie auth, the MCP tool/resource registration pattern (`mcp.AddTool` with struct-tag input schemas, `ReadOnlyHint` annotations), stdio transport, and the read-only/secret-exclusion posture. It is not the full server — the funded work adds Streamable HTTP, the registry/environments/perf tool surfaces, prompts, mutating tools behind `--allow-mutations`, CI/CD, and tests.
