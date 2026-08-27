package mesherytest

// Data is the fixture set the fake serves. Field names and JSON tags follow
// Meshery's wire format rather than anything convenient.
type Data struct {
	Contexts    []Context
	Connections []Connection
	Designs     []Design
	Resources   []Resource
	OrgID       string
}

// Context mirrors an entry from GET /api/system/kubernetes/contexts.
//
// The three identifiers here are distinct and not interchangeable:
// KubernetesServerID is what MeshSync keys resources on, ConnectionID
// addresses the connection record, and ID is the deployment target passed as
// ?contexts=.
type Context struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Server             string `json:"server"`
	Version            string `json:"version"`
	ConnectionID       string `json:"connectionId"`
	KubernetesServerID string `json:"kubernetesServerId"`
}

// Connection mirrors an entry from GET /api/integrations/connections.
type Connection struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// Design mirrors an entry from GET /api/pattern.
//
// PatternFile is a JSON *string* on current Meshery, not a nested object, and
// it is served under the camelCase key. Decoding it as an object, or looking
// only for the older pattern_file spelling, yields an empty design and no
// error.
type Design struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PatternFile string `json:"patternFile"`
}

// Resource mirrors a MeshSync-discovered object from
// GET /api/system/meshsync/resources.
type Resource struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	APIVersion string   `json:"apiVersion"`
	ClusterID  string   `json:"cluster_id"`
	Metadata   Metadata `json:"metadata"`
}

// Metadata is the subset of object metadata MeshSync returns by default.
type Metadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

const designFileJSON = `{"name":"bookinfo","schemaVersion":"designs.meshery.io/v1beta1",` +
	`"components":[` +
	`{"id":"c1","displayName":"productpage","component":{"kind":"Deployment","version":"apps/v1"}},` +
	`{"id":"c2","displayName":"tls-cert","component":{"kind":"Secret","version":"v1"}}],` +
	`"relationships":[{"id":"r1","kind":"edge","subType":"network","type":"non-binding"}]}`

// SeedData returns a small dataset: one cluster, one connection, two designs
// and four discovered resources, one of which is a Secret so that a Secret
// exclusion guarantee has something to exclude.
func SeedData() *Data {
	const ksid = "ksid-9c2e"
	return &Data{
		OrgID: "org-1a2b",
		Contexts: []Context{{
			ID:                 "ctx-7f3a",
			Name:               "minikube",
			Server:             "https://127.0.0.1:6443",
			Version:            "v1.31.0",
			ConnectionID:       "conn-42b1",
			KubernetesServerID: ksid,
		}},
		Connections: []Connection{{
			ID:     "conn-42b1",
			Name:   "minikube",
			Kind:   "kubernetes",
			Status: "connected",
		}},
		Designs: []Design{
			{ID: "d-1001", Name: "bookinfo", PatternFile: designFileJSON},
			{ID: "d-1002", Name: "redis-cache", PatternFile: designFileJSON},
		},
		Resources: []Resource{
			{ID: "r1", Kind: "Deployment", APIVersion: "apps/v1", ClusterID: ksid,
				Metadata: Metadata{Name: "productpage", Namespace: "payments"}},
			{ID: "r2", Kind: "Pod", APIVersion: "v1", ClusterID: ksid,
				Metadata: Metadata{Name: "productpage-7d4", Namespace: "payments"}},
			{ID: "r3", Kind: "Service", APIVersion: "v1", ClusterID: ksid,
				Metadata: Metadata{Name: "productpage-svc", Namespace: "payments"}},
			{ID: "r4", Kind: "Secret", APIVersion: "v1", ClusterID: ksid,
				Metadata: Metadata{Name: "db-credentials", Namespace: "payments"}},
		},
	}
}

// ClusterID returns the Kubernetes server ID of the seeded cluster, which is
// the value the cluster-scoped endpoints expect.
func (d *Data) ClusterID() string {
	if len(d.Contexts) == 0 {
		return ""
	}
	return d.Contexts[0].KubernetesServerID
}
