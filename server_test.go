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
		_, _ = w.Write([]byte(`{"Pod":1,"Deployment":3}`))
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

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "meshery_list_kubernetes_resources"})
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

	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "meshery://meshsync/summary"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) == 0 || !strings.Contains(rr.Contents[0].Text, "Deployment") {
		t.Fatalf("resource read = %+v", rr.Contents)
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
