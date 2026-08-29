// Command meshery-mcp-poc is a minimal read-only MCP server for Meshery.
//
// It speaks the Model Context Protocol over stdio (default) or Streamable HTTP
// and exposes read-only tools and a resource against a local Meshery Server. No
// mutating endpoints are registered and Secrets are never surfaced.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
)

type ListDesignsInput struct {
	Search   string `json:"search,omitempty" jsonschema:"optional case-insensitive design-name filter"`
	Page     int    `json:"page,omitempty" jsonschema:"zero-based page index (default 0)"`
	PageSize int    `json:"pageSize,omitempty" jsonschema:"results per page (default 10)"`
}

type DesignSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListDesignsOutput struct {
	TotalCount int             `json:"totalCount"`
	Designs    []DesignSummary `json:"designs"`
}

type ListK8sInput struct {
	ClusterID string `json:"clusterId" jsonschema:"required; the Kubernetes server ID of the cluster, as returned by meshery_list_kubernetes_contexts"`
	Kind      string `json:"kind,omitempty" jsonschema:"filter by Kubernetes kind e.g. Deployment, Pod, Service; Secret is refused"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter by namespace"`
	Page      int    `json:"page,omitempty" jsonschema:"zero-based page index"`
	PageSize  int    `json:"pageSize,omitempty" jsonschema:"results per page (default 25)"`
}

type ListContextsInput struct {
	Page     int `json:"page,omitempty" jsonschema:"zero-based page index"`
	PageSize int `json:"pageSize,omitempty" jsonschema:"results per page (default 25)"`
}

type ContextSummary struct {
	Name string `json:"name"`
	// ClusterID is the Kubernetes server ID, the value MeshSync keys resources
	// on and the one the cluster tools and resources expect.
	ClusterID    string `json:"clusterId"`
	ConnectionID string `json:"connectionId"`
	ContextID    string `json:"contextId"`
	Server       string `json:"server"`
	Version      string `json:"version"`
}

type ListContextsOutput struct {
	TotalCount int              `json:"totalCount"`
	Contexts   []ContextSummary `json:"contexts"`
}

type K8sSummary struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
}

type ListK8sOutput struct {
	TotalCount int          `json:"totalCount"`
	Resources  []K8sSummary `json:"resources"`
}

type ListConnInput struct {
	Page     int `json:"page,omitempty" jsonschema:"zero-based page index"`
	PageSize int `json:"pageSize,omitempty" jsonschema:"results per page (default 25)"`
}

type ConnectionSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type ListConnOutput struct {
	TotalCount  int                 `json:"totalCount"`
	Connections []ConnectionSummary `json:"connections"`
}

func main() {
	transport := flag.String("transport", "stdio", "transport to serve: stdio or http")
	// Loopback by default. This server acts with the user's Meshery credentials,
	// and the SDK's DNS-rebinding guard only engages when the accepting local
	// address is loopback, so a wildcard bind would let any host on the network
	// drive it. Overriding this exposes those credentials to that network.
	addr := flag.String("addr", "127.0.0.1:8080", "listen address for the http (streamable) transport")
	flag.Parse()

	// A missing or unreadable credential file is not a reason to exit. An MCP
	// client that launches this over stdio would see the process die with no
	// JSON-RPC at all, which it reports as a broken server rather than as the
	// configuration problem it is. Starting anyway means the client completes a
	// handshake, sees the tools, and gets the real reason on the first call.
	c, err := meshery.NewFromEnv()
	if err != nil {
		log.Printf("meshery client unavailable, tools will report this on call: %v", err)
		c = meshery.Unconfigured(err)
	}

	switch *transport {
	case "stdio":
		if err := newServer(c).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatalf("server: %v", err)
		}
	case "http":
		// Streamable HTTP (spec 2025-03-26), the transport that superseded the
		// old HTTP+SSE one. A fresh server is built per session, and Origin is
		// validated to guard against DNS-rebinding.
		handler := mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return newServer(c) },
			&mcp.StreamableHTTPOptions{CrossOriginProtection: http.NewCrossOriginProtection()},
		)
		log.Printf("meshery-mcp-poc listening on %s (streamable http)", *addr)
		if err := http.ListenAndServe(*addr, handler); err != nil {
			log.Fatalf("server: %v", err)
		}
	default:
		log.Fatalf("unknown transport %q (want stdio or http)", *transport)
	}
}

