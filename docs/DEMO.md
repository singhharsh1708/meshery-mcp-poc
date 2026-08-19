# Demo: a real MCP session

Everything below is a genuine JSON-RPC exchange with the compiled binary over
stdio. Nothing is simulated or hand-written. Reproduce it yourself:

```bash
./demo/run.sh
```

That builds the server, starts a mock Meshery on `127.0.0.1:9099` serving the
real endpoint shapes, and drives the binary through a full MCP session.

## Why a mock rather than a live Meshery

The mock serves the exact payload shapes Meshery returns, including the awkward
ones: the design file arrives as a JSON *string* under `patternFile`, the
summary endpoint answers 400 without a cluster, and the resources endpoint
returns a Secret that the server has to strip. Using it means the demo runs
anywhere in about ten seconds.

What this demo proves: the MCP protocol surface is real and complete, the
Meshery request shapes are correct, and the security posture holds. What it does
not prove: behaviour against a live Meshery with a real cluster attached. That
is a genuine gap and is stated in the README rather than glossed over.

## What the server actually sent to Meshery

Captured from the mock's request log during the run:

```
GET /api/system/kubernetes/contexts?page=0&pagesize=25
    cookies: token=demo-session-jwt meshery-provider=Meshery
GET /api/system/meshsync/resources?clusterIds=%5B%22ksid-9c2e%22%5D&namespace=payments&page=0&pagesize=25
    cookies: token=demo-session-jwt meshery-provider=Meshery
GET /api/system/meshsync/resources?asDesign=true&clusterIds=%5B%22ksid-9c2e%22%5D&page=0&pagesize=all
    cookies: token=demo-session-jwt meshery-provider=Meshery
GET /api/pattern/d-1001
    cookies: token=demo-session-jwt meshery-provider=Meshery
```

Three things worth noting in those lines, each of which is a trap that returns
wrong data rather than an error when you get it wrong:

- Authentication is the `token` and `meshery-provider` cookie pair, not a bearer
  header. Meshery's `RemoteProvider.GetToken` reads only the cookie.
- `clusterIds` is URL-encoded `["ksid-9c2e"]`, a JSON array in a single
  parameter. Omitting it makes the handler build an empty `IN` clause, which
  matches zero rows and reads as an empty cluster.
- The topology call sets `asDesign=true`, an undocumented parameter that makes
  Meshery render discovered state as a design so it has components and
  relationships rather than a flat list.

## Full session transcript

