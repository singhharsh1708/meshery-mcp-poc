package mcpera

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HeaderMismatchCode is the JSON-RPC error revision 2026-07-28 reserves for a
// request whose headers disagree with its body, or which is missing a required
// header.
const HeaderMismatchCode = -32020

// HTTPReport is what an HTTP probe found.
type HTTPReport struct {
	// ServesModern is true when a request declaring the modern version was
	// accepted rather than refused.
	ServesModern bool
	// ModernResultIsModern is true when that result carried the modern markers.
	ModernResultIsModern bool
	// RefusedModern carries why a modern request was turned away, when it was.
	RefusedModern string

	// ValidatesHeaderBody is true when a request whose Mcp-Name header disagrees
	// with the tool named in its body is rejected.
	ValidatesHeaderBody bool
	// MismatchStatus and MismatchCode are what that rejection looked like.
	MismatchStatus int
	MismatchCode   int
	// ExecutedMismatched is the hazard: the server ran the tool named in the
	// body while the header named a different one.
	ExecutedMismatched bool

	Notes []string
}

// ProbeHTTP sends a tool call to an MCP endpoint twice, once with the headers
// agreeing with the body and once with the Mcp-Name header naming a different
// tool, and reports whether the disagreement was caught.
//
// Revision 2026-07-28 requires servers to reject a header that disagrees with
// the body, and says why: an intermediary may route on the header while the
// server executes the body. A server predating the rule ignores the header and
// runs whatever the body asked for, so the two sources of truth diverge without
// anything on the wire saying so.
//
// agreeing and mismatched name two tools the server exposes. Both are called,
// so point this at a server whose tools are safe to run.
func ProbeHTTP(ctx context.Context, timeout time.Duration, url, agreeing, mismatched string, headers map[string]string) (*HTTPReport, error) {
	rep := &HTTPReport{}
	client := &http.Client{Timeout: timeout}

	status, body, err := callTool(ctx, client, url, agreeing, agreeing, headers)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", agreeing, err)
	}
	switch {
	case status == http.StatusOK:
		rep.ServesModern = true
		rep.ModernResultIsModern = resultIsModern(body)
	default:
		rep.RefusedModern = fmt.Sprintf("%d: %s", status, firstLine(body))
	}

	// The header names one tool, the body names another.
	status, body, err = callTool(ctx, client, url, mismatched, agreeing, headers)
	if err != nil {
		return nil, fmt.Errorf("calling %s with a mismatched header: %w", agreeing, err)
	}
	rep.MismatchStatus = status
	rep.MismatchCode = errorCode(body)

	switch {
	case rep.MismatchCode == HeaderMismatchCode:
		rep.ValidatesHeaderBody = true
		rep.note(fmt.Sprintf("Rejects a header that disagrees with the body, %d with %d, as revision %s requires.",
			status, HeaderMismatchCode, Version))
	case status == http.StatusOK:
		rep.ExecutedMismatched = true
		rep.note("Ran the tool named in the body while the Mcp-Name header named a different one. " +
			"An intermediary routing on that header and this server disagree about what just executed.")
	default:
		rep.note(fmt.Sprintf("Turned the mismatch away with %d, but not with the %d the revision specifies.",
			status, HeaderMismatchCode))
	}
	return rep, nil
}

func callTool(ctx context.Context, c *http.Client, url, headerName, bodyName string, extra map[string]string) (int, []byte, error) {
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": bodyName, "arguments": map[string]any{},
			"_meta": modernMeta(),
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", Version)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", headerName)
	for k, v := range extra {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// jsonPayload pulls the JSON object out of a response that may be a bare body
// or a single SSE event.
func jsonPayload(body []byte) []byte {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "data:")
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			return []byte(line)
		}
	}
	return nil
}

func errorCode(body []byte) int {
	raw := jsonPayload(body)
	if raw == nil {
		return 0
	}
	var r struct {
		Error *rpcError `json:"error"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Error == nil {
		return 0
	}
	return r.Error.Code
}

func resultIsModern(body []byte) bool {
	raw := jsonPayload(body)
	if raw == nil {
		return false
	}
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Result == nil {
		return false
	}
	return isModernResult(r.Result)
}

func firstLine(body []byte) string {
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func (r *HTTPReport) note(s string) { r.Notes = append(r.Notes, s) }
