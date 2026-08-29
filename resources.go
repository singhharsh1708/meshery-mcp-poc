package main

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"

	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
)

const (
	clusterTopologyTemplate = "meshery://clusters/{cluster_id}/topology"
	clusterSummaryTemplate  = "meshery://clusters/{cluster_id}/summary"
	workloadsTemplate       = "meshery://clusters/{cluster_id}/namespaces/{namespace}/workloads"
	designTopologyTemplate  = "meshery://designs/{design_id}/topology"
)

// The SDK does not pass URI template variables to the handler, so each handler
// re-matches the concrete URI against its own compiled template.
var (
	clusterTopologyPattern = uritemplate.MustNew(clusterTopologyTemplate)
	clusterSummaryPattern  = uritemplate.MustNew(clusterSummaryTemplate)
	workloadsPattern       = uritemplate.MustNew(workloadsTemplate)
	designTopologyPattern  = uritemplate.MustNew(designTopologyTemplate)
)

// subscriptions records the resource URIs clients are watching. MeshSync has no
// push channel for topology deltas, so a real server polls these and calls
// Server.ResourceUpdated when the content changes.
type subscriptions struct {
	mu  sync.Mutex
	set map[string]int
}

func newSubscriptions() *subscriptions {
	return &subscriptions{set: make(map[string]int)}
}

func (s *subscriptions) add(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set[uri]++
}

func (s *subscriptions) remove(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set[uri] <= 1 {
		delete(s.set, uri)
		return
	}
	s.set[uri]--
}

func (s *subscriptions) uris() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.set))
	for uri := range s.set {
		out = append(out, uri)
	}
	return out
}

// servableURI reports whether a URI matches one of the templates this server
// serves, with every variable filled in.
//
// Read and subscribe have to agree on this. A subscription the server will
// never read is worse than a refusal: the client is told it is watching
// something, and cannot tell silence from a resource that never existed.
func servableURI(uri string) bool {
	for _, p := range []*uritemplate.Template{
		clusterTopologyPattern, clusterSummaryPattern, workloadsPattern, designTopologyPattern,
	} {
		vals := p.Match(uri)
		if vals == nil {
			continue
		}
		filled := true
		for _, name := range p.Varnames() {
			if vals.Get(name).String() == "" {
				filled = false
				break
			}
		}
		if filled {
			return true
		}
	}
	return false
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      uri,
		MIMEType: "application/json",
		Text:     string(body),
	}}}, nil
}

// addTopologyResources registers the templated read-only topology resources.
func addTopologyResources(s *mcp.Server, c *meshery.Client) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "cluster-topology",
		Title:       "Cluster topology graph",
		URITemplate: clusterTopologyTemplate,
		MIMEType:    "application/json",
		Description: "Discovered infrastructure of a cluster as a graph, where components are nodes and relationships are edges. An empty relationships list means no edges were derived or evaluation failed, not a confirmed empty graph.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		vals := clusterTopologyPattern.Match(req.Params.URI)
		if vals == nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		// An empty variable still matches the template, and an empty cluster id
		// would widen the query to every cluster rather than narrowing it.
		clusterID := vals.Get("cluster_id").String()
		if clusterID == "" {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		topo, err := c.GetClusterTopology(ctx, clusterID)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, topo)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "cluster-summary",
		Title:       "Per-kind resource counts for a cluster",
		URITemplate: clusterSummaryTemplate,
		MIMEType:    "application/json",
		Description: "MeshSync's summary of what was discovered in a cluster, counted by kind, plus the namespaces present. Requires a cluster id; the underlying endpoint answers 400 without one.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		vals := clusterSummaryPattern.Match(req.Params.URI)
		if vals == nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		clusterID := vals.Get("cluster_id").String()
		if clusterID == "" {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		data, err := c.GetMeshSyncSummary(ctx, clusterID)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: req.Params.URI, MIMEType: "application/json", Text: string(data)},
		}}, nil
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "namespace-workloads",
		Title:       "Workloads in a namespace",
		URITemplate: workloadsTemplate,
		MIMEType:    "application/json",
		Description: "MeshSync-discovered Kubernetes resources in one namespace of one cluster. Secrets are excluded and spec/status payloads are never requested.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		vals := workloadsPattern.Match(req.Params.URI)
		if vals == nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		clusterID, namespace := vals.Get("cluster_id").String(), vals.Get("namespace").String()
		if clusterID == "" || namespace == "" {
			// Either empty would drop its filter and silently widen the read.
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		r, err := c.ListWorkloads(ctx, clusterID, namespace, 0, 0)
		if err != nil {
			return nil, err
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
		return jsonResource(req.Params.URI, out)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "design-topology",
		Title:       "Design component graph",
		URITemplate: designTopologyTemplate,
		MIMEType:    "application/json",
		Description: "Component graph of a saved Meshery design, with components as nodes and relationships as edges.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		vals := designTopologyPattern.Match(req.Params.URI)
		if vals == nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		designID := vals.Get("design_id").String()
		if designID == "" {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		topo, err := c.GetDesignTopology(ctx, designID)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, topo)
	})
}
