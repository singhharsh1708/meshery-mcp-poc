package mesheryfake

import (
	"net/http"
	"strings"
)

func (s *Server) routes(mux *http.ServeMux) {
	// The login page a redirect-following client lands on.
	mux.HandleFunc("/provider", loginPage)
	mux.HandleFunc("/auth/login", loginPage)

	// Unauthenticated by design, matching Meshery.
	mux.HandleFunc("/api/system/version", s.handleVersion)
	mux.HandleFunc("/api/registry/", s.handleRegistry)

	mux.HandleFunc("/api/system/kubernetes/contexts", s.guard(s.handleContexts))
	mux.HandleFunc("/api/integrations/connections", s.guard(s.handleConnections))
	mux.HandleFunc("/api/system/meshsync/resources", s.guard(s.handleResources))
	mux.HandleFunc("/api/system/meshsync/resources/summary", s.guard(s.handleSummary))
	mux.HandleFunc("/api/pattern", s.guard(s.handlePatterns))
	mux.HandleFunc("/api/pattern/", s.guard(s.handlePatternByID))
	mux.HandleFunc("/api/environments", s.guard(s.handleEnvironments))
	mux.HandleFunc("/api/workspaces", s.guard(s.handleWorkspaces))
	mux.HandleFunc("/api/identity/orgs", s.guard(s.handleOrgs))
}

// guard applies Meshery's authentication behaviour: a redirect, not a 401.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			s.redirectUnauthenticated(w, r)
			return
		}
		h(w, r)
	}
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"build":           "v0.8.0",
		"commitsha":       "abc1234",
		"release_channel": "stable",
	})
}

// handleRegistry stands in for the /api/registry family, which is NoAuth.
func (s *Server) handleRegistry(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"page": 0, "pageSize": 25, "totalCount": 0, "models": []any{},
	})
}

func (s *Server) handleContexts(w http.ResponseWriter, r *http.Request) {
	page, size := pageParams(r.URL.Query())
	total := len(s.data.Contexts)
	start, end := paginate(total, page, size)
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       page,
		"pageSize":   size,
		"totalCount": total,
		"contexts":   s.data.Contexts[start:end],
	})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, size := pageParams(q)

	var filtered []Connection
	kinds := q["kind"]
	for _, c := range s.data.Connections {
		if len(kinds) == 0 || contains(kinds, c.Kind) {
			filtered = append(filtered, c)
		}
	}
	start, end := paginate(len(filtered), page, size)
	writeJSON(w, http.StatusOK, map[string]any{
		"page":        page,
		"pageSize":    size,
		"totalCount":  len(filtered),
		"connections": filtered[start:end],
	})
}

// handleResources reproduces the cluster filter that fails by returning
// nothing. Without clusterIds the real handler builds "cluster_id IN ()",
// which matches no rows, and still answers 200.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, size := pageParams(q)
	ids, present := parseClusterIDs(q)

	var filtered []Resource
	if present {
		for _, res := range s.data.Resources {
			if !contains(ids, res.ClusterID) {
				continue
			}
			if kinds := q["kind"]; len(kinds) > 0 && !contains(kinds, res.Kind) {
				continue
			}
			if ns := q["namespace"]; len(ns) > 0 && !contains(ns, res.Metadata.Namespace) {
				continue
			}
			filtered = append(filtered, res)
		}
	}
	// present == false leaves filtered nil, which is the whole point.

	if q.Get("asDesign") == "true" {
		s.writeAsDesign(w, page, size, filtered)
		return
	}

	start, end := paginate(len(filtered), page, size)
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       page,
		"pageSize":   size,
		"totalCount": len(filtered),
		"resources":  filtered[start:end],
	})
}

// writeAsDesign reproduces the undocumented asDesign path: the flat resource
// list is cleared and a design carrying components and relationships is
// returned instead.
func (s *Server) writeAsDesign(w http.ResponseWriter, page, size int, res []Resource) {
	components := make([]map[string]any, 0, len(res))
	for _, r := range res {
		components = append(components, map[string]any{
			"id":          r.ID,
			"displayName": r.Metadata.Name,
			"component":   map[string]any{"kind": r.Kind, "version": r.APIVersion},
			"model":       map[string]any{"name": "kubernetes"},
		})
	}
	relationships := []map[string]any{}
	if len(components) > 1 {
		relationships = append(relationships, map[string]any{
			"id": "e1", "kind": "hierarchical", "subType": "parent", "type": "non-binding",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       page,
		"pageSize":   size,
		"totalCount": len(res),
		"resources":  []Resource{}, // cleared, as the real handler does
		"design": map[string]any{
			"name":          "cluster",
			"schemaVersion": "designs.meshery.io/v1beta1",
			"components":    components,
			"relationships": relationships,
		},
	})
}

// handleSummary requires a repeated singular clusterId and answers 400 without
// one, unlike its sibling above which takes a JSON array under clusterIds.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["clusterId"]) == 0 {
		writeError(w, http.StatusBadRequest, "clusterIds is required")
		return
	}
	counts := map[string]int{}
	for _, res := range s.data.Resources {
		counts[res.Kind]++
	}
	kinds := make([]map[string]any, 0, len(counts))
	for k, n := range counts {
		kinds = append(kinds, map[string]any{"kind": k, "count": n})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds":      kinds,
		"namespaces": []string{"payments", "default"},
	})
}

func (s *Server) handlePatterns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, size := pageParams(q)

	var filtered []Design
	search := strings.ToLower(q.Get("search"))
	for _, d := range s.data.Designs {
		if search == "" || strings.Contains(strings.ToLower(d.Name), search) {
			filtered = append(filtered, d)
		}
	}
	start, end := paginate(len(filtered), page, size)
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       page,
		"pageSize":   size,
		"totalCount": len(filtered),
		"patterns":   filtered[start:end],
	})
}

func (s *Server) handlePatternByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/pattern/")
	for _, d := range s.data.Designs {
		if d.ID == id {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	writeError(w, http.StatusNotFound, "design not found")
}

// handleEnvironments requires orgId, as the real handler does.
func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("orgId") == "" {
		writeError(w, http.StatusBadRequest, "orgId is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page": 0, "pageSize": 25, "total_count": 0, "environments": []any{},
	})
}

// handleWorkspaces requires orgId, accepting orgID as the deprecated spelling
// the real handler still honours.
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("orgId") == "" && q.Get("orgID") == "" {
		writeError(w, http.StatusBadRequest, "orgId is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page": 0, "pageSize": 25, "total_count": 0, "workspaces": []any{},
	})
}

func (s *Server) handleOrgs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"page": 0, "pageSize": 25, "total_count": 1,
		"organizations": []map[string]string{{"id": s.data.OrgID, "name": "Default Org"}},
	})
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
