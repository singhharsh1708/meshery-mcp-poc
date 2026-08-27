package mesherytest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
	"github.com/singhharsh1708/meshery-mcp-poc/mesherytest"
)

const resourcesPath = "/api/system/meshsync/resources"

func naiveGet(t *testing.T, base, path, query string) (int, []byte) {
	t.Helper()
	u := base + path
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+mesherytest.DefaultToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func authedGet(t *testing.T, s *mesherytest.Server, path, query string) map[string]any {
	t.Helper()
	u := s.URL() + path
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
	req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return out
}

func TestBearerAuthLandsOnLoginPage(t *testing.T) {
	s := mesherytest.New(t)

	status, body := naiveGet(t, s.URL(), "/api/system/meshsync/resources", "")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: Meshery redirects rather than refusing, and the redirect target succeeds", status)
	}
	if !strings.Contains(string(body), "Sign in") {
		t.Fatalf("expected the login page, got: %s", body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err == nil {
		t.Fatal("expected the HTML login page to fail JSON decoding")
	}
}

func TestLocalProviderAcceptsAnything(t *testing.T) {
	s := mesherytest.New(t, mesherytest.WithLocalProvider())

	status, body := naiveGet(t, s.URL(), "/api/system/kubernetes/contexts", "")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(string(body), "minikube") {
		t.Fatalf("a local provider should have served the data anyway, got: %s", body)
	}
}

func TestProviderHasThreeChannels(t *testing.T) {
	const path = "/api/system/kubernetes/contexts"

	for _, tc := range []struct {
		channel string
		apply   func(*http.Request, *mesherytest.Server)
	}{
		{"cookie", func(r *http.Request, s *mesherytest.Server) {
			r.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})
		}},
		{"header", func(r *http.Request, s *mesherytest.Server) {
			r.Header.Set("meshery-provider", s.Provider)
		}},
		{"query", func(r *http.Request, s *mesherytest.Server) {
			q := r.URL.Query()
			q.Set("provider", s.Provider)
			r.URL.RawQuery = q.Encode()
		}},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			s := mesherytest.New(t)
			req, _ := http.NewRequest(http.MethodGet, s.URL()+path, nil)
			req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
			tc.apply(req, s)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "minikube") {
				t.Fatalf("provider via %s was not accepted: %s", tc.channel, body)
			}

			if _, got := s.Requests()[0].Provider(); got != tc.channel {
				t.Errorf("recorded channel = %q, want %q", got, tc.channel)
			}
			s.AssertAuthenticated(t)
		})
	}
}

func TestTokenIsCookieOnly(t *testing.T) {
	s := mesherytest.New(t)

	req, _ := http.NewRequest(http.MethodGet, s.URL()+"/api/system/kubernetes/contexts?token="+s.Token, nil)
	req.Header.Set("token", s.Token)
	req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Sign in") {
		t.Fatalf("a token sent as a header or query param is not a session, got: %s", body)
	}
}

func TestUnauthenticatedNonGetIs404(t *testing.T) {
	s := mesherytest.New(t)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		req, _ := http.NewRequest(method, s.URL()+"/api/pattern", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with no session: status = %d, want 404", method, resp.StatusCode)
		}
	}
}

