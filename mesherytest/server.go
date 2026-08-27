package mesherytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
)

// DefaultToken and DefaultProvider are the credential values the fake expects,
// matching the two cookies mesheryctl writes to ~/.meshery/auth.json.
const (
	DefaultToken    = "fake-session-jwt"
	DefaultProvider = "Meshery"
)

// Request is one call the fake received, recorded so a test can assert on what
// the client actually sent rather than only on what came back.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Cookies map[string]string
}

// Server is a fake Meshery Server. Create one with New.
type Server struct {
	// Token and Provider are the cookie values the fake accepts.
	Token    string
	Provider string

	// ProviderType selects the authentication behaviour. A local provider
	// accepts everything, mirroring DefaultLocalProvider.GetSession
	// (server/models/default_local_provider.go:482), which takes the request as
	// _ and returns nil. A remote provider requires the cookies.
	ProviderType ProviderType

	data *Data

	mu       sync.Mutex
	requests []Request

	httpSrv *httptest.Server
}

// ProviderType selects which of Meshery's two authentication behaviours the
// fake reproduces.
type ProviderType int

const (
	// RemoteProvider requires the token and meshery-provider cookies. This is
	// the stricter and more useful default for tests.
	RemoteProvider ProviderType = iota
	// LocalProvider accepts any request. DefaultLocalProvider.GetSession
	// discards the request and returns nil, which is why an incorrect auth
	// implementation can pass against a locally started Meshery and fail
	// against a remote one.
	LocalProvider
)

// Option configures a Server.
type Option func(*Server)

// WithLocalProvider makes the fake accept unauthenticated requests, mirroring
// a locally started Meshery.
func WithLocalProvider() Option {
	return func(s *Server) { s.ProviderType = LocalProvider }
}

// WithData replaces the seeded fixtures.
func WithData(d *Data) Option {
	return func(s *Server) { s.data = d }
}

// New starts a fake Meshery Server seeded with a small, realistic dataset. The
// server is closed automatically when the test finishes.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()

	s := &Server{
		Token:    DefaultToken,
		Provider: DefaultProvider,
		data:     SeedData(),
	}
	for _, o := range opts {
		o(s)
	}

	mux := http.NewServeMux()
	s.routes(mux)
	s.httpSrv = httptest.NewServer(s.record(mux))
	t.Cleanup(s.httpSrv.Close)
	return s
}

// Data returns the fixtures the fake is serving, so a test can name the seeded
// cluster or design rather than hardcoding an identifier.
func (s *Server) Data() *Data { return s.data }

// URL is the base URL of the fake, suitable as MESHERY_URL.
func (s *Server) URL() string { return s.httpSrv.URL }

// Close shuts the fake down. Tests created via New do not need to call this.
func (s *Server) Close() { s.httpSrv.Close() }

// Requests returns every call the fake received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies := map[string]string{}
		for _, c := range r.Cookies() {
			cookies[c.Name] = c.Value
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.Query(),
			Cookies: cookies,
		})
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// authenticated reports whether the request carries the cookie pair Meshery
// requires. Registry routes bypass this; see routes.
//
// Meshery: RemoteProvider.GetToken (server/models/remote_auth.go:191) reads
// only req.Cookie(TokenCookieName), and mesheryctl sends token alongside
// meshery-provider. No inbound route reads an Authorization header to establish
// a session; the only Authorization headers in the tree are on Meshery's own
// outbound calls to Grafana and Prometheus.
func (s *Server) authenticated(r *http.Request) bool {
	if s.ProviderType == LocalProvider {
		return true
	}
	tok, err := r.Cookie("token")
	if err != nil || tok.Value != s.Token {
		return false
	}
	prov, err := r.Cookie("meshery-provider")
	if err != nil || prov.Value != s.Provider {
		return false
	}
	return true
}

