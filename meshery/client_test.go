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

	if out.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1 after excluding the Secret", out.TotalCount)
	}

	if !strings.Contains(rawQuery, `clusterIds=%5B%22c1%22%5D`) {
		t.Errorf("clusterIds not sent as a JSON array: %s", rawQuery)
	}

	for _, bad := range []string{"spec=", "status=", "labels=", "annotations="} {
		if strings.Contains(rawQuery, bad) {
			t.Errorf("client leaked query param %q: %s", bad, rawQuery)
		}
	}
}

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

func TestListKubernetesResourcesRequiresCluster(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a cluster id")
	}))
	defer srv.Close()

	if _, err := c.ListKubernetesResources(context.Background(), "", "", "", 0, 0); err == nil {
		t.Fatal("expected an error when the cluster id is empty")
	}
}

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