func TestInvalidTokenTakesTheOtherPath(t *testing.T) {
	s := mesherytest.New(t)

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	for _, tc := range []struct{ provider, want string }{
		{"Meshery", "/auth/login"},
		{"", "/provider"},
	} {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/api/pattern", nil)
		req.AddCookie(&http.Cookie{Name: "token", Value: "stale-or-expired"})
		if tc.provider != "" {
			req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: tc.provider})
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != tc.want {
			t.Errorf("provider=%q: redirected to %q, want %q", tc.provider, got, tc.want)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, s.URL()+"/api/pattern", nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != mesherytest.RemoteLoginPath {
		t.Errorf("with no token: redirected to %q, want %q", got, mesherytest.RemoteLoginPath)
	}
}

func TestMissingClusterFilterReturnsNothing(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/system/meshsync/resources", "")
	if n := out["totalCount"].(float64); n != 0 {
		t.Fatalf("totalCount = %v, want 0 without a cluster filter", n)
	}

	out = authedGet(t, s, "/api/system/meshsync/resources", `clusterIds=["`+s.Data().ClusterID()+`"]`)
	if n := out["totalCount"].(float64); n != 4 {
		t.Fatalf("totalCount = %v, want 4 with the filter", n)
	}
}

func TestBareClusterIDIsRejected(t *testing.T) {
	s := mesherytest.New(t)

	req, _ := http.NewRequest(http.MethodGet,
		s.URL()+"/api/system/meshsync/resources?clusterIds="+s.Data().ClusterID(), nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
	req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a bare id is not a JSON array", resp.StatusCode)
	}
}

func TestSummaryUsesADifferentSpelling(t *testing.T) {
	s := mesherytest.New(t)
	const path = "/api/system/meshsync/resources/summary"

	req, _ := http.NewRequest(http.MethodGet, s.URL()+path+`?clusterIds=["`+s.Data().ClusterID()+`"]`, nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
	req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when the singular clusterId is absent", resp.StatusCode)
	}

	out := authedGet(t, s, path, "clusterId="+s.Data().ClusterID())
	if out["kinds"] == nil {
		t.Fatalf("expected per-kind counts, got %v", out)
	}
}

func TestSummaryClusterIDIsAPresenceCheck(t *testing.T) {
	s := mesherytest.New(t)
	const path = "/api/system/meshsync/resources/summary"

	for _, q := range []string{"clusterId=", "clusterId=all"} {
		out := authedGet(t, s, path, q)
		if out["kinds"] == nil {
			t.Errorf("%s: expected 200 with a summary, got %v", q, out)
		}
	}

	failed, _ := (&recorder{}).run(func(tt mesherytest.T) {
		s.AssertClusterScoped(tt, path, s.Data().ClusterID())
	})
	if !failed {
		t.Error("clusterId=all should not satisfy a cluster-scoping assertion")
	}
}

func TestPageOneSkipsTheFirstPage(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/system/kubernetes/contexts", "page=1&pageSize=25")
	if ctxs := out["contexts"].([]any); len(ctxs) != 0 {
		t.Fatalf("page 1 returned %d contexts; zero-based paging means the first page is page 0", len(ctxs))
	}

	out = authedGet(t, s, "/api/system/kubernetes/contexts", "page=0&pageSize=25")
	if ctxs := out["contexts"].([]any); len(ctxs) != 1 {
		t.Fatalf("page 0 returned %d contexts, want 1", len(ctxs))
	}
}

func TestPageSizeSpellingIsPerEndpoint(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/pattern", "pagesize=1")
	if n := len(out["patterns"].([]any)); n != 1 {
		t.Errorf("lowercase pagesize=1 returned %d designs, want 1", n)
	}
	out = authedGet(t, s, "/api/pattern", "pageSize=1")
	if n := len(out["patterns"].([]any)); n != 25 {
		t.Errorf("camelCase pageSize=1 returned %d designs, want the default of 25: this endpoint ignores that spelling", n)
	}

	cluster := `clusterIds=["` + s.Data().ClusterID() + `"]`
	for _, spelling := range []string{"pageSize=1", "pagesize=1"} {
		out = authedGet(t, s, "/api/system/meshsync/resources", cluster+"&"+spelling)
		if n := len(out["resources"].([]any)); n != 1 {
			t.Errorf("%s returned %d resources, want 1: this endpoint reads both spellings", spelling, n)
		}
	}
}

func TestAssertPageSizeSpellingCatchesTheWrongOne(t *testing.T) {
	s := mesherytest.New(t)
	authedGet(t, s, "/api/pattern", "pageSize=1")

	failed, msg := (&recorder{}).run(func(tt mesherytest.T) {
		s.AssertPageSizeSpelling(tt, "/api/pattern")
	})
	if !failed {
		t.Fatal("camelCase pageSize on a lowercase-only endpoint should have been flagged")
	}
	if !strings.Contains(msg, "pagesize") {
		t.Errorf("the failure should name the spelling the endpoint reads, got: %s", msg)
	}

	s2 := mesherytest.New(t)
	authedGet(t, s2, "/api/pattern", "pagesize=1")
	if failed, msg := (&recorder{}).run(func(tt mesherytest.T) {
		s2.AssertPageSizeSpelling(tt, "/api/pattern")
	}); failed {
		t.Errorf("lowercase pagesize should be accepted here, got: %s", msg)
	}
}

func TestPageSizeAllReturnsEverything(t *testing.T) {
	s := mesherytest.New(t)
	total := len(s.Data().Designs)
	if total <= 25 {
		t.Fatalf("fixture has %d designs; the test cannot distinguish no-limit from the default below 26", total)
	}

	out := authedGet(t, s, "/api/pattern", "pagesize=all")
	if n := len(out["patterns"].([]any)); n != total {
		t.Fatalf("pagesize=all returned %d designs, want all %d", n, total)
	}

	out = authedGet(t, s, "/api/pattern", "")
	if n := len(out["patterns"].([]any)); n != 25 {
		t.Fatalf("no page size returned %d designs, want the default of 25", n)
	}

	out = authedGet(t, s, "/api/pattern", "pagesize=all")
	if got := out["pageSize"].(float64); got != float64(total) {
		t.Errorf("pageSize = %v with no limit, want the row count %d", got, total)
	}
}

func TestNegativePageIsClamped(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/pattern", "page=-3&pagesize=2")
	if n := len(out["patterns"].([]any)); n != 2 {
		t.Fatalf("page=-3 returned %d designs, want the first page of 2", n)
	}
	if out["patterns"].([]any)[0].(map[string]any)["id"] != "d-1001" {
		t.Error("page=-3 should have been clamped to the first page")
	}
}

func TestPageBeyondTheEndIsEmptyNotAPanic(t *testing.T) {
	s := mesherytest.New(t)

	total := len(s.Data().Designs)
	out := authedGet(t, s, "/api/pattern", "page=99&pagesize=25")
	if n := len(out["patterns"].([]any)); n != 0 {
		t.Fatalf("page 99 returned %d designs, want 0", n)
	}
	if n := out["totalCount"].(float64); n != float64(total) {
		t.Fatalf("totalCount = %v, want the unpaginated total of %d", n, total)
	}
}

func TestAsDesignClearsTheFlatList(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/system/meshsync/resources",
		`asDesign=true&clusterIds=["`+s.Data().ClusterID()+`"]`)

	if n := len(out["resources"].([]any)); n != 0 {
		t.Fatalf("resources = %d, want 0: asDesign clears the flat list", n)
	}
	design, ok := out["design"].(map[string]any)
	if !ok {
		t.Fatalf("no design in the response: %v", out)
	}
	if n := len(design["components"].([]any)); n != 4 {
		t.Fatalf("components = %d, want 4", n)
	}
}

