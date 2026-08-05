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
	Kind      string `json:"kind,omitempty" jsonschema:"filter by Kubernetes kind e.g. Deployment, Pod, Service (Secret is not permitted)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter by namespace"`
	Page      int    `json:"page,omitempty" jsonschema:"zero-based page index"`
	PageSize  int    `json:"pageSize,omitempty" jsonschema:"results per page (default 25)"`
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
	addr := flag.String("addr", ":8080", "listen address for the http (streamable) transport")
	flag.Parse()

	c, err := meshery.NewFromEnv()
	if err != nil {
		log.Fatalf("meshery client: %v", err)
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
	s := mcp.NewServer(&mcp.Implementation{Name: "meshery-mcp-poc", Version: "0.1.0"}, nil)

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
		Name:        "meshery_list_kubernetes_resources",
		Description: "List MeshSync-discovered Kubernetes resources via GET /api/system/meshsync/resources. Read-only; Secrets and spec/status payloads excluded.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListK8sInput) (*mcp.CallToolResult, ListK8sOutput, error) {
		r, err := c.ListKubernetesResources(ctx, in.Kind, in.Namespace, in.Page, in.PageSize)
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

	const summaryURI = "meshery://meshsync/summary"
	s.AddResource(&mcp.Resource{
		URI:         summaryURI,
		Name:        "meshsync-topology-summary",
		MIMEType:    "application/json",
		Description: "Read-only MeshSync cluster resource summary (GET /api/system/meshsync/resources/summary).",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		data, err := c.GetMeshSyncSummary(ctx)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: summaryURI, MIMEType: "application/json", Text: string(data)},
		}}, nil
	})

	return s
}
