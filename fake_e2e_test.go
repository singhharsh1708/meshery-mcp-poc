package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
	"github.com/singhharsh1708/meshery-mcp-poc/mesheryfake"
)

// TestMCPServerAgainstFakeMeshery drives the whole MCP surface, over the SDK's
// transport, against the fake Meshery rather than a hand-written mock, and then
// asserts on what reached Meshery.
//
// The distinction matters. A hand-written mock returns the shape the code under
// test expects, so it agrees with the code even when the code is wrong about
// the real API. The fake reproduces Meshery's behaviour instead, including the
// parts that fail silently, so a tool that gets the wire contract wrong comes
// back empty here the way it would in production.
func TestMCPServerAgainstFakeMeshery(t *testing.T) {
	fake := mesheryfake.New(t)
	cs := connectTo(t, newServer(meshery.New(fake.URL(), fake.Token, fake.Provider)))
	ctx := context.Background()
	cluster := fake.Data().ClusterID()

	// The contexts tool is the entry point: it is the only call that yields a
	// Kubernetes server ID, which every cluster-scoped call then needs.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "meshery_list_kubernetes_contexts"})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); !strings.Contains(got, cluster) {
		t.Fatalf("contexts tool did not return the Kubernetes server ID %q: %s", cluster, got)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "meshery_list_kubernetes_resources",
		Arguments: map[string]any{"clusterId": cluster},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := textOf(res)
	if res.IsError {
		t.Fatalf("resources tool errored: %s", got)
	}
	// Three of the four seeded resources. Emptiness here would be the silent
	// failure this package exists to surface.
	if !strings.Contains(got, "productpage") {
		t.Fatalf("resources tool returned nothing for a populated cluster: %s", got)
	}
	if strings.Contains(got, "db-credentials") || strings.Contains(got, `"Secret"`) {
		t.Fatalf("a Secret reached the model: %s", got)
	}

	for _, call := range []struct {
		name string
		want string
	}{
		{"meshery_list_designs", "bookinfo"},
		{"meshery_list_kubernetes_connections", "minikube"},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name})
		if err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if got := textOf(res); !strings.Contains(got, call.want) {
			t.Errorf("%s did not return %q: %s", call.name, call.want, got)
		}
	}

	// The templated resources go through the undocumented asDesign path, which
	// clears the flat resource list and returns a graph instead.
	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "meshery://clusters/" + cluster + "/topology",
	})
	if err != nil {
		t.Fatal(err)
	}
	topo := rr.Contents[0].Text
	for _, want := range []string{`"displayName":"productpage"`, `"evaluated":true`, `"excludedSecrets":1`} {
		if !strings.Contains(topo, want) {
			t.Errorf("topology missing %s: %s", want, topo)
		}
	}
	if strings.Contains(topo, "db-credentials") {
		t.Errorf("a Secret reached the model through the topology graph: %s", topo)
	}

	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "meshery://clusters/" + cluster + "/summary",
	}); err != nil {
		t.Fatalf("summary resource: %v", err)
	}

	// Everything above only proves the responses looked right. These prove the
	// requests were right, which is the half a mock cannot check.
	fake.AssertAuthenticated(t)
	fake.AssertClusterScoped(t, "/api/system/meshsync/resources", cluster)
	fake.AssertClusterScoped(t, "/api/system/meshsync/resources/summary", cluster)
	fake.AssertZeroBasedPaging(t, "/api/system/meshsync/resources")
	fake.AssertZeroBasedPaging(t, "/api/pattern")
	fake.AssertQuery(t, "/api/system/meshsync/resources", "asDesign", "true")

	for _, field := range []string{"spec", "status", "labels", "annotations"} {
		fake.AssertNoQuery(t, "/api/system/meshsync/resources", field)
	}
	for _, path := range []string{"/api/pattern/deploy", "/api/pattern/import"} {
		fake.AssertNotCalled(t, path)
	}
}

// TestSecretKindIsRefusedBeforeAnyRequest checks the refusal happens in the
// server, not by filtering a response. Dropping the filter and returning every
// other kind would look like a successful answer to the model.
func TestSecretKindIsRefusedBeforeAnyRequest(t *testing.T) {
	fake := mesheryfake.New(t)
	cs := connectTo(t, newServer(meshery.New(fake.URL(), fake.Token, fake.Provider)))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "meshery_list_kubernetes_resources",
		Arguments: map[string]any{"clusterId": fake.Data().ClusterID(), "kind": "Secret"},
	})
	if err == nil && !res.IsError {
		t.Fatalf("expected a refusal, got: %s", textOf(res))
	}
	fake.AssertNotCalled(t, "/api/system/meshsync/resources")
}
