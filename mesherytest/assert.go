package mesherytest

import (
	"encoding/json"
	"net/http"
	"strings"
)

type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

func (s *Server) find(path string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

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

func (s *Server) AssertCalled(t T, path string) {
	t.Helper()
	s.lastTo(t, path)
}

func (s *Server) AssertNotCalled(t T, path string) {
	t.Helper()
	if n := len(s.find(path)); n > 0 {
		t.Errorf("expected no request to %s, got %d", path, n)
	}
}

func (s *Server) AssertAuthenticated(t T) {
	t.Helper()
	checked := 0
	for _, r := range s.Requests() {
		if isPublic(r) {
			continue
		}
		checked++
		if r.Cookies["token"] != s.Token {
			t.Errorf("%s %s: token cookie = %q, want %q. Meshery reads the session from cookies; RemoteProvider.GetToken looks at req.Cookie(\"token\") and nothing else, so an Authorization header is not a session",
				r.Method, r.Path, r.Cookies["token"], s.Token)
		}

		if got, channel := r.Provider(); got != s.Provider {
			t.Errorf("%s %s: provider = %q (via %s), want %q. Meshery takes it from the meshery-provider cookie, else a header of the same name, else ?provider=",
				r.Method, r.Path, got, orNone(channel), s.Provider)
		}
	}
	if checked == 0 {
		t.Errorf("AssertAuthenticated saw no authenticated request to check")
	}
}

func isPublic(r Request) bool {
	switch r.Path {
	case "/api/system/version", "/provider", "/auth/login", RemoteLoginPath:
		return true
	}
	if r.Method != http.MethodGet {
		return false
	}
	return strings.HasPrefix(r.Path, "/api/registry") ||
		strings.HasPrefix(r.Path, "/api/meshmodels")
}

func orNone(s string) string {
	if s == "" {
		return "no channel"
	}
	return s
}

func (s *Server) AssertQuery(t T, path, key, want string) {
	t.Helper()
	r := s.lastTo(t, path)
	if got := r.Query.Get(key); got != want {
		t.Errorf("%s: %s = %q, want %q (full query: %s)", path, key, got, want, r.Query.Encode())
	}
}

func (s *Server) AssertNoQuery(t T, path, key string) {
	t.Helper()
	r := s.lastTo(t, path)
	if v, ok := r.Query[key]; ok {
		t.Errorf("%s: expected %s to be absent, got %v", path, key, v)
	}
}

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

func (s *Server) AssertPageSizeSpelling(t T, path string) {
	t.Helper()
	r := s.lastTo(t, path)

	camel, hasCamel := r.Query["pageSize"]
	_, hasLower := r.Query["pagesize"]
	if !hasCamel && !hasLower {
		return
	}
	if spellingFor(path) == canonicalFirst || hasLower {
		return
	}
	t.Errorf("%s: sent pageSize=%s, but this endpoint reads only the lowercase pagesize, so that value is ignored and the default of 25 applies. Send pagesize instead, which every endpoint here accepts",
		path, strings.Join(camel, ","))
}
