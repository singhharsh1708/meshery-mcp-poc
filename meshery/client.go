package meshery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client is a small read-only client for a local Meshery Server. Meshery's
// AuthMiddleware authenticates data routes with two cookies (token and
// meshery-provider) written by `mesheryctl system login`; there is no
// Authorization: Bearer path for these routes.
type Client struct {
	baseURL  string
	token    string
	provider string
	http     *http.Client
}

// NewFromEnv reads MESHERY_URL (default http://localhost:9081) and
// MESHERY_TOKEN_PATH (default ~/.meshery/auth.json, the JSON map written by
// `mesheryctl system login`: {"token":"...","meshery-provider":"..."}).
func NewFromEnv() (*Client, error) {
	base := os.Getenv("MESHERY_URL")
	if base == "" {
		base = "http://localhost:9081"
	}
	tp := os.Getenv("MESHERY_TOKEN_PATH")
	if tp == "" {
		home, _ := os.UserHomeDir()
		tp = home + "/.meshery/auth.json"
	}
	raw, err := os.ReadFile(tp)
	if err != nil {
		return nil, fmt.Errorf("read meshery token: %w", err)
	}
	var tok map[string]string
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("parse meshery token: %w", err)
	}
	return New(base, tok["token"], tok["meshery-provider"]), nil
}

// New builds a client for a base URL and the two auth cookie values. Used by
// NewFromEnv and by tests that point at a mock server.
func New(baseURL, token, provider string) *Client {
	return &Client{
		baseURL:  baseURL,
		token:    token,
		provider: provider,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "token", Value: c.token})
	req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: c.provider})
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Include Meshery's own message. Without it the model receives a bare
		// status line and cannot tell a missing filter from an expired session.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return fmt.Errorf("meshery GET %s -> %s", path, resp.Status)
		}
		return fmt.Errorf("meshery GET %s -> %s: %s", path, resp.Status, detail)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Pattern is a Meshery design summary (GET /api/pattern).
type Pattern struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PatternsResponse wraps GET /api/pattern. Wire tags are camelCase.
type PatternsResponse struct {
	Page       uint      `json:"page"`
	PageSize   uint      `json:"pageSize"`
	TotalCount uint      `json:"totalCount"`
	Patterns   []Pattern `json:"patterns"`
}

