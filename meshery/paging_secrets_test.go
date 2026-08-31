package meshery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAllPagesAsksForEveryRow pins the sentinel onto the wire. Meshery treats
// the page size "all" as "apply no limit", so it is the only way to ask for a
// whole namespace in one read.
func TestAllPagesAsksForEveryRow(t *testing.T) {
	var got string
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("pagesize")
		_, _ = w.Write([]byte(`{"page":0,"pageSize":2,"totalCount":2,"resources":[
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"a","namespace":"prod"}},
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"b","namespace":"prod"}}]}`))
	}))
	defer srv.Close()

	if _, err := c.ListWorkloads(context.Background(), "c1", "prod", 0, AllPages); err != nil {
		t.Fatal(err)
	}
	if got != "all" {
		t.Fatalf("pagesize = %q, want %q: a numeric size would truncate the namespace", got, "all")
	}
}

// TestListWorkloadsRefusesACountWithoutRows is the guard every sibling list
// method already had. A renamed resources key decodes into an empty slice
// beside a non-zero count, which is indistinguishable from an empty namespace.
func TestListWorkloadsRefusesACountWithoutRows(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// totalCount says 7, and the rows arrive under a key this client does
		// not read, which is exactly what a shape change looks like.
		_, _ = w.Write([]byte(`{"page":0,"pageSize":25,"totalCount":7,"items":[{"kind":"Pod"}]}`))
	}))
	defer srv.Close()

	_, err := c.ListWorkloads(context.Background(), "c1", "prod", 0, 0)
	if err == nil {
		t.Fatal("want an error: 7 results were reported and none carried")
	}
	if !strings.Contains(err.Error(), "shape may have changed") {
		t.Fatalf("error should name the shape change, got %v", err)
	}
}

// TestSessionRedirectIsNotFollowed covers an expired session. Meshery answers
// with a 302 to a sign-in page that serves HTML under a 200, so following it
// turns an auth failure into a body that parses as neither an object nor an
// error.
func TestSessionRedirectIsNotFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>Sign in to Meshery</body></html>"))
	})
	mux.HandleFunc("/api/pattern", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/user/login?provider=Local", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, "stale", "Local")

	_, err := c.ListDesigns(context.Background(), "", 0, 10)
	if err == nil {
		t.Fatal("want an error for a redirected request")
	}
	if !strings.Contains(err.Error(), "re-authenticate") {
		t.Fatalf("error should name the session, got %v", err)
	}
	if strings.Contains(err.Error(), "shape may have changed") {
		t.Fatalf("an expired session was reported as an API change: %v", err)
	}
}

// TestSecretCountOnlyCorrectedWhenComplete is the disagreeing fixture for the
// two components of the count: what this page holds, and what the server counted
// across every page. A page carrying one Secret out of a larger result cannot
// say how many Secrets the other pages hold, so the total is left alone and
// labelled rather than quietly decremented.
func TestSecretCountOnlyCorrectedWhenComplete(t *testing.T) {
	const paged = `{"page":0,"pageSize":2,"totalCount":9,"resources":[
		{"kind":"Pod","apiVersion":"v1","metadata":{"name":"a","namespace":"prod"}},
		{"kind":"Secret","apiVersion":"v1","metadata":{"name":"s","namespace":"prod"}}]}`
	const complete = `{"page":0,"pageSize":2,"totalCount":2,"resources":[
		{"kind":"Pod","apiVersion":"v1","metadata":{"name":"a","namespace":"prod"}},
		{"kind":"Secret","apiVersion":"v1","metadata":{"name":"s","namespace":"prod"}}]}`

	for _, tc := range []struct {
		name       string
		body       string
		wantTotal  int64
		wantFilter bool
	}{
		// The server counted 9 rows and sent 2. Subtracting the one Secret here
		// would report 8, a number that still counts every Secret on the seven
		// rows this response never carried.
		{"paged", paged, 9, false},
		{"complete", complete, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			out, err := c.ListWorkloads(context.Background(), "c1", "prod", 0, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Resources) != 1 {
				t.Fatalf("Secret was not dropped: %+v", out.Resources)
			}
			if out.ExcludedSecrets != 1 {
				t.Errorf("ExcludedSecrets = %d, want 1", out.ExcludedSecrets)
			}
			if out.TotalCount != tc.wantTotal {
				t.Errorf("TotalCount = %d, want %d", out.TotalCount, tc.wantTotal)
			}
			if out.TotalCountFiltered != tc.wantFilter {
				t.Errorf("TotalCountFiltered = %v, want %v", out.TotalCountFiltered, tc.wantFilter)
			}
		})
	}
}

// TestSummaryExcludesTheSecretKind covers the one path that can report Secrets
// without naming one. The census is filtered like every other path, and the
// count it removed is reported rather than dropped.
func TestSummaryExcludesTheSecretKind(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capitalized keys and a null labels are what a live server sends: the
		// kinds entries are a Go struct carrying no JSON tags.
		_, _ = w.Write([]byte(`{"kinds":[
			{"Kind":"Pod","Model":"kubernetes","Count":5},
			{"Kind":"Secret","Model":"kubernetes","Count":3}],
			"namespaces":["prod"],"labels":null}`))
	}))
	defer srv.Close()

	raw, err := c.GetMeshSyncSummary(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, `"Kind":"Secret"`) {
		t.Fatalf("the Secret kind survived the census: %s", got)
	}
	if !strings.Contains(got, `"excludedSecrets":3`) {
		t.Fatalf("the removed count should be reported: %s", got)
	}
	if !strings.Contains(got, `"Kind":"Pod"`) {
		t.Fatalf("the other kinds should survive: %s", got)
	}
}

// TestSummaryRefusesABodyItCannotRead pins that an unparseable census is not
// forwarded. Passing it through would forward the row the filter exists to
// remove.
func TestSummaryRefusesABodyItCannotRead(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A JSON object the summary decoder cannot read: kinds is a string.
		_, _ = w.Write([]byte(`{"kinds":"Secret=3"}`))
	}))
	defer srv.Close()

	if _, err := c.GetMeshSyncSummary(context.Background(), "c1"); err == nil {
		t.Fatal("want an error rather than forwarding an unfiltered census")
	}
}

// TestClusterTopologyRefusesACountWithoutComponents is the asDesign counterpart
// of the flat list's guard. This path asks for every row, so a cluster the
// server counted rows for has to produce components; a renamed design or
// components key would otherwise arrive as an empty graph, which reads as a
// cluster holding nothing.
func TestClusterTopologyRefusesACountWithoutComponents(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Nine rows counted, and the components arrive under a key this client
		// does not read.
		_, _ = w.Write([]byte(`{"totalCount":9,"design":{"name":"cluster","nodes":[{"id":"n1"}]}}`))
	}))
	defer srv.Close()

	if _, err := c.GetClusterTopology(context.Background(), "c1"); err == nil {
		t.Fatal("want an error: nine rows were counted and no component carried")
	}
}

// TestEmptyClusterTopologyIsNotAnError is the control. A cluster that really
// holds nothing reports no rows and no components, and that agrees.
func TestEmptyClusterTopologyIsNotAnError(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalCount":0,"design":{"name":"cluster","components":[]}}`))
	}))
	defer srv.Close()

	topo, err := c.GetClusterTopology(context.Background(), "c1")
	if err != nil {
		t.Fatalf("an empty cluster is a valid answer: %v", err)
	}
	if len(topo.Components) != 0 {
		t.Errorf("components = %d, want 0", len(topo.Components))
	}
}
