package meshery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

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

type Topology struct {
	Name          string                 `json:"name"`
	SchemaVersion string                 `json:"schemaVersion"`
	Components    []TopologyComponent    `json:"components"`
	Relationships []TopologyRelationship `json:"relationships"`

	Evaluated bool `json:"evaluated"`

	ExcludedSecrets int `json:"excludedSecrets"`
}

func (c *Client) GetClusterTopology(ctx context.Context, clusterID string) (*Topology, error) {
	q := url.Values{}
	q.Set("asDesign", "true")
	q.Set("page", "0")
	q.Set("pagesize", "all")
	if clusterID != "" {
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

func excludeSecrets(in []TopologyComponent) (kept []TopologyComponent, dropped int) {
	kept = make([]TopologyComponent, 0, len(in))
	for _, c := range in {
		if c.Component.Kind == "Secret" {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

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
			return nil, fmt.Errorf("design file is not JSON; this build cannot parse it")
		}
	}
	var pf patternFile
	if err := json.Unmarshal(trimmed, &pf); err != nil {
		return nil, fmt.Errorf("design file could not be parsed: %w", err)
	}
	return &pf, nil
}

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
