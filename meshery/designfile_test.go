package meshery

import (
	"context"
	"net/http"
	"testing"
)

func TestGetDesignTopologyAcceptsPatternFileSpellings(t *testing.T) {
	const design = `{"name":"my-design","schemaVersion":"designs.meshery.io/v1beta1",` +
		`"components":[{"id":"c1","displayName":"redis","component":{"kind":"Deployment","version":"apps/v1"}}],` +
		`"relationships":[{"id":"e1","kind":"edge"}]}`

	cases := map[string]string{
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

func TestGetDesignTopologyErrorsRatherThanReturningEmpty(t *testing.T) {
	cases := map[string]string{
		"no design file at all": `{"id":"d1","name":"outer"}`,
		"unknown spelling":      `{"id":"d1","name":"outer","some_other_field":{"components":[]}}`,
		"yaml inside string":    `{"id":"d1","patternFile":"name: my-design\ncomponents: []\n"}`,
		"unparsable string":     `{"id":"d1","patternFile":"not json at all"}`,
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