func TestDesignFileIsAJSONString(t *testing.T) {
	s := mesherytest.New(t)

	out := authedGet(t, s, "/api/pattern/d-1001", "")
	raw, ok := out["patternFile"].(string)
	if !ok {
		t.Fatalf("patternFile = %T, want a JSON string", out["patternFile"])
	}
	var pf map[string]any
	if err := json.Unmarshal([]byte(raw), &pf); err != nil {
		t.Fatalf("patternFile did not parse as JSON: %v", err)
	}
	if pf["name"] != "bookinfo" {
		t.Fatalf("design name = %v", pf["name"])
	}
	if _, legacy := out["pattern_file"]; legacy {
		t.Error("current Meshery does not serve pattern_file; a client that only reads it should fail here")
	}
}

func TestOrgScopedEndpointsRequireOrgID(t *testing.T) {
	s := mesherytest.New(t)

	for _, path := range []string{"/api/environments", "/api/workspaces"} {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+path, nil)
		req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
		req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 without orgId", path, resp.StatusCode)
		}
	}
}

func TestRegistryReadsAreUnauthenticated(t *testing.T) {
	s := mesherytest.New(t)

	status, body := naiveGet(t, s.URL(), "/api/registry/models", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(string(body), "Sign in") {
		t.Fatal("registry GETs are NoAuth in Meshery and should not redirect")
	}
}

func TestRegistryWritesStillNeedASession(t *testing.T) {
	s := mesherytest.New(t)

	req, _ := http.NewRequest(http.MethodPost, s.URL()+"/api/registry/register", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unauthenticated registry write", resp.StatusCode)
	}

	failed, msg := (&recorder{}).run(s.AssertAuthenticated)
	if !failed {
		t.Error("AssertAuthenticated exempted an unauthenticated registry write")
	}
	if !strings.Contains(msg, "/api/registry/register") {
		t.Errorf("expected the write to be named in the failure, got: %s", msg)
	}
}

func TestRealClientSatisfiesEveryAssertion(t *testing.T) {
	s := mesherytest.New(t)
	c := meshery.New(s.URL(), s.Token, s.Provider)
	ctx := context.Background()
	cluster := s.Data().ClusterID()

	contexts, err := c.ListKubernetesContexts(ctx, 0, 25)
	if err != nil {
		t.Fatalf("ListKubernetesContexts: %v", err)
	}
	if len(contexts.Contexts) != 1 {
		t.Fatalf("contexts = %d, want 1", len(contexts.Contexts))
	}

	res, err := c.ListKubernetesResources(ctx, cluster, "", "", 0, 25)
	if err != nil {
		t.Fatalf("ListKubernetesResources: %v", err)
	}
	if len(res.Resources) != 3 {
		t.Fatalf("resources = %d, want 3 of 4 with the Secret excluded", len(res.Resources))
	}
	for _, r := range res.Resources {
		if r.Kind == "Secret" {
			t.Errorf("Secret %s reached the caller", r.Metadata.Name)
		}
	}

	if _, err := c.GetMeshSyncSummary(ctx, cluster); err != nil {
		t.Fatalf("GetMeshSyncSummary: %v", err)
	}
	if _, err := c.ListDesigns(ctx, "", 0, 25); err != nil {
		t.Fatalf("ListDesigns: %v", err)
	}
	if _, err := c.ListKubernetesConnections(ctx, 0, 25); err != nil {
		t.Fatalf("ListKubernetesConnections: %v", err)
	}

	topo, err := c.GetClusterTopology(ctx, cluster)
	if err != nil {
		t.Fatalf("GetClusterTopology: %v", err)
	}
	if topo.ExcludedSecrets != 1 {
		t.Errorf("excludedSecrets = %d, want 1", topo.ExcludedSecrets)
	}
	if len(topo.Components) != 3 {
		t.Errorf("components = %d, want 3", len(topo.Components))
	}

	design, err := c.GetDesignTopology(ctx, "d-1001")
	if err != nil {
		t.Fatalf("GetDesignTopology: %v", err)
	}
	if len(design.Components) != 1 {
		t.Errorf("design components = %d, want 1 with the Secret excluded", len(design.Components))
	}

	s.AssertAuthenticated(t)
	s.AssertCalled(t, "/api/system/kubernetes/contexts")
	s.AssertClusterScoped(t, resourcesPath, cluster)
	s.AssertClusterScoped(t, resourcesPath+"/summary", cluster)
	s.AssertZeroBasedPaging(t, "/api/pattern")
	s.AssertZeroBasedPaging(t, "/api/integrations/connections")
	s.AssertQuery(t, "/api/integrations/connections", "kind", "kubernetes")

	for _, field := range []string{"spec", "status", "labels", "annotations"} {
		s.AssertNoQuery(t, resourcesPath, field)
	}

	for _, path := range []string{"/api/pattern/deploy", "/api/pattern/import"} {
		s.AssertNotCalled(t, path)
	}
}

type recorder struct {
	failures []string
}

type fatal struct{}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(fatal{})
}

