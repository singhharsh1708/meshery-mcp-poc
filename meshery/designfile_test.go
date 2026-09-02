package meshery

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// TestGetDesignTopologyAcceptsPatternFileSpellings covers the shapes Meshery has
// used for the design file. The camelCase string form is what current releases
// return; decoding only the older snake_case object form produced an empty
// graph instead of an error.
func TestGetDesignTopologyAcceptsPatternFileSpellings(t *testing.T) {
	const design = `{"name":"my-design","schemaVersion":"designs.meshery.io/v1beta1",` +
		`"components":[{"id":"c1","displayName":"redis","component":{"kind":"Deployment","version":"apps/v1"}}],` +
		`"relationships":[{"id":"e1","kind":"edge"}]}`

	cases := map[string]string{
		// current: JSON string under camelCase
		"patternFile string":  `{"id":"d1","name":"outer","patternFile":"{\"name\":\"my-design\",\"schemaVersion\":\"designs.meshery.io/v1beta1\",\"components\":[{\"id\":\"c1\",\"displayName\":\"redis\",\"component\":{\"kind\":\"Deployment\",\"version\":\"apps/v1\"}}],\"relationships\":[{\"id\":\"e1\",\"kind\":\"edge\"}]}"}`,
		"patternFile object":  `{"id":"d1","name":"outer","patternFile":` + design + `}`,
		"pattern_file object": `{"id":"d1","name":"outer","pattern_file":` + design + `}`,
		"designFile object":   `{"id":"d1","name":"outer","designFile":` + design + `}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			topo, err := c.GetDesignTopology(context.Background(), "d1")
			if err != nil {
				t.Fatal(err)
			}
			if topo.Name != "my-design" {
				t.Errorf("Name = %q, want my-design", topo.Name)
			}
			if len(topo.Components) != 1 || topo.Components[0].DisplayName != "redis" {
				t.Fatalf("components = %+v", topo.Components)
			}
			if !topo.Evaluated {
				t.Error("Evaluated should be true when relationships are present")
			}
		})
	}
}

// TestGetDesignTopologyErrorsRatherThanReturningEmpty pins that a design file
// which is missing or unparsable is an error, never a graph with no components.
func TestGetDesignTopologyErrorsRatherThanReturningEmpty(t *testing.T) {
	cases := map[string]string{
		"no design file at all": `{"id":"d1","name":"outer"}`,
		"unknown spelling":      `{"id":"d1","name":"outer","some_other_field":{"components":[]}}`,
		"unparsable string":     `{"id":"d1","patternFile":"\tnot: [valid, yaml"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			if _, err := c.GetDesignTopology(context.Background(), "d1"); err == nil {
				t.Fatal("expected an error, got a topology (a silent empty graph is the bug this guards)")
			}
		})
	}
}

// TestGetDesignTopologyEvaluatedReflectsRelationships pins that the design path
// derives Evaluated the same way the cluster path does, rather than asserting
// true unconditionally.
func TestGetDesignTopologyEvaluatedReflectsRelationships(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"d1","patternFile":{"name":"d","components":[
			{"id":"c1","displayName":"redis","component":{"kind":"Deployment"}}],"relationships":[]}}`))
	}))
	defer srv.Close()

	topo, err := c.GetDesignTopology(context.Background(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if topo.Evaluated {
		t.Error("Evaluated must be false when the design carries no relationships")
	}
}

// TestGetDesignTopologyParsesRealMesheryYAML uses a design captured verbatim
// from a running Meshery Server. Every design a live server returns is YAML
// inside the patternFile string, not JSON, so a JSON-only decoder fails on all
// of them while passing against any mock that serves JSON.
func TestGetDesignTopologyParsesRealMesheryYAML(t *testing.T) {
	design, err := os.ReadFile("testdata/real_design.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"id":          "d1",
		"name":        "prometheus-postgres-exporter",
		"patternFile": string(design),
	})
	if err != nil {
		t.Fatal(err)
	}

	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	topo, err := c.GetDesignTopology(context.Background(), "d1")
	if err != nil {
		t.Fatalf("a design from a real Meshery did not parse: %v", err)
	}
	if len(topo.Components) == 0 {
		t.Fatal("no components parsed out of a real design")
	}
	if topo.Name != "prometheus-postgres-exporter" {
		t.Errorf("name = %q", topo.Name)
	}
	for _, c := range topo.Components {
		if c.DisplayName == "" {
			t.Errorf("component %s parsed with no display name", c.ID)
		}
	}
}
