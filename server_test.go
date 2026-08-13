package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
)

func mockMeshery() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pattern", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"page":0,"pageSize":10,"totalCount":1,"patterns":[{"id":"a","name":"nginx"}]}`))
	})
	mux.HandleFunc("/api/system/meshsync/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":2,"resources":[
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"web","namespace":"default"}},
			{"kind":"Secret","apiVersion":"v1","metadata":{"name":"db-creds","namespace":"default"}}
		]}`))
	})
	mux.HandleFunc("/api/system/meshsync/resources/summary", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()["clusterId"]) == 0 {
			// Mirror the real handler, which answers 400 without a cluster.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"clusterIds is required"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Pod":1,"Deployment":3}`))
	})
	mux.HandleFunc("/api/system/kubernetes/contexts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":1,"contexts":[
			{"id":"ctx-1","name":"minikube","server":"https://127.0.0.1:6443","version":"v1.31.0",
			 "connectionId":"conn-1","kubernetesServerId":"ksid-1"}]}`))
	})
	return httptest.NewServer(mux)
}

// TestServerEndToEnd wires the MCP server to a client over the SDK's in-memory
// transport and drives it against a mock Meshery, exercising the full loop:
// tools/call, structured output, Secret exclusion, and resources/read.
func TestServerEndToEnd(t *testing.T) {
	backend := mockMeshery()
	defer backend.Close()

	s := newServer(meshery.New(backend.URL, "tok", "prov"))

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "meshery_list_designs"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list_designs returned error: %s", textOf(res))
	}
	if got := textOf(res); !strings.Contains(got, "nginx") || !strings.Contains(got, "\"totalCount\":1") {
		t.Fatalf("list_designs output = %s", got)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "meshery_list_kubernetes_resources",
		Arguments: map[string]any{"clusterId": "ksid-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := textOf(res)
	if strings.Contains(got, "Secret") || strings.Contains(got, "db-creds") {
		t.Fatalf("Secret leaked into tool output: %s", got)
	}
	if !strings.Contains(got, "web") {
		t.Fatalf("expected the Pod in output: %s", got)
	}

	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "meshery://clusters/ksid-1/summary"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) == 0 || !strings.Contains(rr.Contents[0].Text, "Deployment") {
		t.Fatalf("resource read = %+v", rr.Contents)
	}

	// The contexts tool is the entry point for every cluster-scoped call, so it
	// must surface the Kubernetes server ID distinctly from the other two ids.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "meshery_list_kubernetes_contexts"})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); !strings.Contains(got, `"clusterId":"ksid-1"`) ||
		!strings.Contains(got, `"connectionId":"conn-1"`) {
		t.Fatalf("contexts output did not keep the identifiers distinct: %s", got)
	}
}

// TestSecretKindIsRefusedThroughTheTool checks the refusal survives the MCP
// layer as a tool error the model can read, rather than an unfiltered listing.
func TestSecretKindIsRefusedThroughTheTool(t *testing.T) {
	backend := mockMeshery()
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "meshery_list_kubernetes_resources",
		Arguments: map[string]any{"clusterId": "ksid-1", "kind": "Secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("asking for Secrets must be an error, got: %s", textOf(res))
	}
	if strings.Contains(textOf(res), "web") {
		t.Errorf("refusal leaked an unfiltered listing: %s", textOf(res))
	}
}

// TestEmptyTemplateVariablesAreRejected pins that an empty variable does not
// widen a scoped read to every cluster or namespace.
func TestEmptyTemplateVariablesAreRejected(t *testing.T) {
	backend := mockMeshery()
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	for _, uri := range []string{
		"meshery://clusters//topology",
		"meshery://clusters//summary",
		"meshery://clusters/c1/namespaces//workloads",
		"meshery://designs//topology",
	} {
		if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("%s should not resolve: an empty variable drops its filter", uri)
		}
	}
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
