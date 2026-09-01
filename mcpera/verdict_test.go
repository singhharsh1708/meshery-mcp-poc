package mcpera_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/singhharsh1708/meshery-mcp-poc/mcpera"
)

// probe runs a probe against a handler and fails the test if it errors.
func probeHandler(t *testing.T, h http.HandlerFunc) *mcpera.HTTPReport {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	rep, err := mcpera.ProbeHTTP(context.Background(), 3*time.Second, srv.URL, "alpha", "beta", nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return rep
}

// TestNonJSONBodyIsNotAnExecution covers the hazard row of the published table.
// An endpoint fronted by a single-page app or a proxy answers 200 with HTML to
// anything, including a call whose header disagrees with its body. Nothing about
// that says a tool ran, and reporting it as one would put a server that is not
// an MCP server at all in the row reserved for the worst finding.
func TestNonJSONBodyIsNotAnExecution(t *testing.T) {
	rep := probeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>app shell</body></html>"))
	})
	if rep.ExecutedMismatched {
		t.Error("an unreadable body was reported as the mismatched tool having executed")
	}
	if rep.ServesModern {
		t.Error("an endpoint answering HTML was reported as serving the modern era")
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "not JSON-RPC") {
		t.Errorf("the report should say the body was not JSON-RPC: %v", rep.Notes)
	}
}

// TestErrorUnder200IsARefusal is the disagreeing fixture for the two things a
// verdict is built from: the status line and the body. JSON-RPC carries its
// errors in the body, so a server refusing the modern version answers 200 with
// an error object. Reading only the status would publish that refusal as
// service, in the silent-downgrade row.
func TestErrorUnder200IsARefusal(t *testing.T) {
	rep := probeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"unsupported protocol version"}}`))
	})
	if rep.ServesModern {
		t.Error("a JSON-RPC refusal under a 200 was reported as serving the modern era")
	}
	if rep.RefusedModern == "" {
		t.Error("the refusal should be recorded")
	}
	if !strings.Contains(rep.RefusedModern, "-32600") {
		t.Errorf("the refusal should name the error code, got %q", rep.RefusedModern)
	}
	if rep.ExecutedMismatched {
		t.Error("a server that refused both calls cannot have executed the mismatched one")
	}
}

// TestRedirectIsNotFollowed pins that a 3xx is reported rather than chased. Go
// rewrites a redirected POST into a GET against the new location, so a followed
// redirect describes neither the request that was sent nor the endpoint that was
// asked about.
func TestRedirectIsNotFollowed(t *testing.T) {
	var landed bool
	mux := http.NewServeMux()
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		landed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rep, err := mcpera.ProbeHTTP(context.Background(), 3*time.Second, srv.URL, "alpha", "beta", nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if landed {
		t.Error("the probe followed a redirect, so it measured a different endpoint than the one named")
	}
	if rep.ServesModern {
		t.Error("a redirect is not an answer about protocol eras")
	}
	if rep.ExecutedMismatched {
		t.Error("a redirected call did not execute anything")
	}
}

// TestNonJSONOnTheMismatchAloneIsNotAnExecution isolates the mismatch guard.
//
// The server above answers HTML to everything, so the agreeing call already
// fails and the verdict never reaches the question of what the mismatched call
// did. Here the agreeing call is served properly and only the disagreeing one
// is intercepted, which is what a proxy or a filter in front of a real MCP
// server looks like. The header was never judged, so nothing is known about
// whether the tool ran.
func TestNonJSONOnTheMismatchAloneIsNotAnExecution(t *testing.T) {
	rep := probeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if r.Header.Get("Mcp-Name") != req.Params.Name {
			// Intercepted before it reached the server.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><html><body>request blocked</body></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	})
	if !rep.ServesModern {
		t.Fatal("the agreeing call was served, so the probe should say so")
	}
	if rep.ExecutedMismatched {
		t.Error("a body that is not JSON-RPC says nothing about whether the tool ran")
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "not JSON-RPC") {
		t.Errorf("the report should say the body was unreadable: %v", rep.Notes)
	}
}

// TestExecutedMismatchStillReported is the positive control for the three tests
// above. A server that genuinely ignores the header and runs the body must
// still land in the hazard row, otherwise the guards have simply turned the
// finding off.
func TestExecutedMismatchStillReported(t *testing.T) {
	rep := probeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The header is ignored and the body is run, which is what a server
		// predating the rule does.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ran"}]}}`))
	})
	if !rep.ServesModern {
		t.Fatal("this server does serve the agreeing call")
	}
	if !rep.ExecutedMismatched {
		t.Error("a server that ran the body while the header disagreed must still be reported")
	}
}

// TestOversizedBodyIsNotBlamedOnTheServer covers this probe's own limit. A
// response larger than it reads arrives cut in half, parses as nothing, and
// would otherwise be reported as a server that answered something other than
// JSON-RPC. The call was served; only the shape is unknown.
func TestOversizedBodyIsNotBlamedOnTheServer(t *testing.T) {
	// One valid JSON-RPC result, padded past the read limit.
	huge := `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","pad":"` +
		strings.Repeat("x", 9<<20) + `"}}`
	rep := probeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, huge)
	})
	if !rep.BodyTruncated {
		t.Error("a body past the read limit should be reported as truncated")
	}
	if !rep.ServesModern {
		t.Error("the call was served, the size of the answer does not change that")
	}
	if rep.RefusedModern != "" {
		t.Errorf("a large answer is not a refusal, got %q", rep.RefusedModern)
	}
	if rep.ExecutedMismatched {
		t.Error("whether the tool ran is unknown when the body could not be read")
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "larger than") {
		t.Errorf("the notes should name the limit: %v", rep.Notes)
	}
}