// newServer builds the MCP server with its read-only tools and resource
// registered against the given Meshery client. Kept separate from main so tests
// can build it against a mock client.
func newServer(c *meshery.Client) *mcp.Server {
	// Both handlers must be set for the server to advertise
	// capabilities.resources.subscribe.
	subs := newSubscriptions()
	s := mcp.NewServer(&mcp.Implementation{Name: "meshery-mcp-poc", Version: "0.1.0"}, &mcp.ServerOptions{
		SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
			if !servableURI(req.Params.URI) {
				return mcp.ResourceNotFoundError(req.Params.URI)
			}
			subs.add(req.Params.URI)
			return nil
		},
		UnsubscribeHandler: func(_ context.Context, req *mcp.UnsubscribeRequest) error {
			subs.remove(req.Params.URI)
			return nil
		},
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "meshery_list_designs",
		Description: "List Meshery designs via GET /api/pattern. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListDesignsInput) (*mcp.CallToolResult, ListDesignsOutput, error) {
		r, err := c.ListDesigns(ctx, in.Search, in.Page, in.PageSize)
		if err != nil {
			return nil, ListDesignsOutput{}, err
		}
		out := ListDesignsOutput{TotalCount: int(r.TotalCount)}
		for _, p := range r.Patterns {
			out.Designs = append(out.Designs, DesignSummary{ID: p.ID, Name: p.Name})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "meshery_list_kubernetes_contexts",
		Description: "List the Kubernetes contexts Meshery knows about. Call this first: it returns the clusterId that the other Kubernetes tools and the cluster resources require, alongside the separate connection and context identifiers. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListContextsInput) (*mcp.CallToolResult, ListContextsOutput, error) {
		r, err := c.ListKubernetesContexts(ctx, in.Page, in.PageSize)
		if err != nil {
			return nil, ListContextsOutput{}, err
		}
		out := ListContextsOutput{TotalCount: r.TotalCount}
		for _, k := range r.Contexts {
			if k == nil {
				continue
			}
			out.Contexts = append(out.Contexts, ContextSummary{
				Name:         k.Name,
				ClusterID:    k.KubernetesServerID,
				ConnectionID: k.ConnectionID,
				ContextID:    k.ID,
				Server:       k.Server,
				Version:      k.Version,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "meshery_list_kubernetes_resources",
		Description: "List MeshSync-discovered Kubernetes resources for one cluster via GET /api/system/meshsync/resources. Requires a clusterId from meshery_list_kubernetes_contexts. Read-only; Secrets and spec/status payloads excluded.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListK8sInput) (*mcp.CallToolResult, ListK8sOutput, error) {
		r, err := c.ListKubernetesResources(ctx, in.ClusterID, in.Kind, in.Namespace, in.Page, in.PageSize)
		if err != nil {
			return nil, ListK8sOutput{}, err
		}
		out := ListK8sOutput{TotalCount: int(r.TotalCount)}
		for _, k := range r.Resources {
			out.Resources = append(out.Resources, K8sSummary{
				Kind:       k.Kind,
				APIVersion: k.APIVersion,
				Name:       k.Metadata.Name,
				Namespace:  k.Metadata.Namespace,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "meshery_list_kubernetes_connections",
		Description: "List the Kubernetes cluster connections Meshery is managing via GET /api/integrations/connections?kind=kubernetes. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListConnInput) (*mcp.CallToolResult, ListConnOutput, error) {
		r, err := c.ListKubernetesConnections(ctx, in.Page, in.PageSize)
		if err != nil {
			return nil, ListConnOutput{}, err
		}
		out := ListConnOutput{TotalCount: r.TotalCount}
		for _, cn := range r.Connections {
			out.Connections = append(out.Connections, ConnectionSummary{ID: cn.ID, Name: cn.Name, Kind: cn.Kind, Status: cn.Status})
		}
		return nil, out, nil
	})

	addTopologyResources(s, c)
	addPrompts(s)

	return s
}
