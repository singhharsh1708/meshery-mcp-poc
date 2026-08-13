# meshery-mcp-poc

[![ci](https://github.com/singhharsh1708/meshery-mcp-poc/actions/workflows/ci.yml/badge.svg)](https://github.com/singhharsh1708/meshery-mcp-poc/actions/workflows/ci.yml)

A small, read-only [Model Context Protocol](https://modelcontextprotocol.io) server for [Meshery](https://meshery.io), built as a proof-of-concept for the LFX Term 3 2026 "Meshery MCP Server" project ([cncf/mentoring#2019](https://github.com/cncf/mentoring/issues/2019)).

It speaks MCP over **stdio** (default) or **Streamable HTTP** and lets an AI client (Claude Desktop, MCP Inspector, …) read from a local Meshery Server. It exposes:

- **`meshery_list_kubernetes_contexts`** — the entry point for anything cluster-scoped, via `GET /api/system/kubernetes/contexts`
- **`meshery_list_designs`** — lists Meshery designs via `GET /api/pattern`
- **`meshery_list_kubernetes_resources`** — lists MeshSync-discovered Kubernetes resources for one cluster via `GET /api/system/meshsync/resources`
- **`meshery_list_kubernetes_connections`** — lists the Kubernetes cluster connections Meshery is managing via `GET /api/integrations/connections?kind=kubernetes`

Templated resources (RFC 6570), with `resources/subscribe` supported:

- **`meshery://clusters/{cluster_id}/topology`** — the discovered state of a cluster as a graph, components as nodes and relationships as edges
- **`meshery://clusters/{cluster_id}/summary`** — per-kind resource counts for a cluster
- **`meshery://clusters/{cluster_id}/namespaces/{namespace}/workloads`** — resources in one namespace of one cluster
- **`meshery://designs/{design_id}/topology`** — component graph of a saved design

## Three identifiers, and why the contexts tool comes first

Meshery uses three different identifiers for what a user calls "my cluster", and mixing them up produces empty results rather than errors. `GET /api/system/kubernetes/contexts` returns all three together, which is why `meshery_list_kubernetes_contexts` is the entry point:

| Value | What it addresses |
|---|---|
| `kubernetesServerId` | what MeshSync keys discovered resources on; this is the `cluster_id` every resource here takes |
| `connectionId` | the connection record, used by Meshery's own events and connection APIs |
| `id` (context id) | the deployment target, passed as `?contexts=` when deploying a design |

The cluster-scoped endpoints are also unforgiving about it. `GET /api/system/meshsync/resources` filters with `cluster_id IN (?)` against whatever it is given, so omitting the filter produces an empty `IN` clause and returns nothing at all rather than everything. Its sibling `/resources/summary` requires a cluster too and answers 400 without one, and it spells the parameter differently: a repeated singular `clusterId`, not the JSON-encoded `clusterIds` array the resources endpoint parses.

Prompts (guided read-only workflows):

- **`debug_cluster`** — systematic cluster investigation, steering the model onto the tools and resources this server actually exposes
- **`review_design`** — structured design review weighted toward a chosen concern
- **`compare_designs`** — diff two designs' component graphs

No mutating endpoints are registered. `spec`/`status`/`labels`/`annotations` are never requested from MeshSync, so Secret data and last-applied-config never reach the model, and Secrets are filtered out of every path that returns resources or components. The topology resources report an `excludedSecrets` count so a filtered graph is distinguishable from one that never contained any.

## Build

```bash
go build -o meshery-mcp-poc .
```

Requires Go 1.25+. Depends on `github.com/modelcontextprotocol/go-sdk` v1.7.0 and `github.com/yosida95/uritemplate/v3`, which the SDK also uses and which the resource handlers need to match URI templates.

## Transports

```bash
# stdio (default) — for local AI clients like Claude Desktop
./meshery-mcp-poc

# Streamable HTTP (spec 2025-03-26, the transport that superseded HTTP+SSE)
./meshery-mcp-poc -transport http
```

The HTTP mode builds a fresh server per session and validates the `Origin` header (via `http.CrossOriginProtection`) to guard browsers against DNS-rebinding.

It binds `127.0.0.1:8080` by default, deliberately. This server acts with the Meshery credentials of whoever started it, and two things mean a wider bind hands those credentials to the network: the SDK's rebinding guard only engages when the accepting local address is loopback, and `CrossOriginProtection` allows any request that sends no `Origin` or `Sec-Fetch-Site` header, which is every non-browser client. Changing `-addr` to a wildcard address without putting real authentication in front of it lets any host that can reach the port read your Meshery data.

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
      "command": "/absolute/path/to/meshery-mcp-poc",
      "env": {
        "MESHERY_URL": "http://localhost:9081",
        "MESHERY_TOKEN_PATH": "/absolute/path/to/home/.meshery/auth.json"
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

Unit tests cover request paths, cookie auth, response parsing, and Secret exclusion on every path that returns resources or components, including both topology paths (`meshery/client_test.go`, `meshery/topology_test.go`). They also assert positive controls on the query itself, so a dropped `namespace`, `clusterIds` or `asDesign` parameter fails the build rather than passing silently, and that `spec`/`status`/`labels`/`annotations` are never requested.

End-to-end tests (`server_test.go`, `resources_test.go`, `prompts_test.go`) wire the MCP server to a client over the SDK's in-memory transport and drive it against a mock Meshery, covering the tools, the templated resources, subscriptions and the prompts.

## Topology

The cluster topology resource is built on `GET /api/system/meshsync/resources?asDesign=true`, which makes Meshery render discovered cluster state as a design and run it through the relationship evaluator. The `design` field of the response is a `PatternFile`: its `components` are the graph's nodes and its `relationships` are its edges.

Three things that shape the implementation, all from `server/handlers/meshsync_handler.go`:

- `asDesign` is undocumented and absent from Meshery's `openapi.yml`, so it is treated here as an internal API that can move.
- When it is set, the server clears the flat `resources` list. You get the graph or the list, never both in one call.
- Evaluation runs at depth 1 with no timeout guard, and on failure the server falls back to the un-evaluated design and still returns 200. An empty `relationships` array therefore means "no edges were derived or evaluation failed", never a confirmed empty graph, which is why the resource reports an explicit `evaluated` field rather than presenting empty edges as healthy.

MeshSync exposes no push channel for topology deltas, so subscriptions are poll-and-notify: the server tracks subscribed URIs and a real deployment re-reads them and sends `notifications/resources/updated` when content changes.

## Scope

This is a deliberately small vertical slice of the funded project: the Go REST client + cookie auth, the MCP tool/resource registration pattern (`mcp.AddTool` with struct-tag input schemas, `ReadOnlyHint` annotations), both transports (stdio and Streamable HTTP), CI, tests, and the read-only/secret-exclusion posture. It is not the full server — the funded work adds the registry/environments/perf tool surfaces, prompts, and mutating tools behind `--allow-mutations`.

## License

[Apache License 2.0](LICENSE), matching Meshery, so any of this can be folded into [meshery-extensions/meshery-mcp-server](https://github.com/meshery-extensions/meshery-mcp-server) as-is.