// ListDesigns lists Meshery designs via GET /api/pattern.
func (c *Client) ListDesigns(ctx context.Context, search string, page, pageSize int) (*PatternsResponse, error) {
	if pageSize == 0 {
		pageSize = 10
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	if search != "" {
		q.Set("search", search)
	}
	var out PatternsResponse
	return &out, c.get(ctx, "/api/pattern", q, &out)
}

type k8sMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// K8sResource is a MeshSync-discovered Kubernetes object (subset).
type K8sResource struct {
	Kind       string  `json:"kind"`
	APIVersion string  `json:"apiVersion"`
	Metadata   k8sMeta `json:"metadata"`
}

// MeshSyncResponse wraps GET /api/system/meshsync/resources. camelCase tags.
type MeshSyncResponse struct {
	Page       int           `json:"page"`
	PageSize   int           `json:"pageSize"`
	TotalCount int64         `json:"totalCount"`
	Resources  []K8sResource `json:"resources"`
}

// ErrSecretKindRefused is returned when a caller asks specifically for Secrets.
// Dropping the filter instead would return every other kind as though it were
// the answer, which is a worse failure than refusing.
var ErrSecretKindRefused = errors.New("this server does not return Kubernetes Secrets")

// ListKubernetesResources lists MeshSync-discovered resources for a cluster.
//
// clusterID is the Kubernetes server ID, which is what MeshSync keys resources
// on. It is required: the handler filters with `cluster_id IN (?)` against
// whatever it is given, so omitting it yields an empty IN clause and therefore
// zero rows rather than everything.
//
// Security: it never requests spec/status/labels/annotations, so the server
// omits those columns and Secret data / last-applied-config are never
// serialized; Secrets are excluded outright as a second layer.
func (c *Client) ListKubernetesResources(ctx context.Context, clusterID, kind, namespace string, page, pageSize int) (*MeshSyncResponse, error) {
	if kind == "Secret" {
		return nil, ErrSecretKindRefused
	}
	if clusterID == "" {
		return nil, fmt.Errorf("cluster id is required; list the Kubernetes contexts first to obtain one")
	}
	if pageSize == 0 {
		pageSize = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	if err := setClusterIDs(q, clusterID); err != nil {
		return nil, err
	}
	if kind != "" {
		q.Add("kind", kind)
	}
	if namespace != "" {
		q.Add("namespace", namespace)
	}
	var out MeshSyncResponse
	if err := c.get(ctx, "/api/system/meshsync/resources", q, &out); err != nil {
		return nil, err
	}
	excludeSecretResources(&out)
	return &out, nil
}

// excludeSecretResources drops Secret rows and reduces TotalCount to match, so
// the count and the list cannot disagree.
func excludeSecretResources(out *MeshSyncResponse) {
	filtered := out.Resources[:0]
	for _, r := range out.Resources {
		if r.Kind == "Secret" {
			if out.TotalCount > 0 {
				out.TotalCount--
			}
			continue
		}
		filtered = append(filtered, r)
	}
	out.Resources = filtered
}

// setClusterIDs writes the JSON-encoded array form that
// /api/system/meshsync/resources expects. Note the summary endpoint next door
// spells the same filter differently; see GetMeshSyncSummary.
func setClusterIDs(q url.Values, clusterIDs ...string) error {
	ids, err := json.Marshal(clusterIDs)
	if err != nil {
		return err
	}
	q.Set("clusterIds", string(ids))
	return nil
}

// GetMeshSyncSummary returns the raw MeshSync resource summary
// (GET /api/system/meshsync/resources/summary).
//
// This endpoint requires at least one cluster and answers 400 without one. It
// also spells the parameter differently from its sibling: a repeated singular
// `clusterId`, rather than the JSON-encoded `clusterIds` array that
// /api/system/meshsync/resources parses.
func (c *Client) GetMeshSyncSummary(ctx context.Context, clusterIDs ...string) (json.RawMessage, error) {
	if len(clusterIDs) == 0 {
		return nil, fmt.Errorf("at least one cluster id is required; list the Kubernetes contexts first to obtain one")
	}
	q := url.Values{}
	for _, id := range clusterIDs {
		if id == "" {
			return nil, fmt.Errorf("cluster id must not be empty")
		}
		q.Add("clusterId", id)
	}
	var raw json.RawMessage
	return raw, c.get(ctx, "/api/system/meshsync/resources/summary", q, &raw)
}

// KubernetesContext ties together the three identifiers Meshery uses for a
// cluster, which are easy to confuse: ID addresses a design deployment target,
// ConnectionID addresses the connection record, and KubernetesServerID is what
// MeshSync keys discovered resources on.
type KubernetesContext struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Server             string `json:"server"`
	Version            string `json:"version"`
	ConnectionID       string `json:"connectionId"`
	KubernetesServerID string `json:"kubernetesServerId"`
}

// ContextsResponse wraps GET /api/system/kubernetes/contexts.
type ContextsResponse struct {
	Page       uint64               `json:"page"`
	PageSize   uint64               `json:"pageSize"`
	TotalCount int                  `json:"totalCount"`
	Contexts   []*KubernetesContext `json:"contexts"`
}

// ListKubernetesContexts lists the Kubernetes contexts Meshery knows about.
// This is the only call that yields a Kubernetes server ID, so it is the entry
// point for every cluster-scoped tool and resource here.
func (c *Client) ListKubernetesContexts(ctx context.Context, page, pageSize int) (*ContextsResponse, error) {
	if pageSize == 0 {
		pageSize = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	var out ContextsResponse
	return &out, c.get(ctx, "/api/system/kubernetes/contexts", q, &out)
}

// Connection is a Meshery connection (subset of GET /api/integrations/connections).
type Connection struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// ConnectionsResponse wraps GET /api/integrations/connections. camelCase tags.
type ConnectionsResponse struct {
	Page        int          `json:"page"`
	PageSize    int          `json:"pageSize"`
	TotalCount  int          `json:"totalCount"`
	Connections []Connection `json:"connections"`
}

// ListKubernetesConnections lists the Kubernetes cluster connections Meshery is
// managing via GET /api/integrations/connections?kind=kubernetes. Read-only.
func (c *Client) ListKubernetesConnections(ctx context.Context, page, pageSize int) (*ConnectionsResponse, error) {
	if pageSize == 0 {
		pageSize = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	q.Add("kind", "kubernetes")
	var out ConnectionsResponse
	return &out, c.get(ctx, "/api/integrations/connections", q, &out)
}
