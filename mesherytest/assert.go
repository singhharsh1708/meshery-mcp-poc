package mesherytest

import (
	"encoding/json"
	"strings"
)

// T is the subset of *testing.T the assertions need. Taking an interface here
// rather than *testing.T lets a test capture a failure instead of reporting it,
// which is how this package proves its own assertions are not vacuous.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// find returns the recorded requests whose path matches, in order.
func (s *Server) find(path string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// lastTo returns the most recent recorded request to path, failing the test if
// the client never called it.
func (s *Server) lastTo(t T, path string) Request {
	t.Helper()
	got := s.find(path)
	if len(got) == 0 {
		t.Fatalf("no request to %s; the client called: %s", path, strings.Join(s.paths(), ", "))
	}
	return got[len(got)-1]
}

func (s *Server) paths() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range s.Requests() {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r.Path)
		}
	}
	if len(out) == 0 {
		return []string{"(nothing)"}
	}
	return out
}

// AssertCalled fails unless the client called path at least once.
func (s *Server) AssertCalled(t T, path string) {
	t.Helper()
	s.lastTo(t, path)
}

// AssertNotCalled fails if the client called path. Use it for the negative half
// of a guarantee, such as proving a read-only server never touched a mutating
// route.
func (s *Server) AssertNotCalled(t T, path string) {
	t.Helper()
	if n := len(s.find(path)); n > 0 {
		t.Errorf("expected no request to %s, got %d", path, n)
	}
}

// AssertAuthenticated fails unless every recorded request that needed
// credentials carried both cookies. It catches a client that authenticates with
// an Authorization header, which a locally started Meshery accepts and a remote
// one rejects.
func (s *Server) AssertAuthenticated(t T) {
	t.Helper()
	checked := 0
	for _, r := range s.Requests() {
		if isPublicPath(r.Path) {
			continue
		}
		checked++
		if r.Cookies["token"] != s.Token {
			t.Errorf("%s %s: token cookie = %q, want %q. Meshery reads the session only from cookies; no route reads an Authorization header",
				r.Method, r.Path, r.Cookies["token"], s.Token)
		}
		if r.Cookies["meshery-provider"] != s.Provider {
			t.Errorf("%s %s: meshery-provider cookie = %q, want %q",
				r.Method, r.Path, r.Cookies["meshery-provider"], s.Provider)
		}
	}
	if checked == 0 {
		t.Errorf("AssertAuthenticated saw no authenticated request to check")
	}
}

func isPublicPath(path string) bool {
	return path == "/api/system/version" ||
		path == "/provider" ||
		path == "/auth/login" ||
		strings.HasPrefix(path, "/api/registry")
}

// AssertQuery fails unless the last request to path carried key=want.
func (s *Server) AssertQuery(t T, path, key, want string) {
	t.Helper()
	r := s.lastTo(t, path)
	if got := r.Query.Get(key); got != want {
		t.Errorf("%s: %s = %q, want %q (full query: %s)", path, key, got, want, r.Query.Encode())
	}
}

// AssertNoQuery fails if the last request to path carried key at all. Use it to
// prove a field that would widen the blast radius, such as spec or status on a
// MeshSync read, was never requested.
func (s *Server) AssertNoQuery(t T, path, key string) {
	t.Helper()
	r := s.lastTo(t, path)
	if v, ok := r.Query[key]; ok {
		t.Errorf("%s: expected %s to be absent, got %v", path, key, v)
	}
}

// AssertClusterScoped fails unless the last request to path filtered by
// cluster, and unless the filter was encoded the way that endpoint parses it.
//
// This is the assertion that catches the most common way a Meshery client goes
// wrong: omitting the filter, or sending it as a bare string or a repeated
// parameter, all of which return 200 with nothing in it.
func (s *Server) AssertClusterScoped(t T, path string, want ...string) {
	t.Helper()
	r := s.lastTo(t, path)

	if path == "/api/system/meshsync/resources/summary" {
		got := r.Query["clusterId"]
		if len(got) == 0 {
			t.Fatalf("%s: no clusterId. This endpoint takes a repeated singular clusterId and answers 400 without one, unlike its sibling which takes a JSON clusterIds array",
				path)
		}
		assertSameSet(t, path, "clusterId", got, want)
		return
	}

	raw := r.Query.Get("clusterIds")
	if raw == "" {
		t.Fatalf("%s: no clusterIds. Meshery filters with cluster_id IN (?) against whatever it is given, so an absent filter is an empty IN clause: 200 with zero rows, which reads as an empty cluster",
			path)
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%s: clusterIds = %q, which is not a JSON array. The handler json.Unmarshals this value into a []string and answers 400 when that fails, so a bare id is rejected outright rather than filtering by it",
			path, raw)
	}
	assertSameSet(t, path, "clusterIds", got, want)
}

func assertSameSet(t T, path, key string, got, want []string) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("%s: %s = %v, missing %q", path, key, got, w)
		}
	}
}

// AssertZeroBasedPaging fails unless the last request to path opened at page 0.
//
// Meshery's pagination is zero-based on both paths that compute an offset, so a
// client that opens at page 1 silently skips the first page of every list it
// reads. Nothing errors, and a short fixture can hide it entirely.
func (s *Server) AssertZeroBasedPaging(t T, path string) {
	t.Helper()
	r := s.lastTo(t, path)
	page := r.Query.Get("page")
	if page == "" || page == "0" {
		return
	}
	t.Errorf("%s: page = %q. Meshery computes offset = page * pageSize on both pagination paths, so page=1 skips the first %s results rather than returning them",
		path, page, defaultOr(r.Query.Get("pageSize"), "25"))
}

func defaultOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