func (r *recorder) run(assert func(mesherytest.T)) (failed bool, msg string) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatal); !ok {
				panic(p)
			}
		}
		failed, msg = len(r.failures) > 0, strings.Join(r.failures, "; ")
	}()
	assert(r)
	return
}

func TestAssertionsFailOnABrokenClient(t *testing.T) {
	cases := []struct {
		name    string
		drive   func(t *testing.T, s *mesherytest.Server)
		assert  func(s *mesherytest.Server) func(mesherytest.T)
		explain string
	}{
		{
			name: "bearer auth instead of cookies",
			drive: func(t *testing.T, s *mesherytest.Server) {
				naiveGet(t, s.URL(), "/api/system/meshsync/resources", "")
			},
			assert:  func(s *mesherytest.Server) func(mesherytest.T) { return s.AssertAuthenticated },
			explain: "Authorization header",
		},
		{
			name: "no cluster filter",
			drive: func(t *testing.T, s *mesherytest.Server) {
				authedGet(t, s, "/api/system/meshsync/resources", "")
			},
			assert: func(s *mesherytest.Server) func(mesherytest.T) {
				return func(tt mesherytest.T) { s.AssertClusterScoped(tt, resourcesPath) }
			},
			explain: "empty IN clause",
		},
		{
			name: "cluster id sent unquoted",
			drive: func(t *testing.T, s *mesherytest.Server) {
				authedGet(t, s, "/api/system/meshsync/resources", "clusterIds=ksid-9c2e")
			},
			assert: func(s *mesherytest.Server) func(mesherytest.T) {
				return func(tt mesherytest.T) { s.AssertClusterScoped(tt, resourcesPath) }
			},
			explain: "not a JSON array",
		},
		{
			name: "summary given the plural spelling",
			drive: func(t *testing.T, s *mesherytest.Server) {
				authedGet(t, s, resourcesPath+"/summary", `clusterIds=["ksid-9c2e"]`)
			},
			assert: func(s *mesherytest.Server) func(mesherytest.T) {
				return func(tt mesherytest.T) { s.AssertClusterScoped(tt, resourcesPath+"/summary") }
			},
			explain: "repeated singular clusterId",
		},
		{
			name: "one-based paging",
			drive: func(t *testing.T, s *mesherytest.Server) {
				authedGet(t, s, "/api/pattern", "page=1")
			},
			assert: func(s *mesherytest.Server) func(mesherytest.T) {
				return func(tt mesherytest.T) { s.AssertZeroBasedPaging(tt, "/api/pattern") }
			},
			explain: "skips the first",
		},
		{
			name:  "endpoint never called",
			drive: func(t *testing.T, s *mesherytest.Server) {},
			assert: func(s *mesherytest.Server) func(mesherytest.T) {
				return func(tt mesherytest.T) { tt.Helper(); s.AssertCalled(tt, "/api/system/kubernetes/contexts") }
			},
			explain: "no request to",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mesherytest.New(t)
			tc.drive(t, s)

			failed, msg := (&recorder{}).run(tc.assert(s))
			if !failed {
				t.Fatal("the assertion passed on a client that is wrong, so it would not catch this in review")
			}
			if !strings.Contains(msg, tc.explain) {
				t.Errorf("failure message should explain the trap, wanted %q in:\n%s", tc.explain, msg)
			}
		})
	}
}

