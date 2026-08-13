package meshery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// TopologyComponent is a node in a topology graph: one component of the design
// Meshery renders from discovered cluster state.
type TopologyComponent struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Component   struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"component"`
	Model struct {
		Name string `json:"name"`
	} `json:"model"`
}

// TopologyRelationship is an edge in a topology graph. Kind is one of edge,
// hierarchical or sibling.
type TopologyRelationship struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	SubType string `json:"subType"`
	Type    string `json:"type"`
}

type patternFile struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	SchemaVersion string                 `json:"schemaVersion"`
	Components    []TopologyComponent    `json:"components"`
	Relationships []TopologyRelationship `json:"relationships"`
}

type topologyEnvelope struct {
	TotalCount int64       `json:"totalCount"`
	Design     patternFile `json:"design"`
}

// Topology is a graph of discovered infrastructure: components are nodes and
// relationships are edges.
type Topology struct {
	Name          string                 `json:"name"`
	SchemaVersion string                 `json:"schemaVersion"`
	Components    []TopologyComponent    `json:"components"`
	Relationships []TopologyRelationship `json:"relationships"`
	// Evaluated reports whether Meshery's relationship evaluation produced any
	// edges. The server falls back to the un-evaluated design and still returns
	// 200 when evaluation fails, so an empty Relationships slice is ambiguous:
	// it means "no edges or evaluation failed", never a confirmed empty graph.
	Evaluated bool `json:"evaluated"`
}

// GetClusterTopology renders the discovered state of a cluster as a graph via
// GET /api/system/meshsync/resources?asDesign=true.
//
// asDesign is undocumented and absent from Meshery's openapi.yml, so it is
// treated here as an internal API. The server clears the flat resources list
// when it is set, evaluates relationships at depth 1, and has no timeout guard
// on that path.
func (c *Client) GetClusterTopology(ctx context.Context, clusterID string) (*Topology, error) {
	q := url.Values{}
	q.Set("asDesign", "true")
	q.Set("page", "0")
	q.Set("pagesize", "all")
	if clusterID != "" {
		// clusterIds is a JSON-encoded array, not a repeated query parameter.
		ids, err := json.Marshal([]string{clusterID})
		if err != nil {
			return nil, err
		}
		q.Set("clusterIds", string(ids))
	}
	var out topologyEnvelope
	if err := c.get(ctx, "/api/system/meshsync/resources", q, &out); err != nil {
		return nil, err
	}
	return &Topology{
		Name:          out.Design.Name,
		SchemaVersion: out.Design.SchemaVersion,
		Components:    out.Design.Components,
		Relationships: out.Design.Relationships,
		Evaluated:     len(out.Design.Relationships) > 0,
	}, nil
}

// GetDesignTopology returns the component graph of a saved design via
// GET /api/pattern/{id}.
func (c *Client) GetDesignTopology(ctx context.Context, designID string) (*Topology, error) {
	if designID == "" {
		return nil, fmt.Errorf("design id is required")
	}
	var out struct {
		ID          string      `json:"id"`
		Name        string      `json:"name"`
		PatternFile patternFile `json:"pattern_file"`
	}
	if err := c.get(ctx, "/api/pattern/"+url.PathEscape(designID), nil, &out); err != nil {
		return nil, err
	}
	name := out.PatternFile.Name
	if name == "" {
		name = out.Name
	}
	return &Topology{
		Name:          name,
		SchemaVersion: out.PatternFile.SchemaVersion,
		Components:    out.PatternFile.Components,
		Relationships: out.PatternFile.Relationships,
		Evaluated:     true,
	}, nil
}

// ListWorkloads lists MeshSync-discovered resources in one namespace of one
// cluster. Secrets are excluded and spec/status/labels/annotations are never
// requested, matching ListKubernetesResources.
func (c *Client) ListWorkloads(ctx context.Context, clusterID, namespace string, page, pageSize int) (*MeshSyncResponse, error) {
	if pageSize == 0 {
		pageSize = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	if namespace != "" {
		q.Add("namespace", namespace)
	}
	if clusterID != "" {
		ids, err := json.Marshal([]string{clusterID})
		if err != nil {
			return nil, err
		}
		q.Set("clusterIds", string(ids))
	}
	var out MeshSyncResponse
	if err := c.get(ctx, "/api/system/meshsync/resources", q, &out); err != nil {
		return nil, err
	}
	filtered := out.Resources[:0]
	for _, r := range out.Resources {
		if r.Kind != "Secret" {
			filtered = append(filtered, r)
		}
	}
	out.Resources = filtered
	return &out, nil
}
