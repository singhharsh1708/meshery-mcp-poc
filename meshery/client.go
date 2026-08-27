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

type Client struct {
	baseURL  string
	token    string
	provider string
	http     *http.Client
}

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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return fmt.Errorf("meshery GET %s -> %s", path, resp.Status)
		}
		return fmt.Errorf("meshery GET %s -> %s: %s", path, resp.Status, detail)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Pattern struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PatternsResponse struct {
	Page       uint      `json:"page"`
	PageSize   uint      `json:"pageSize"`
	TotalCount uint      `json:"totalCount"`
	Patterns   []Pattern `json:"patterns"`
}

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

type K8sResource struct {
	Kind       string  `json:"kind"`
	APIVersion string  `json:"apiVersion"`
	Metadata   k8sMeta `json:"metadata"`
}

type MeshSyncResponse struct {
	Page       int           `json:"page"`
	PageSize   int           `json:"pageSize"`
	TotalCount int64         `json:"totalCount"`
	Resources  []K8sResource `json:"resources"`
}

var ErrSecretKindRefused = errors.New("this server does not return Kubernetes Secrets")

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

func setClusterIDs(q url.Values, clusterIDs ...string) error {
	ids, err := json.Marshal(clusterIDs)
	if err != nil {
		return err
	}
	q.Set("clusterIds", string(ids))
	return nil
}

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

type KubernetesContext struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Server             string `json:"server"`
	Version            string `json:"version"`
	ConnectionID       string `json:"connectionId"`
	KubernetesServerID string `json:"kubernetesServerId"`
}

type ContextsResponse struct {
	Page       uint64               `json:"page"`
	PageSize   uint64               `json:"pageSize"`
	TotalCount int                  `json:"totalCount"`
	Contexts   []*KubernetesContext `json:"contexts"`
}

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

type Connection struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type ConnectionsResponse struct {
	Page        int          `json:"page"`
	PageSize    int          `json:"pageSize"`
	TotalCount  int          `json:"totalCount"`
	Connections []Connection `json:"connections"`
}

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
