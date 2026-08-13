package meshery

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestGetClusterTopologyExcludesSecrets is the regression test for the gap that
// let Secret components through the asDesign path while the flat resource list
// filtered them.
func TestGetClusterTopologyExcludesSecrets(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalCount":3,"design":{"name":"cluster","schemaVersion":"v1beta1",
			"components":[
				{"id":"n1","displayName":"web","component":{"kind":"Pod","version":"v1"}},
				{"id":"n2","displayName":"db-creds","component":{"kind":"Secret","version":"v1"}},
				{"id":"n3","displayName":"api","component":{"kind":"Deployment","version":"apps/v1"}}
			],
			"relationships":[{"id":"e1","kind":"hierarchical"}]}}`))
	}))
	defer srv.Close()

	topo, err := c.GetClusterTopology(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	for _, comp := range topo.Components {
		if comp.Component.Kind == "Secret" || comp.DisplayName == "db-creds" {
			t.Fatalf("Secret component leaked into topology: %+v", topo.Components)
		}
	}
	if len(topo.Components) != 2 {
		t.Fatalf("want 2 non-Secret components, got %d", len(topo.Components))
	}
	if topo.ExcludedSecrets != 1 {
		t.Errorf("ExcludedSecrets = %d, want 1", topo.ExcludedSecrets)
	}
}

// TestGetDesignTopologyExcludesSecrets covers the same guarantee on the design
// path.
func TestGetDesignTopologyExcludesSecrets(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"d1","name":"d","pattern_file":{"name":"d","schemaVersion":"v1beta1",
			"components":[
				{"id":"c1","displayName":"redis","component":{"kind":"Deployment","version":"apps/v1"}},
				{"id":"c2","displayName":"tls","component":{"kind":"Secret","version":"v1"}}
			],
			"relationships":[]}}`))
	}))
	defer srv.Close()

	topo, err := c.GetDesignTopology(context.Background(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Components) != 1 || topo.Components[0].DisplayName != "redis" {
		t.Fatalf("unexpected components: %+v", topo.Components)
	}
	if topo.ExcludedSecrets != 1 {
		t.Errorf("ExcludedSecrets = %d, want 1", topo.ExcludedSecrets)
	}
}

// TestGetClusterTopologyEvaluatedIsFalseWithoutEdges pins the ambiguous-empty
// case: Meshery returns 200 with an un-evaluated design when evaluation fails,
// and that must not be reported as a healthy empty graph.
func TestGetClusterTopologyEvaluatedIsFalseWithoutEdges(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalCount":1,"design":{"name":"c","components":[
			{"id":"n1","displayName":"web","component":{"kind":"Pod","version":"v1"}}],"relationships":[]}}`))
	}))
	defer srv.Close()

	topo, err := c.GetClusterTopology(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if topo.Evaluated {
		t.Error("Evaluated must be false when no relationships were returned")
	}
}

// TestGetClusterTopologySendsAsDesignAndClusterIDs is a positive control on the
// query: without it, dropping either parameter would go unnoticed.
func TestGetClusterTopologySendsAsDesignAndClusterIDs(t *testing.T) {
	var raw string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"design":{"components":[],"relationships":[]}}`))
	}))
	defer srv.Close()

	if _, err := c.GetClusterTopology(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "asDesign=true") {
		t.Errorf("asDesign not sent: %s", raw)
	}
	// clusterIds must be a JSON-encoded array, not a repeated parameter.
	if !strings.Contains(raw, `clusterIds=%5B%22c1%22%5D`) {
		t.Errorf("clusterIds not sent as a JSON array: %s", raw)
	}
}

// TestListWorkloadsSendsFilters is the positive control the workloads resource
// lacked: a dropped namespace or cluster filter would otherwise still pass.
func TestListWorkloadsSendsFilters(t *testing.T) {
	var raw string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":0,"resources":[]}`))
	}))
	defer srv.Close()

	if _, err := c.ListWorkloads(context.Background(), "c1", "prod", 0, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "namespace=prod") {
		t.Errorf("namespace filter not sent: %s", raw)
	}
	if !strings.Contains(raw, `clusterIds=%5B%22c1%22%5D`) {
		t.Errorf("clusterIds not sent as a JSON array: %s", raw)
	}
	for _, bad := range []string{"spec=", "status=", "labels=", "annotations="} {
		if strings.Contains(raw, bad) {
			t.Errorf("leaked query param %q: %s", bad, raw)
		}
	}
}

// TestGetDesignTopologyRequiresID checks the guard rather than sending an
// empty path segment to Meshery.
func TestGetDesignTopologyRequiresID(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a design id")
	}))
	defer srv.Close()

	if _, err := c.GetDesignTopology(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty design id")
	}
}

// TestGetDesignTopologyUsesTheID confirms the id reaches Meshery in the path,
// escaped.
func TestGetDesignTopologyUsesTheID(t *testing.T) {
	var path string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"pattern_file":{"components":[],"relationships":[]}}`))
	}))
	defer srv.Close()

	if _, err := c.GetDesignTopology(context.Background(), "abc-123"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/pattern/abc-123" {
		t.Errorf("path = %s, want /api/pattern/abc-123", path)
	}
}
