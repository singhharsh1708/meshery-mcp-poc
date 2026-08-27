package mesheryfake_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/singhharsh1708/meshery-mcp-poc/meshery"
	"github.com/singhharsh1708/meshery-mcp-poc/mesheryfake"
)

const resourcesPath = "/api/system/meshsync/resources"

// naiveGet is a client written the way a Meshery client is usually written the
// first time: a bearer token, no cluster filter, one-based pages. Every one of
// these choices is wrong against a real Meshery, and none of them produce an
// error. The tests below use it to show what the fake catches.
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
	req.Header.Set("Authorization", "Bearer "+mesheryfake.DefaultToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func authedGet(t *testing.T, s *mesheryfake.Server, path, query string) map[string]any {
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

// TestBearerAuthLandsOnLoginPage is the first bug the fake reproduces. A bearer
// token is not a Meshery session, and the failure is not a 401: the request is
// redirected, the redirect is followed, and the client parses an HTML login
// page. A mock that checks the Authorization header would call this a pass.
func TestBearerAuthLandsOnLoginPage(t *testing.T) {
	s := mesheryfake.New(t)

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

// TestLocalProviderAcceptsAnything is why the bug above is latent rather than
// obvious. DefaultLocalProvider.GetSession returns nil unconditionally, so a
// client with no working authentication passes every test against a locally
// started Meshery and fails the first time it meets a remote provider.
func TestLocalProviderAcceptsAnything(t *testing.T) {
	s := mesheryfake.New(t, mesheryfake.WithLocalProvider())

	status, body := naiveGet(t, s.URL(), "/api/system/kubernetes/contexts", "")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(string(body), "minikube") {
		t.Fatalf("a local provider should have served the data anyway, got: %s", body)
	}
}

// TestMissingClusterFilterReturnsNothing is the second bug. Meshery filters
// with cluster_id IN (?), so no filter is an empty IN clause: 200, an empty
// list, and a model that reports the cluster is empty.
func TestMissingClusterFilterReturnsNothing(t *testing.T) {
	s := mesheryfake.New(t)

	out := authedGet(t, s, "/api/system/meshsync/resources", "")
	if n := out["totalCount"].(float64); n != 0 {
		t.Fatalf("totalCount = %v, want 0 without a cluster filter", n)
	}

	out = authedGet(t, s, "/api/system/meshsync/resources", `clusterIds=["`+s.Data().ClusterID()+`"]`)
	if n := out["totalCount"].(float64); n != 4 {
		t.Fatalf("totalCount = %v, want 4 with the filter", n)
	}
}

// TestBareClusterIDIsNotAnArray covers the near miss: the parameter is present
// and looks right, but the handler json.Unmarshals it into a []string, so an
// unquoted id fails to parse and the filter ends up empty. Same silent zero.
func TestBareClusterIDIsNotAnArray(t *testing.T) {
	s := mesheryfake.New(t)

	out := authedGet(t, s, "/api/system/meshsync/resources", "clusterIds="+s.Data().ClusterID())
	if n := out["totalCount"].(float64); n != 0 {
		t.Fatalf("totalCount = %v, want 0: a bare id is not a JSON array", n)
	}
}

// TestSummaryUsesADifferentSpelling covers the trap between the two sibling
// endpoints: resources takes a JSON clusterIds array, summary takes a repeated
// singular clusterId and answers 400 without one. Reusing the first spelling on
// the second endpoint fails outright.
func TestSummaryUsesADifferentSpelling(t *testing.T) {
	s := mesheryfake.New(t)
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

// TestPageOneSkipsTheFirstPage is the third bug. Pagination is zero-based on
// both of Meshery's offset paths, so a client that opens at page 1 misses the
// first page of every list. With one seeded cluster, page 1 is empty.
func TestPageOneSkipsTheFirstPage(t *testing.T) {
	s := mesheryfake.New(t)

	out := authedGet(t, s, "/api/system/kubernetes/contexts", "page=1&pageSize=25")
	if ctxs := out["contexts"].([]any); len(ctxs) != 0 {
		t.Fatalf("page 1 returned %d contexts; zero-based paging means the first page is page 0", len(ctxs))
	}

	out = authedGet(t, s, "/api/system/kubernetes/contexts", "page=0&pageSize=25")
	if ctxs := out["contexts"].([]any); len(ctxs) != 1 {
		t.Fatalf("page 0 returned %d contexts, want 1", len(ctxs))
	}
}

// TestLegacyPageSizeSpellingStillWorks pins the fallback, so a client using the
// older lowercase spelling is not broken by the fake when Meshery accepts it.
func TestLegacyPageSizeSpellingStillWorks(t *testing.T) {
	s := mesheryfake.New(t)

	out := authedGet(t, s, "/api/pattern", "pagesize=1")
	if n := len(out["patterns"].([]any)); n != 1 {
		t.Fatalf("pagesize=1 returned %d designs, want 1", n)
	}
}

// TestAsDesignClearsTheFlatList covers the undocumented topology path: setting
// asDesign moves the answer into a design and empties resources. A client that
// keeps reading resources gets an empty list and no error.
func TestAsDesignClearsTheFlatList(t *testing.T) {
	s := mesheryfake.New(t)

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

// TestDesignFileIsAJSONString covers the shape trap on designs. patternFile is
// a JSON string under a camelCase key on current Meshery. Decoding it as a
// nested object, or looking only for pattern_file, yields an empty design with
// no error at all.
func TestDesignFileIsAJSONString(t *testing.T) {
	s := mesheryfake.New(t)

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

// TestOrgScopedEndpointsRequireOrgID covers the last silent-400 family.
func TestOrgScopedEndpointsRequireOrgID(t *testing.T) {
	s := mesheryfake.New(t)

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

// TestRegistryIsUnauthenticated pins the one family that genuinely needs no
// session, so a client is not made to authenticate where Meshery does not.
func TestRegistryIsUnauthenticated(t *testing.T) {
	s := mesheryfake.New(t)

	status, body := naiveGet(t, s.URL(), "/api/registry/models", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(string(body), "Sign in") {
		t.Fatal("registry routes are NoAuth in Meshery and should not redirect")
	}
}

// TestRealClientSatisfiesEveryAssertion is the positive control. The client in
// this repository is driven through the fake and every assertion the package
// offers is applied to what it actually sent. If the assertions were vacuous,
// this would pass with a broken client; the tests above show they are not.
func TestRealClientSatisfiesEveryAssertion(t *testing.T) {
	s := mesheryfake.New(t)
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

	// Fields that would carry Secret data, last-applied-config and the rest of
	// an object's payload to the model. None are ever requested.
	for _, field := range []string{"spec", "status", "labels", "annotations"} {
		s.AssertNoQuery(t, resourcesPath, field)
	}

	// A read-only server touches no mutating route.
	for _, path := range []string{"/api/pattern/deploy", "/api/pattern/import"} {
		s.AssertNotCalled(t, path)
	}
}

// recorder captures assertion failures instead of reporting them, so a test can
// check that an assertion fires. Fatalf panics with a sentinel, which stands in
// for the runtime.Goexit a real *testing.T would perform.
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

// run applies an assertion and reports whether it failed, along with the
// message, so a test can check the message explains the trap rather than only
// reporting a mismatch.
func (r *recorder) run(assert func(mesheryfake.T)) (failed bool, msg string) {
	// The result is set here rather than after assert returns, because Fatalf
	// unwinds through this defer and never reaches the return below.
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

// TestAssertionsFailOnABrokenClient proves the assertions are not vacuous, by
// driving the fake with a client that gets each thing wrong and checking the
// matching assertion fires. Without this, a passing suite would only mean the
// assertions never say anything.
func TestAssertionsFailOnABrokenClient(t *testing.T) {
	cases := []struct {
		name    string
		drive   func(t *testing.T, s *mesheryfake.Server)
		assert  func(s *mesheryfake.Server) func(mesheryfake.T)
		explain string
	}{
		{
			name: "bearer auth instead of cookies",
			drive: func(t *testing.T, s *mesheryfake.Server) {
				naiveGet(t, s.URL(), "/api/system/meshsync/resources", "")
			},
			assert:  func(s *mesheryfake.Server) func(mesheryfake.T) { return s.AssertAuthenticated },
			explain: "Authorization header",
		},
		{
			name: "no cluster filter",
			drive: func(t *testing.T, s *mesheryfake.Server) {
				authedGet(t, s, "/api/system/meshsync/resources", "")
			},
			assert: func(s *mesheryfake.Server) func(mesheryfake.T) {
				return func(tt mesheryfake.T) { s.AssertClusterScoped(tt, resourcesPath) }
			},
			explain: "empty IN clause",
		},
		{
			name: "cluster id sent unquoted",
			drive: func(t *testing.T, s *mesheryfake.Server) {
				authedGet(t, s, "/api/system/meshsync/resources", "clusterIds=ksid-9c2e")
			},
			assert: func(s *mesheryfake.Server) func(mesheryfake.T) {
				return func(tt mesheryfake.T) { s.AssertClusterScoped(tt, resourcesPath) }
			},
			explain: "not a JSON array",
		},
		{
			name: "summary given the plural spelling",
			drive: func(t *testing.T, s *mesheryfake.Server) {
				authedGet(t, s, resourcesPath+"/summary", `clusterIds=["ksid-9c2e"]`)
			},
			assert: func(s *mesheryfake.Server) func(mesheryfake.T) {
				return func(tt mesheryfake.T) { s.AssertClusterScoped(tt, resourcesPath+"/summary") }
			},
			explain: "repeated singular clusterId",
		},
		{
			name: "one-based paging",
			drive: func(t *testing.T, s *mesheryfake.Server) {
				authedGet(t, s, "/api/pattern", "page=1")
			},
			assert: func(s *mesheryfake.Server) func(mesheryfake.T) {
				return func(tt mesheryfake.T) { s.AssertZeroBasedPaging(tt, "/api/pattern") }
			},
			explain: "skips the first",
		},
		{
			name:  "endpoint never called",
			drive: func(t *testing.T, s *mesheryfake.Server) {},
			assert: func(s *mesheryfake.Server) func(mesheryfake.T) {
				return func(tt mesheryfake.T) { tt.Helper(); s.AssertCalled(tt, "/api/system/kubernetes/contexts") }
			},
			explain: "no request to",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mesheryfake.New(t)
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
