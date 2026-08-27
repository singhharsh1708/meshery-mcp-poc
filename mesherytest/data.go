package mesherytest

import "fmt"

type Data struct {
	Contexts    []Context
	Connections []Connection
	Designs     []Design
	Resources   []Resource
	OrgID       string
}

type Context struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Server             string `json:"server"`
	Version            string `json:"version"`
	ConnectionID       string `json:"connectionId"`
	KubernetesServerID string `json:"kubernetesServerId"`
}

type Connection struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type Design struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PatternFile string `json:"patternFile"`
}

type Resource struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	APIVersion string   `json:"apiVersion"`
	ClusterID  string   `json:"cluster_id"`
	Metadata   Metadata `json:"metadata"`
}

type Metadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

const designFileJSON = `{"name":"bookinfo","schemaVersion":"designs.meshery.io/v1beta1",` +
	`"components":[` +
	`{"id":"c1","displayName":"productpage","component":{"kind":"Deployment","version":"apps/v1"}},` +
	`{"id":"c2","displayName":"tls-cert","component":{"kind":"Secret","version":"v1"}}],` +
	`"relationships":[{"id":"r1","kind":"edge","subType":"network","type":"non-binding"}]}`

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
		Designs: seedDesigns(),
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

func seedDesigns() []Design {
	designs := []Design{
		{ID: "d-1001", Name: "bookinfo", PatternFile: designFileJSON},
		{ID: "d-1002", Name: "redis-cache", PatternFile: designFileJSON},
	}
	for i := 3; i <= 30; i++ {
		designs = append(designs, Design{
			ID:          fmt.Sprintf("d-%04d", 1000+i),
			Name:        fmt.Sprintf("design-%02d", i),
			PatternFile: designFileJSON,
		})
	}
	return designs
}

func (d *Data) ClusterID() string {
	if len(d.Contexts) == 0 {
		return ""
	}
	return d.Contexts[0].KubernetesServerID
}
