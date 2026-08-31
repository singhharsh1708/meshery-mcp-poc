package meshery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"sigs.k8s.io/yaml"
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
	// ExcludedSecrets counts Secret components withheld from Components, so a
	// caller can tell a graph that was filtered from one that had none.
	ExcludedSecrets int `json:"excludedSecrets"`
}

// GetClusterTopology renders the discovered state of a cluster as a graph via
// GET /api/system/meshsync/resources?asDesign=true.
//
// asDesign is undocumented and absent from Meshery's openapi.yml, so it is
// treated here as an internal API. The server clears the flat resources list
// when it is set, evaluates relationships at depth 1, and has no timeout guard
// on that path.
func (c *Client) GetClusterTopology(ctx context.Context, clusterID string) (*Topology, error) {
	// Without a cluster filter Meshery answers 200 with nothing, which reads as
	// an empty cluster rather than as the missing argument it is.
	if clusterID == "" {
		return nil, errors.New("cluster id is required; list the Kubernetes contexts first to obtain one")
	}
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
	kept, dropped := excludeSecrets(out.Design.Components)
	return &Topology{
		Name:            out.Design.Name,
		SchemaVersion:   out.Design.SchemaVersion,
		Components:      kept,
		Relationships:   out.Design.Relationships,
		Evaluated:       len(out.Design.Relationships) > 0,
		ExcludedSecrets: dropped,
	}, nil
}

// excludeSecrets drops Secret components and reports how many were removed.
//
// The asDesign path renders every discovered resource as a component, Secrets
// included, so this filter is what keeps the read-only posture true for the
// topology resources. Relationships are left untouched: they may still
// reference a dropped component by id, but the relationship shape exposed here
// carries no names or configuration, so nothing about a Secret leaks through
// them.
func excludeSecrets(in []TopologyComponent) (kept []TopologyComponent, dropped int) {
	kept = make([]TopologyComponent, 0, len(in))
	for _, c := range in {
		if isSecretKind(c.Component.Kind) {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

// GetDesignTopology returns the component graph of a saved design via
// GET /api/pattern/{id}.
//
// Meshery has changed both the name and the shape of this field over time.
// Current releases marshal MesheryPattern.PatternFile as a JSON *string* under
// "patternFile"; older ones used "pattern_file", and the design-file payload is
// also seen as a nested object under "designFile". decodeDesignFile accepts all
// of those rather than silently returning an empty graph when the spelling
// changes.
func (c *Client) GetDesignTopology(ctx context.Context, designID string) (*Topology, error) {
	if designID == "" {
		return nil, fmt.Errorf("design id is required")
	}
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`

		PatternFile     json.RawMessage `json:"patternFile"`
		PatternFileSnak json.RawMessage `json:"pattern_file"`
		DesignFile      json.RawMessage `json:"designFile"`
		DesignFileSnake json.RawMessage `json:"design_file"`
	}
	if err := c.get(ctx, "/api/pattern/"+url.PathEscape(designID), nil, &out); err != nil {
		return nil, err
	}

	raw := firstNonEmpty(out.PatternFile, out.PatternFileSnak, out.DesignFile, out.DesignFileSnake)
	if len(raw) == 0 {
		return nil, fmt.Errorf("design %s carried no design file: none of patternFile, pattern_file, designFile or design_file were present", designID)
	}
	pf, err := decodeDesignFile(raw)
	if err != nil {
		return nil, fmt.Errorf("design %s: %w", designID, err)
	}

	name := pf.Name
	if name == "" {
		name = out.Name
	}
	kept, dropped := excludeSecrets(pf.Components)
	return &Topology{
		Name:            name,
		SchemaVersion:   pf.SchemaVersion,
		Components:      kept,
		Relationships:   pf.Relationships,
		Evaluated:       len(pf.Relationships) > 0,
		ExcludedSecrets: dropped,
	}, nil
}

func firstNonEmpty(vals ...json.RawMessage) json.RawMessage {
	for _, v := range vals {
		if len(v) > 0 && string(v) != "null" {
			return v
		}
	}
	return nil
}

// decodeDesignFile reads a design file that may arrive either as a nested JSON
// object or as a JSON string containing the encoded design.
func decodeDesignFile(raw json.RawMessage) (*patternFile, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err != nil {
			return nil, fmt.Errorf("design file is a string that could not be unquoted: %w", err)
		}
		trimmed = bytes.TrimSpace([]byte(inner))
		if len(trimmed) == 0 {
			return nil, fmt.Errorf("design file string was empty")
		}
		if trimmed[0] != '{' {
			// Meshery serves the design file as YAML, not JSON. Every design a
			// live server returns takes this path.
			var pf patternFile
			if err := yaml.Unmarshal(trimmed, &pf); err != nil {
				return nil, fmt.Errorf("design file could not be parsed as YAML: %w", err)
			}
			return &pf, nil
		}
	}
	var pf patternFile
	if err := json.Unmarshal(trimmed, &pf); err != nil {
		return nil, fmt.Errorf("design file could not be parsed: %w", err)
	}
	return &pf, nil
}

// ListWorkloads lists MeshSync-discovered resources in one namespace of one
// cluster. Secrets are excluded and spec/status/labels/annotations are never
// requested, matching ListKubernetesResources.
func (c *Client) ListWorkloads(ctx context.Context, clusterID, namespace string, page, pageSize int) (*MeshSyncResponse, error) {
	// Same reason as GetClusterTopology: an absent filter is 200 with nothing.
	if clusterID == "" {
		return nil, errors.New("cluster id is required; list the Kubernetes contexts first to obtain one")
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", pageSizeParam(pageSize, 25))
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
	// Every sibling list method checks this. Without it a renamed resources key
	// decodes into an empty slice beside a non-zero count, which reads as an
	// empty namespace rather than as the shape change it is.
	if err := checkListConsistency("/api/system/meshsync/resources", page, len(out.Resources), out.TotalCount); err != nil {
		return nil, err
	}
	excludeSecretResources(&out)
	return &out, nil
}
