//go:build ignore

// Command mock_meshery serves the Meshery REST endpoints this server calls,
// with realistic payloads, so the demo can run without a cluster.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("MOCK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9099"
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/system/kubernetes/contexts", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"page":0,"pageSize":25,"totalCount":1,"contexts":[
			{"id":"ctx-7f3a","name":"minikube","server":"https://127.0.0.1:6443","version":"v1.31.0",
			 "connectionId":"conn-42b1","kubernetesServerId":"ksid-9c2e"}]}`)
	})

	mux.HandleFunc("/api/pattern", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"page":0,"pageSize":10,"totalCount":2,"patterns":[
			{"id":"d-1001","name":"bookinfo"},{"id":"d-1002","name":"redis-cache"}]}`)
	})

	mux.HandleFunc("/api/integrations/connections", func(w http.ResponseWriter, r *http.Request) {
		write(w, `{"page":0,"pageSize":25,"totalCount":1,"connections":[
			{"id":"conn-42b1","name":"minikube","kind":"kubernetes","status":"connected"}]}`)
	})

	mux.HandleFunc("/api/system/meshsync/resources/summary", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()["clusterId"]) == 0 {
			// Mirrors the real handler, which answers 400 without a cluster.
			w.WriteHeader(http.StatusBadRequest)
			write(w, `{"error":"clusterIds is required"}`)
			return
		}
		write(w, `{"kinds":[{"kind":"Pod","count":12},{"kind":"Deployment","count":4},{"kind":"Service","count":5}],
			"namespaces":["default","kube-system","payments"]}`)
	})

	mux.HandleFunc("/api/system/meshsync/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("asDesign") == "true" {
			// Note the Secret: the server must filter it out of the graph.
			write(w, `{"totalCount":4,"design":{"name":"minikube","schemaVersion":"designs.meshery.io/v1beta1",
				"components":[
					{"id":"n1","displayName":"productpage","component":{"kind":"Deployment","version":"apps/v1"},"model":{"name":"kubernetes"}},
					{"id":"n2","displayName":"productpage-svc","component":{"kind":"Service","version":"v1"},"model":{"name":"kubernetes"}},
					{"id":"n3","displayName":"db-credentials","component":{"kind":"Secret","version":"v1"},"model":{"name":"kubernetes"}},
					{"id":"n4","displayName":"reviews","component":{"kind":"Deployment","version":"apps/v1"},"model":{"name":"kubernetes"}}],
				"relationships":[
					{"id":"e1","kind":"hierarchical","subType":"parent","type":"non-binding"},
					{"id":"e2","kind":"edge","subType":"network","type":"non-binding"}]}}`)
			return
		}
		write(w, `{"page":0,"pageSize":25,"totalCount":3,"resources":[
			{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"productpage","namespace":"payments"}},
			{"kind":"Pod","apiVersion":"v1","metadata":{"name":"productpage-7d4","namespace":"payments"}},
			{"kind":"Secret","apiVersion":"v1","metadata":{"name":"db-credentials","namespace":"payments"}}]}`)
	})

	mux.HandleFunc("/api/pattern/", func(w http.ResponseWriter, r *http.Request) {
		// Current Meshery returns the design file as a JSON *string* under patternFile.
		write(w, `{"id":"d-1001","name":"bookinfo","patternFile":"{\"name\":\"bookinfo\",\"schemaVersion\":\"designs.meshery.io/v1beta1\",\"components\":[{\"id\":\"c1\",\"displayName\":\"productpage\",\"component\":{\"kind\":\"Deployment\",\"version\":\"apps/v1\"}},{\"id\":\"c2\",\"displayName\":\"tls-cert\",\"component\":{\"kind\":\"Secret\",\"version\":\"v1\"}}],\"relationships\":[{\"id\":\"r1\",\"kind\":\"edge\"}]}"}`)
	})

	// Readiness probe, deliberately outside the request log so it does not
	// look like an unauthenticated call in a demo recording.
	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	root.Handle("/", logCookies(mux))

	log.Printf("mock meshery on %s", addr)
	if err := http.ListenAndServe(addr, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// logCookies records that the server authenticates the way Meshery expects.
func logCookies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, _ := r.Cookie("token")
		prov, _ := r.Cookie("meshery-provider")
		if tok != nil && prov != nil {
			log.Printf("%s %s?%s  cookies: token=%s meshery-provider=%s",
				r.Method, r.URL.Path, r.URL.RawQuery, tok.Value, prov.Value)
		} else {
			log.Printf("%s %s?%s  NO AUTH COOKIES", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		next.ServeHTTP(w, r)
	})
}

func write(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	var buf json.RawMessage = []byte(s)
	_, _ = w.Write(buf)
}
