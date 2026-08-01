package meshery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
		return fmt.Errorf("meshery GET %s -> %s", path, resp.Status)
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

// ListKubernetesResources lists MeshSync-discovered resources.
//
// Security: it never requests spec/status/labels/annotations, so the server
// omits those columns and Secret data / last-applied-config are never
// serialized; Secrets are excluded outright as a second layer.
func (c *Client) ListKubernetesResources(ctx context.Context, kind, namespace string, page, pageSize int) (*MeshSyncResponse, error) {
	if pageSize == 0 {
		pageSize = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pagesize", strconv.Itoa(pageSize))
	if kind != "" && kind != "Secret" {
		q.Add("kind", kind)
	}
	if namespace != "" {
		q.Add("namespace", namespace)
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

// GetMeshSyncSummary returns the raw MeshSync resource summary for the read-only
// MCP resource (GET /api/system/meshsync/resources/summary).
func (c *Client) GetMeshSyncSummary(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	return raw, c.get(ctx, "/api/system/meshsync/resources/summary", nil, &raw)
}
