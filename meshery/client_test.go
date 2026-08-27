package meshery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(h http.Handler) (*Client, *httptest.Server) {
	srv := httptest.NewServer(h)
	return New(srv.URL, "tok", "prov"), srv
}

func TestListDesignsParsesAndSendsCookies(t *testing.T) {
	var gotPath, gotCookies string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookies = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"page":0,"pageSize":10,"totalCount":2,"patterns":[{"id":"a","name":"nginx"},{"id":"b","name":"redis"}]}`))
	}))
	defer srv.Close()

	out, err := c.ListDesigns(context.Background(), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalCount != 2 || len(out.Patterns) != 2 || out.Patterns[0].Name != "nginx" {
		t.Fatalf("bad parse: %+v", out)
	}
	if gotPath != "/api/pattern" {
		t.Errorf("path = %s, want /api/pattern", gotPath)
	}
	if !strings.Contains(gotCookies, "token=tok") || !strings.Contains(gotCookies, "meshery-provider=prov") {
		t.Errorf("auth cookies not sent: %q", gotCookies)
	}
}

func TestListKubernetesResourcesExcludesSecrets(t *testing.T) {
	var rawQuery string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":2,"resources":[
			{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"web","namespace":"default"}},
			{"kind":"Secret","apiVersion":"v1","metadata":{"name":"db-creds","namespace":"default"}}
		]}`))
	}))
	defer srv.Close()

	out, err := c.ListKubernetesResources(context.Background(), "c1", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Resources) != 1 {
		t.Fatalf("want 1 non-secret resource, got %d: %+v", len(out.Resources), out.Resources)
	}
	for _, r := range out.Resources {
		if r.Kind == "Secret" {
			t.Fatalf("Secret was not excluded: %+v", out.Resources)
		}
	}
	// TotalCount must come down with the filtered row, or the count and the
	// list disagree and a model reports a resource it was never shown.
	if out.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1 after excluding the Secret", out.TotalCount)
	}
	// Scoping the read to a cluster is mandatory: the handler filters with
	// cluster_id IN (?), so an absent value returns nothing at all.
	if !strings.Contains(rawQuery, `clusterIds=%5B%22c1%22%5D`) {
		t.Errorf("clusterIds not sent as a JSON array: %s", rawQuery)
	}
	// The client must never request the columns that carry Secret payloads.
	for _, bad := range []string{"spec=", "status=", "labels=", "annotations="} {
		if strings.Contains(rawQuery, bad) {
			t.Errorf("client leaked query param %q: %s", bad, rawQuery)
		}
	}
}

// TestListKubernetesResourcesRefusesSecretKind pins that asking for Secrets is
// refused outright rather than by dropping the filter, which would widen the
// request to every other kind and return that as the answer.
func TestListKubernetesResourcesRefusesSecretKind(t *testing.T) {
	called := false
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":3,"resources":[
			{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"web","namespace":"default"}},
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"web-0","namespace":"default"}}
		]}`))
	}))
	defer srv.Close()

	out, err := c.ListKubernetesResources(context.Background(), "c1", "Secret", "", 0, 0)
	if err == nil {
		t.Fatalf("expected a refusal for kind=Secret, got %+v", out)
	}
	if !errors.Is(err, ErrSecretKindRefused) {
		t.Errorf("error = %v, want ErrSecretKindRefused", err)
	}
	if called {
		t.Error("no request should reach Meshery when Secrets are requested")
	}
}

// TestListKubernetesResourcesRequiresCluster guards the empty IN clause: an
// absent cluster id makes the handler match zero rows, which reads as an empty
// cluster rather than a mistake.
func TestListKubernetesResourcesRequiresCluster(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a cluster id")
	}))
	defer srv.Close()

	if _, err := c.ListKubernetesResources(context.Background(), "", "", "", 0, 0); err == nil {
		t.Fatal("expected an error when the cluster id is empty")
	}
}

// TestGetMeshSyncSummaryRequiresClusterAndSpelling covers the parameter name
// that differs from its sibling endpoint: summary takes a repeated singular
// clusterId, not the JSON-encoded clusterIds array.
func TestGetMeshSyncSummaryRequiresClusterAndSpelling(t *testing.T) {
	var rawQuery string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"kinds":[{"kind":"Pod","count":3}]}`))
	}))
	defer srv.Close()

	if _, err := c.GetMeshSyncSummary(context.Background()); err == nil {
		t.Error("expected an error with no cluster id, since the endpoint answers 400")
	}
	if _, err := c.GetMeshSyncSummary(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawQuery, "clusterId=c1") {
		t.Errorf("summary must send the singular repeated clusterId: %s", rawQuery)
	}
	if strings.Contains(rawQuery, "clusterIds=") {
		t.Errorf("summary must not send the plural JSON array spelling: %s", rawQuery)
	}
}

// TestListKubernetesContextsParsesIdentifiers pins that the three identifiers
// stay distinct, since conflating them is the main way these tools go wrong.
func TestListKubernetesContextsParsesIdentifiers(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":1,"contexts":[
			{"id":"ctx-1","name":"minikube","server":"https://127.0.0.1:6443","version":"v1.31.0",
			 "connectionId":"conn-1","kubernetesServerId":"ksid-1"}]}`))
	}))
	defer srv.Close()

	out, err := c.ListKubernetesContexts(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contexts) != 1 {
		t.Fatalf("want 1 context, got %d", len(out.Contexts))
	}
	got := out.Contexts[0]
	if got.KubernetesServerID != "ksid-1" || got.ConnectionID != "conn-1" || got.ID != "ctx-1" {
		t.Errorf("identifiers conflated: %+v", got)
	}
}

func TestGetErrorsOnNon200(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := c.ListDesigns(context.Background(), "", 0, 0); err == nil {
		t.Fatal("expected an error on 401, got nil")
	}
}

func TestListKubernetesConnectionsFiltersByKind(t *testing.T) {
	var gotPath, rawQuery, gotCookies string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, rawQuery, gotCookies = r.URL.Path, r.URL.RawQuery, r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":1,"connections":[{"id":"c1","name":"minikube","kind":"kubernetes","status":"connected"}]}`))
	}))
	defer srv.Close()

	out, err := c.ListKubernetesConnections(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalCount != 1 || len(out.Connections) != 1 || out.Connections[0].Name != "minikube" {
		t.Fatalf("bad parse: %+v", out)
	}
	if gotPath != "/api/integrations/connections" {
		t.Errorf("path = %s, want /api/integrations/connections", gotPath)
	}
	if !strings.Contains(rawQuery, "kind=kubernetes") {
		t.Errorf("kind=kubernetes not sent: %s", rawQuery)
	}
	if !strings.Contains(gotCookies, "token=tok") || !strings.Contains(gotCookies, "meshery-provider=prov") {
		t.Errorf("auth cookies not sent: %q", gotCookies)
	}
}

// TestUnconfiguredClientReportsWhy checks a client with no credentials answers
// with the reason rather than attempting a call. The server starts in this
// state on purpose, so that an MCP client completes a handshake and sees the
// configuration problem instead of a process that exited before saying
// anything.
func TestUnconfiguredClientReportsWhy(t *testing.T) {
	c := Unconfigured(errors.New("read meshery token: no such file"))

	_, err := c.ListDesigns(context.Background(), "", 0, 10)
	if err == nil {
		t.Fatal("expected an error from an unconfigured client")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want it to say the client is not configured", err)
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error = %q, want the underlying reason preserved", err)
	}
}