// redirectUnauthenticated reproduces the first thing Meshery does with an
// unauthenticated API call: a 302 to a login page, which a redirect-following
// client turns into HTML with a 200 and a failure inside its JSON decoder.
//
// Meshery: AuthMiddleware sends a failed remote-provider auth through
// HandleUnAuthenticated (server/models/remote_provider.go:1049), which
// redirects to /auth/login when the meshery-provider cookie is present and to
// /provider when it is not. SessionInjectorMiddleware
// (server/handlers/middlewares.go:221) redirects to /provider likewise.
//
// Deliberately not reproduced: Meshery counts attempts in a cookie and answers
// 401 once retries reach MaxAuthRetries, which is 3
// (server/models/remote_auth.go:39), and AuthMiddleware answers 401 outright
// when an enforced provider key does not match
// (server/handlers/middlewares.go:169). Neither is the case a client meets
// first, and reproducing the retry counter would make the fake stateful for no
// gain, so the fake always takes the redirect branch.
func (s *Server) redirectUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("meshery-provider"); err != nil {
		http.Redirect(w, r, "/provider", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

// loginPage is what a redirect-following client actually receives: HTML with a
// 200, which is why the failure surfaces as a JSON parse error.
func loginPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><html><body>Sign in to Meshery</body></html>"))
}

// unlimited is the page size that means "no limit", which is what Meshery does
// for pageSize=all.
const unlimited = -1

// paginate applies Meshery's pagination arithmetic.
//
// Meshery: getPaginationParams computes offset = page * limit
// (server/handlers/utils.go:116), and models.Paginate does
// offset := (page) * pageSize (server/models/persister_utils.go:10). Both are
// zero-based, so page=1 skips the first page. Meshery's own callers open with
// page := 0. Negative pages are clamped to 0, as getPaginationParams does.
func paginate(total, page, pageSize int) (start, end int) {
	if page < 0 {
		page = 0
	}
	if pageSize == unlimited {
		return 0, total
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	start = page * pageSize
	if start > total {
		start = total
	}
	end = start + pageSize
	if end > total {
		end = total
	}
	return start, end
}

const defaultPageSize = 25

// pageParams reads the pagination parameters the way Meshery does: pageSize is
// canonical, pagesize is accepted as the legacy spelling.
func pageParams(q url.Values) (page, pageSize int) {
	page, _ = strconv.Atoi(q.Get("page"))
	sizeStr := q.Get("pageSize")
	if sizeStr == "" {
		sizeStr = q.Get("pagesize")
	}
	// Meshery's persisters special-case pageSize=all to fetch every row rather
	// than applying a limit, so it is not a page size at all.
	if sizeStr == "all" {
		return page, unlimited
	}
	pageSize, _ = strconv.Atoi(sizeStr)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return page, pageSize
}

// clusterFilter is the outcome of reading the clusterIds query parameter. The
// three cases are genuinely different and Meshery treats them differently.
type clusterFilter int

const (
	// clusterFilterAbsent: no clusterIds at all. Meshery sets the filter to an
	// empty slice, so the SQL becomes "cluster_id IN ()", which matches nothing
	// and still answers 200. This is the silent one.
	clusterFilterAbsent clusterFilter = iota
	// clusterFilterMalformed: present but not a JSON array. Meshery answers 400.
	clusterFilterMalformed
	// clusterFilterPresent: a well-formed JSON array.
	clusterFilterPresent
)

// parseClusterIDs reads the JSON-encoded array that
// /api/system/meshsync/resources expects.
//
// Meshery: server/handlers/meshsync_handler.go:267-278 does
// Query().Get("clusterIds"), json.Unmarshals it into a []string, answers 400 if
// that fails, and otherwise sets filter.ClusterIds = []string{} when the
// parameter is absent. Line 283 then builds
// Where("kubernetes_resources.cluster_id IN (?)", filter.ClusterIds).
func parseClusterIDs(q url.Values) ([]string, clusterFilter) {
	raw := q.Get("clusterIds")
	if raw == "" {
		return nil, clusterFilterAbsent
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, clusterFilterMalformed
	}
	return ids, clusterFilterPresent
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
