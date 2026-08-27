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

func mockTopologyMeshery(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/meshsync/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("asDesign") == "true" {
			if got := r.URL.Query().Get("clusterIds"); got != `["c1"]` {
				t.Errorf("clusterIds = %q, want a JSON array", got)
			}
			_, _ = w.Write([]byte(`{"totalCount":2,"design":{"name":"cluster","schemaVersion":"designs.meshery.io/v1beta1",
				"components":[{"id":"n1","displayName":"web","component":{"kind":"Pod","version":"v1"},"model":{"name":"kubernetes"}}],
				"relationships":[{"id":"e1","kind":"hierarchical","subType":"parent","type":"non-binding"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":2,"resources":[
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"web","namespace":"prod"}},
			{"kind":"Secret","apiVersion":"v1","metadata":{"name":"db-creds","namespace":"prod"}}
		]}`))
	})
	mux.HandleFunc("/api/pattern/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"d1","name":"my-design","pattern_file":{"name":"my-design","schemaVersion":"designs.meshery.io/v1beta1",
			"components":[{"id":"c1","displayName":"redis","component":{"kind":"Deployment","version":"apps/v1"},"model":{"name":"kubernetes"}}],
			"relationships":[]}}`))
	})
	return httptest.NewServer(mux)
}

func connectTo(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestResourceTemplatesAreListed(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	res, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		clusterTopologyTemplate: false,
		workloadsTemplate:       false,
		designTopologyTemplate:  false,
	}
	for _, tmpl := range res.ResourceTemplates {
		if _, ok := want[tmpl.URITemplate]; ok {
			want[tmpl.URITemplate] = true
		}
	}
	for uri, found := range want {
		if !found {
			t.Errorf("template %s not advertised", uri)
		}
	}
}

func TestReadClusterTopology(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	rr, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "meshery://clusters/c1/topology",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) == 0 {
		t.Fatal("no contents returned")
	}
	got := rr.Contents[0].Text
	for _, want := range []string{`"displayName":"web"`, `"kind":"hierarchical"`, `"evaluated":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("topology missing %s: %s", want, got)
		}
	}
	if rr.Contents[0].MIMEType != "application/json" {
		t.Errorf("mime type = %s", rr.Contents[0].MIMEType)
	}
}

func TestReadWorkloadsExcludesSecrets(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	rr, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "meshery://clusters/c1/namespaces/prod/workloads",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rr.Contents[0].Text
	if strings.Contains(got, "Secret") || strings.Contains(got, "db-creds") {
		t.Fatalf("Secret leaked into resource output: %s", got)
	}
	if !strings.Contains(got, "web") {
		t.Fatalf("expected the Pod in output: %s", got)
	}
}

func TestReadDesignTopology(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	rr, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "meshery://designs/d1/topology",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rr.Contents[0].Text; !strings.Contains(got, "redis") {
		t.Fatalf("design topology = %s", got)
	}
}

func TestReadUnknownResourceErrors(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "meshery://nope/whatever",
	}); err == nil {
		t.Fatal("expected an error for an unknown resource URI, got nil")
	}
}

func TestSubscribeRoundTrip(t *testing.T) {
	backend := mockTopologyMeshery(t)
	defer backend.Close()
	cs := connectTo(t, newServer(meshery.New(backend.URL, "tok", "prov")))

	ctx := context.Background()
	uri := "meshery://clusters/c1/topology"
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := cs.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
}

func TestSubscriptionsRefCount(t *testing.T) {
	subs := newSubscriptions()
	subs.add("a")
	subs.add("a")
	subs.remove("a")
	if len(subs.uris()) != 1 {
		t.Fatalf("uri dropped while a subscriber remained: %v", subs.uris())
	}
	subs.remove("a")
	if len(subs.uris()) != 0 {
		t.Fatalf("uri retained after last unsubscribe: %v", subs.uris())
	}
}