```
========================================================================
meshery-mcp-poc :: live MCP session over stdio
========================================================================

# Handshake. Note the server's advertised capabilities.
-> {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "demo-client", "version": "1.0"}}}
<- {
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "capabilities": {
      "logging": {},
      "prompts": {
        "listChanged": true
      },
      "resources": {
        "listChanged": true,
        "subscribe": true
      },
      "tools": {
        "listChanged": true
      }
    },
    "protocolVersion": "2025-06-18",
    "serverInfo": {
      "name": "meshery-mcp-poc",
      "version": "0.1.0"
    }
  }
}

# Tools the server exposes.
-> {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}
<- {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "ttlMs": 0,
    "cacheScope": "public",
    "tools": [
      {
        "annotations": {
          "idempotentHint": false,
          "readOnlyHint": true
        },
        "description": "List Meshery designs via GET /api/pattern. Read-only.",
        "inputSchema": {
          "type": "object",
          "properties": {
            "search": {
              "type": "string",
              "description": "optional case-insensitive design-name filter"
            },
            "page": {
              "type": "integer",
              "description": "zero-based page index (default 0)"
            },
            "pageSize": {
              "type": "integer",
              "description": "results per page (default 10)"
            }
          },
          "additionalProperties": false
        },
        "name": "meshery_list_designs",
        "outputSchema": {
          "type": "object",
          "properties": {
            "totalCount": {
              "type": "integer"
            },
            "designs": {
              "type": [
                "null",
                "array"
              ],
              "items": {
                "type": "object",
                "properties": {
                  "id": {
                    "type": "string"
                  },
                  "name": {
                    "type": "stri

# Entry point: returns the three distinct Meshery cluster identifiers.
-> {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "meshery_list_kubernetes_contexts", "arguments": {}}}
<- {
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\"contexts\":[{\"clusterId\":\"ksid-9c2e\",\"connectionId\":\"conn-42b1\",\"contextId\":\"ctx-7f3a\",\"name\":\"minikube\",\"server\":\"https://127.0.0.1:6443\",\"version\":\"v1.31.0\"}],\"totalCount\":1}"
      }
    ],
    "structuredContent": {
      "contexts": [
        {
          "clusterId": "ksid-9c2e",
          "connectionId": "conn-42b1",
          "contextId": "ctx-7f3a",
          "name": "minikube",
          "server": "https://127.0.0.1:6443",
          "version": "v1.31.0"
        }
      ],
      "totalCount": 1
    }
  }
}

# Cluster-scoped read. Meshery returns a Secret; the server strips it.
-> {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "meshery_list_kubernetes_resources", "arguments": {"clusterId": "ksid-9c2e", "namespace": "payments"}}}
<- {
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\"resources\":[{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\",\"name\":\"productpage\",\"namespace\":\"payments\"},{\"apiVersion\":\"v1\",\"kind\":\"Pod\",\"name\":\"productpage-7d4\",\"namespace\":\"payments\"}],\"totalCount\":2}"
      }
    ],
    "structuredContent": {
      "resources": [
        {
          "apiVersion": "apps/v1",
          "kind": "Deployment",
          "name": "productpage",
          "namespace": "payments"
        },
        {
          "apiVersion": "v1",
          "kind": "Pod",
          "name": "productpage-7d4",
          "namespace": "payments"
        }
      ],
      "totalCount": 2
    }
  }
}

# Asking for Secrets is refused rather than silently widened.
-> {"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "meshery_list_kubernetes_resources", "arguments": {"clusterId": "ksid-9c2e", "kind": "Secret"}}}
<- {
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "this server does not return Kubernetes Secrets"
      }
    ],
    "isError": true
  }
}

# Parameterised resources (RFC 6570).
-> {"jsonrpc": "2.0", "id": 6, "method": "resources/templates/list", "params": {}}
<- {
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "ttlMs": 0,
    "cacheScope": "public",
    "resourceTemplates": [
      {
        "description": "MeshSync-discovered Kubernetes resources in one namespace of one cluster. Secrets are excluded and spec/status payloads are never requested.",
        "mimeType": "application/json",
        "name": "namespace-workloads",
        "title": "Workloads in a namespace",
        "uriTemplate": "meshery://clusters/{cluster_id}/namespaces/{namespace}/workloads"
      },
      {
        "description": "MeshSync's summary of what was discovered in a cluster, counted by kind, plus the namespaces present. Requires a cluster id; the underlying endpoint answers 400 without one.",
        "mimeType": "application/json",
        "name": "cluster-summary",
        "title": "Per-kind resource counts for a cluster",
        "uriTemplate": "meshery://clusters/{cluster_id}/summary"
      },
      {
        "description": "Discovered infrastructure of a cluster as a graph, where components are nodes and relationships are edges. An empty relationships list means no edges were derived or evaluation failed, not a confirmed empty graph.",
        "mimeType": "application/json",
        "name": "cluster-topology",
        "title": "Cluster topology graph",
        "uriTemplate": "meshery://clusters/{cluster_id}/topology"
      },
      {
        "descripti

# Live topology graph. Secret component filtered, evaluated flag reported.
-> {"jsonrpc": "2.0", "id": 7, "method": "resources/read", "params": {"uri": "meshery://clusters/ksid-9c2e/topology"}}
<- {
  "jsonrpc": "2.0",
  "id": 7,
  "result": {
    "ttlMs": 0,
    "cacheScope": "public",
    "contents": [
      {
        "uri": "meshery://clusters/ksid-9c2e/topology",
        "mimeType": "application/json",
        "text": "{\"name\":\"minikube\",\"schemaVersion\":\"designs.meshery.io/v1beta1\",\"components\":[{\"id\":\"n1\",\"displayName\":\"productpage\",\"component\":{\"kind\":\"Deployment\",\"version\":\"apps/v1\"},\"model\":{\"name\":\"kubernetes\"}},{\"id\":\"n2\",\"displayName\":\"productpage-svc\",\"component\":{\"kind\":\"Service\",\"version\":\"v1\"},\"model\":{\"name\":\"kubernetes\"}},{\"id\":\"n4\",\"displayName\":\"reviews\",\"component\":{\"kind\":\"Deployment\",\"version\":\"apps/v1\"},\"model\":{\"name\":\"kubernetes\"}}],\"relationships\":[{\"id\":\"e1\",\"kind\":\"hierarchical\",\"subType\":\"parent\",\"type\":\"non-binding\"},{\"id\":\"e2\",\"kind\":\"edge\",\"subType\":\"network\",\"type\":\"non-binding\"}],\"evaluated\":true,\"excludedSecrets\":1}"
      }
    ]
  }
}

# Empty template variable must not widen the query to every cluster.
-> {"jsonrpc": "2.0", "id": 8, "method": "resources/read", "params": {"uri": "meshery://clusters//topology"}}
<- {
  "jsonrpc": "2.0",
  "id": 8,
  "error": {
    "code": -32602,
    "message": "Resource not found",
    "data": {
      "uri": "meshery://clusters//topology"
    }
  }
}

# Design graph, decoded from patternFile as a JSON string.
-> {"jsonrpc": "2.0", "id": 9, "method": "resources/read", "params": {"uri": "meshery://designs/d-1001/topology"}}
<- {
  "jsonrpc": "2.0",
  "id": 9,
  "result": {
    "ttlMs": 0,
    "cacheScope": "public",
    "contents": [
      {
        "uri": "meshery://designs/d-1001/topology",
        "mimeType": "application/json",
        "text": "{\"name\":\"bookinfo\",\"schemaVersion\":\"designs.meshery.io/v1beta1\",\"components\":[{\"id\":\"c1\",\"displayName\":\"productpage\",\"component\":{\"kind\":\"Deployment\",\"version\":\"apps/v1\"},\"model\":{\"name\":\"\"}}],\"relationships\":[{\"id\":\"r1\",\"kind\":\"edge\",\"subType\":\"\",\"type\":\"\"}],\"evaluated\":true,\"excludedSecrets\":1}"
      }
    ]
  }
}

# Resource subscription.
-> {"jsonrpc": "2.0", "id": 10, "method": "resources/subscribe", "params": {"uri": "meshery://clusters/ksid-9c2e/topology"}}
<- {
  "jsonrpc": "2.0",
  "id": 10,
  "result": {}
}

# Guided workflows.
-> {"jsonrpc": "2.0", "id": 11, "method": "prompts/list", "params": {}}
<- {
  "jsonrpc": "2.0",
  "id": 11,
  "result": {
    "ttlMs": 0,
    "cacheScope": "public",
    "prompts": [
      {
        "arguments": [
          {
            "name": "design_id_a",
            "description": "First design ID, treated as the baseline.",
            "required": true
          },
          {
            "name": "design_id_b",
            "description": "Second design ID, treated as the candidate.",
            "required": true
          }
        ],
        "description": "Diff the component graphs of two designs.",
        "name": "compare_designs",
        "title": "Compare two designs"
      },
      {
        "arguments": [
          {
            "name": "cluster_id",
            "description": "Kubernetes server ID of the cluster to investigate. Omit to start from the connection list."
          },
          {
            "name": "symptoms",
            "description": "What the user is observing, for example 'pods restarting in payments'."
          }
        ],
        "description": "Systematic read-only investigation of a cluster managed by Meshery.",
        "name": "debug_cluster",
        "title": "Investigate cluster health"
      },
      {
        "arguments": [
          {
            "name": "design_id",
            "description": "ID of the design to review.",
            "required": true
          },
          {
            "name": "review

# A prompt that steers the model onto real tools and warns about the traps.
-> {"jsonrpc": "2.0", "id": 12, "method": "prompts/get", "params": {"name": "debug_cluster", "arguments": {"cluster_id": "ksid-9c2e", "symptoms": "pods restarting in payments"}}}
<- {
  "jsonrpc": "2.0",
  "id": 12,
  "result": {
    "description": "Read-only cluster investigation",
    "messages": [
      {
        "content": {
          "type": "text",
          "text": "Investigate the health of a Kubernetes cluster through Meshery. Work read-only: this server exposes no tools that change cluster state.\n\nReported symptoms: pods restarting in payments\n\nSuggested order:\n\n1. Confirm cluster ksid-9c2e is connected via meshery_list_kubernetes_connections. A disconnected cluster explains stale data before anything else does. If ksid-9c2e turns out not to be a Kubernetes server ID, call meshery_list_kubernetes_contexts to get the right one.\n2. Read meshery://clusters/ksid-9c2e/summary for per-kind counts, to see which resource kinds exist at all.\n3. Read meshery://clusters/ksid-9c2e/topology for the component graph and how workloads relate.\n4. Narrow to a namespace with meshery://clusters/ksid-9c2e/namespaces/{namespace}/workloads.\n\nThings to keep in mind while reasoning:\n\n- The topology resource reports an evaluated field. When it is false the relationship evaluation did not produce edges, which is not the same as the workloads having no relationships. Do not conclude a cluster is disconnected internally on that basis.\n- This data comes from MeshSync's discovered state, not a live read against the API server, so it lags. Say so when a conclusion

========================================================================
session complete
========================================================================
```