func TestDoubleCloseIsSafe(t *testing.T) {
	s := mesherytest.New(t)
	s.Close()
	s.Close()
}

func TestConcurrentUse(t *testing.T) {
	s := mesherytest.New(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			authedGet(t, s, "/api/pattern", "")
			_ = s.Requests()
			_ = s.Data().ClusterID()
		}()
	}
	wg.Wait()
	if n := len(s.Requests()); n != 20 {
		t.Fatalf("recorded %d requests, want 20", n)
	}
}

func TestGarbagePaginationDoesNotPanic(t *testing.T) {
	s := mesherytest.New(t)
	for _, q := range []string{
		"page=abc&pagesize=xyz",
		"page=999999999999999999999",
		"pagesize=-5",
		"page=&pagesize=",
		"pagesize=0",
		"page=2147483647&pagesize=2147483647",

		"page=1&pagesize=9223372036854775807",
		"page=2&pagesize=4611686018427387904",
		"page=4611686018427387904&pagesize=2",
	} {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/api/pattern?"+q, nil)
		req.AddCookie(&http.Cookie{Name: "token", Value: s.Token})
		req.AddCookie(&http.Cookie{Name: "meshery-provider", Value: s.Provider})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: handler did not answer: %v", q, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("%s: status %d", q, resp.StatusCode)
		}
	}
}
