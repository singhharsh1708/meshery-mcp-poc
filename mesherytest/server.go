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

const (
	DefaultToken    = "fake-session-jwt"
	DefaultProvider = "Meshery"
)

type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Cookies map[string]string
	Headers map[string]string
}

type Server struct {
	Token          string
	Provider       string
	ProviderType   ProviderType
	RemoteLoginURL string

	data     *Data
	httpSrv  *httptest.Server
	mu       sync.Mutex
	requests []Request
}

type ProviderType int

const (
	RemoteProvider ProviderType = iota

	LocalProvider
)

type Option func(*Server)

func WithLocalProvider() Option {
	return func(s *Server) { s.ProviderType = LocalProvider }
}

func WithRemoteLoginURL(url string) Option {
	return func(s *Server) { s.RemoteLoginURL = url }
}

func WithData(d *Data) Option {
	return func(s *Server) { s.data = d }
}

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

func (s *Server) remoteLoginURL() string {
	if s.RemoteLoginURL != "" {
		return s.RemoteLoginURL
	}
	return RemoteLoginPath
}

func (s *Server) Data() *Data { return s.data }

func (s *Server) URL() string { return s.httpSrv.URL }

func (s *Server) Close() { s.httpSrv.Close() }

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
		headers := map[string]string{}
		for name := range r.Header {
			headers[name] = r.Header.Get(name)
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.Query(),
			Cookies: cookies,
			Headers: headers,
		})
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	if s.ProviderType == LocalProvider {
		return true
	}
	tok, err := r.Cookie("token")
	if err != nil || tok.Value != s.Token {
		return false
	}
	return resolveProvider(r) == s.Provider
}

func resolveProvider(r *http.Request) string {
	if ck, err := r.Cookie("meshery-provider"); err == nil && ck.Value != "" {
		return ck.Value
	}
	if hdr := r.Header.Get("meshery-provider"); hdr != "" {
		return hdr
	}
	return r.URL.Query().Get("provider")
}

const RemoteLoginPath = "/remote-provider/login"

func (s *Server) rejectUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("token"); err != nil {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Redirect(w, r, s.remoteLoginURL(), http.StatusFound)
		return
	}
	if _, err := r.Cookie("meshery-provider"); err != nil {
		http.Redirect(w, r, "/provider", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

func (r Request) Provider() (value, channel string) {
	if v := r.Cookies["meshery-provider"]; v != "" {
		return v, "cookie"
	}
	if v := r.Headers[http.CanonicalHeaderKey("meshery-provider")]; v != "" {
		return v, "header"
	}
	if v := r.Query.Get("provider"); v != "" {
		return v, "query"
	}
	return "", ""
}

func loginPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><html><body>Sign in to Meshery</body></html>"))
}

const unlimited = -1

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

	if page > total/pageSize {
		return total, total
	}
	start = page * pageSize
	end = start + pageSize
	if end > total || end < 0 {
		end = total
	}
	return start, end
}

const defaultPageSize = 25

type sizeSpelling int

const (
	lowercaseOnly sizeSpelling = iota

	canonicalFirst
)

var pageSizeSpelling = map[string]sizeSpelling{
	"/api/system/kubernetes/contexts": lowercaseOnly,
	"/api/pattern":                    lowercaseOnly,
	"/api/integrations/connections":   canonicalFirst,
	"/api/system/meshsync/resources":  canonicalFirst,
}

func spellingFor(path string) sizeSpelling {
	if s, ok := pageSizeSpelling[path]; ok {
		return s
	}
	return canonicalFirst
}

func reportedSize(pageSize, returned int) int {
	if pageSize == unlimited {
		return returned
	}
	return pageSize
}

func pageParams(q url.Values, spelling sizeSpelling) (page, pageSize int) {
	page, _ = strconv.Atoi(q.Get("page"))
	sizeStr := q.Get("pagesize")
	if spelling == canonicalFirst {
		if v := q.Get("pageSize"); v != "" {
			sizeStr = v
		}
	}

	if sizeStr == "all" {
		return page, unlimited
	}
	pageSize, _ = strconv.Atoi(sizeStr)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return page, pageSize
}

type clusterFilter int

const (
	clusterFilterAbsent clusterFilter = iota

	clusterFilterMalformed

	clusterFilterPresent
)

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
