package dualera_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singhharsh1708/meshery-mcp-poc/dualera"
)

// fakeLegacy is a server from before revision 2026-07-28: it answers
// initialize, serves tools, and has never heard of server/discover.
func fakeLegacy(t *testing.T, in io.Reader, out io.Writer) {
	t.Helper()
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || len(msg.ID) == 0 {
			continue // a notification
		}
		reply := func(result any) {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": result})
		}
		switch msg.Method {
		case "initialize":
			reply(map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "wrapped", "version": "1.2.3"},
				"instructions":    "wrapped server instructions",
			})
		case "tools/list":
			reply(map[string]any{"tools": []any{map[string]any{"name": "ping"}}})
		case "tools/call":
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong:" + msg.Params.Name}}})
		case "server/discover":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
				"error": map[string]any{"code": -32601, "message": "Method server/discover not found"}})
		case "boom":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
				"error": map[string]any{"code": -32000, "message": "upstream failed"}})
		}
	}
}

// run drives requests through a bridge sitting in front of a fake legacy
// server, and returns the messages the client saw.
func run(t *testing.T, requests ...map[string]any) []map[string]json.RawMessage {
	t.Helper()

	clientToBridge, clientWrites := io.Pipe()
	bridgeToServer, serverReads := io.Pipe()
	serverToBridge, serverWrites := io.Pipe()
	bridgeOut := &syncBuf{}

	go fakeLegacy(t, bridgeToServer, serverWrites)

	b := dualera.New(serverReads, serverToBridge, bridgeOut)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = b.Run(ctx, clientToBridge); close(done) }()

	enc := json.NewEncoder(clientWrites)
	for _, r := range requests {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	_ = clientWrites.Close()
	<-done

	var out []map[string]json.RawMessage
	for _, line := range strings.Split(bridgeOut.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func modernParams(extra map[string]any) map[string]any {
	p := map[string]any{"_meta": map[string]any{
		dualera.MetaProtocolVersion:    dualera.ModernVersion,
		dualera.MetaClientCapabilities: map[string]any{},
	}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func resultOf(t *testing.T, msgs []map[string]json.RawMessage, id string) map[string]json.RawMessage {
	t.Helper()
	for _, m := range msgs {
		if string(m["id"]) != id {
			continue
		}
		if e, ok := m["error"]; ok {
			t.Fatalf("id %s returned an error: %s", id, e)
		}
		var r map[string]json.RawMessage
		if json.Unmarshal(m["result"], &r) != nil {
			t.Fatalf("id %s result did not decode: %s", id, m["result"])
		}
		return r
	}
	t.Fatalf("no reply for id %s in %d messages", id, len(msgs))
	return nil
}

// TestModernRequestGetsModernMarkers is the point of the bridge. The wrapped
// server answers in the legacy shape; the client must see a modern one, since
// those markers are the only thing that distinguishes a modern peer from a
// silent downgrade.
func TestModernRequestGetsModernMarkers(t *testing.T) {
	msgs := run(t, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": modernParams(map[string]any{"name": "ping", "arguments": map[string]any{}}),
	})
	res := resultOf(t, msgs, "7")

	if got := string(res["resultType"]); got != `"complete"` {
		t.Errorf("resultType = %s, want \"complete\"", got)
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(res["_meta"], &meta) != nil {
		t.Fatalf("_meta did not decode: %s", res["_meta"])
	}
	if _, ok := meta[dualera.MetaServerInfo]; !ok {
		t.Error("the result should carry serverInfo in _meta")
	}
	// The wrapped server's own answer must survive intact.
	if !strings.Contains(string(res["content"]), "pong:ping") {
		t.Errorf("content = %s, want the wrapped server's answer", res["content"])
	}
}

// TestLegacyClientIsUnchanged checks the bridge is transparent to a client that
// still handshakes. Adding modern markers there would be its own compatibility
// break.
func TestLegacyClientIsUnchanged(t *testing.T) {
	msgs := run(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": "2025-11-25"}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "ping", "arguments": map[string]any{}}},
	)

	init := resultOf(t, msgs, "1")
	if got := string(init["protocolVersion"]); got != `"2025-11-25"` {
		t.Errorf("protocolVersion = %s", got)
	}
	call := resultOf(t, msgs, "2")
	if _, ok := call["resultType"]; ok {
		t.Error("a legacy client should not be handed modern markers")
	}
}

// TestDiscoverIsAnsweredFromTheWrappedServer checks the capabilities a modern
// client sees are the ones the wrapped server actually reported, not invented.
func TestDiscoverIsAnsweredFromTheWrappedServer(t *testing.T) {
	msgs := run(t, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "server/discover",
		"params": modernParams(nil),
	})
	res := resultOf(t, msgs, "3")

	var versions []string
	if json.Unmarshal(res["supportedVersions"], &versions) != nil {
		t.Fatalf("supportedVersions did not decode: %s", res["supportedVersions"])
	}
	if len(versions) == 0 || versions[0] != dualera.ModernVersion {
		t.Errorf("supportedVersions = %v, want %s first", versions, dualera.ModernVersion)
	}
	if !strings.Contains(string(res["capabilities"]), "tools") {
		t.Errorf("capabilities = %s, want the wrapped server's own", res["capabilities"])
	}
	if !strings.Contains(string(res["instructions"]), "wrapped server instructions") {
		t.Errorf("instructions = %s, want the wrapped server's own", res["instructions"])
	}
}

// TestUpstreamErrorReachesTheClient checks the bridge does not turn a failure
// into a success, which would be the same class of bug it exists to remove.
func TestUpstreamErrorReachesTheClient(t *testing.T) {
	msgs := run(t, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "boom", "params": modernParams(nil),
	})
	for _, m := range msgs {
		if string(m["id"]) == "9" {
			if _, ok := m["error"]; !ok {
				t.Fatalf("expected the upstream error to survive, got %v", m)
			}
			if !strings.Contains(string(m["error"]), "upstream failed") {
				t.Errorf("error = %s, want the upstream message", m["error"])
			}
			return
		}
	}
	t.Fatal("no reply for id 9")
}

// TestBridgeRequestsDoNotCollideWithClientIDs checks the handshake the bridge
// performs on its own behalf is invisible. A client using a small integer id
// must not receive the bridge's internal reply.
func TestBridgeRequestsDoNotCollideWithClientIDs(t *testing.T) {
	msgs := run(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": modernParams(nil),
	})
	seen := 0
	for _, m := range msgs {
		if string(m["id"]) == "1" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("client saw %d replies for id 1, want exactly 1: %v", seen, msgs)
	}
	res := resultOf(t, msgs, "1")
	if !strings.Contains(string(res["tools"]), "ping") {
		t.Errorf("tools = %s", res["tools"])
	}
}

type syncBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// A server whose first initialize fails and whose second succeeds.
func flakyInit(in io.Reader, out io.Writer, attempts *int32) {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		switch m.Method {
		case "initialize":
			if atomic.AddInt32(attempts, 1) == 1 {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
					"error": map[string]any{"code": -32000, "message": "starting up"}})
				continue
			}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"protocolVersion": "2025-11-25",
					"capabilities": map[string]any{"tools": map[string]any{}},
					"serverInfo":   map[string]any{"name": "wrapped"}}})
		case "tools/list":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"tools": []any{}}})
		}
	}
}

// A server that never replies to the first request but answers later ones.
func stalling(in io.Reader, out io.Writer, seen *int32) {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		if m.Method == "initialize" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"protocolVersion": "2025-11-25",
					"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "w"}}})
			continue
		}
		if atomic.AddInt32(seen, 1) == 1 {
			continue // swallow the first real request
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
			"result": map[string]any{"tools": []any{}}})
	}
}

func drive(t *testing.T, server func(io.Reader, io.Writer), reqs []map[string]any, wait time.Duration) string {
	t.Helper()
	c2b, cw := io.Pipe()
	b2s, sr := io.Pipe()
	s2b, sw := io.Pipe()
	out := &syncBuf{}
	go server(b2s, sw)
	b := dualera.New(sr, s2b, out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, c2b) }()
	// Written from a goroutine: if the bridge stops reading, the pipe write
	// blocks, and that blocking is itself one of the things under test.
	go func() {
		enc := json.NewEncoder(cw)
		for _, r := range reqs {
			_ = enc.Encode(r)
			time.Sleep(40 * time.Millisecond)
		}
	}()
	time.Sleep(wait)
	_ = cw.Close()
	return out.String()
}

func modern(id int, method string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method,
		"params": map[string]any{"_meta": map[string]any{
			dualera.MetaProtocolVersion: dualera.ModernVersion}}}
}

// A handshake that fails once must not disable the bridge forever.
func TestHandshakeFailureIsNotPermanent(t *testing.T) {
	var attempts int32
	got := drive(t, func(r io.Reader, w io.Writer) { flakyInit(r, w, &attempts) },
		[]map[string]any{modern(1, "tools/list"), modern(2, "tools/list")}, 600*time.Millisecond)

	if !strings.Contains(got, `"id":2`) {
		t.Fatalf("no reply to the second request at all: %s", got)
	}
	if strings.Count(got, "cannot reach the wrapped server") > 1 {
		t.Errorf("the second request still failed on a cached handshake error:\n%s", got)
	}
}

// One unanswered request must not stall every later one.
func TestOneStalledRequestDoesNotBlockTheRest(t *testing.T) {
	var seen int32
	got := drive(t, func(r io.Reader, w io.Writer) { stalling(r, w, &seen) },
		[]map[string]any{modern(1, "tools/list"), modern(2, "tools/list")}, 800*time.Millisecond)

	if !strings.Contains(got, `"id":2`) {
		t.Fatalf("request 2 never got a reply because request 1 stalled the loop:\n%s", got)
	}
}

// nullResult answers every request with {"result":null}, which is legal
// JSON-RPC for a method with no return value.
func nullResult(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		if m.Method == "initialize" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"protocolVersion": "2025-11-25",
					"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "w"}}})
			continue
		}
		_, _ = out.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":null}` + "\n"))
	}
}

func TestNullResultDoesNotKillTheBridge(t *testing.T) {
	got := drive(t, nullResult,
		[]map[string]any{modern(1, "tools/list"), modern(2, "tools/list")},
		700*time.Millisecond)

	if !strings.Contains(got, `"id":2`) {
		t.Fatalf("the bridge died on a null result, so request 2 never got a reply:\n%s", got)
	}
}

// dyingServer answers initialize then closes its output, standing in for a
// wrapped process that exits or crashes mid-session.
func dyingServer(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		if m.Method == "initialize" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"protocolVersion": "2025-11-25",
					"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "w"}}})
			continue
		}
		if c, ok := out.(io.Closer); ok {
			_ = c.Close()
		}
		return
	}
}

func TestServerDeathFailsInflightRequests(t *testing.T) {
	got := drive(t, dyingServer,
		[]map[string]any{modern(1, "tools/list")}, 900*time.Millisecond)

	if !strings.Contains(got, `"id":1`) {
		t.Fatalf("the request hung forever after the wrapped server died:\n%s", got)
	}
}

// brokenReply answers with neither result nor error, which JSON-RPC 2.0 forbids
// but a confused peer emits. The bridge must not turn that into a success.
func brokenReply(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		if m.Method == "initialize" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
				"result": map[string]any{"protocolVersion": "2025-11-25",
					"capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "w"}}})
			continue
		}
		_, _ = out.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `}` + "\n"))
	}
}

// TestResultlessReplyIsNotASuccess covers the bridge committing the exact fault
// it exists to prevent. A reply with no result and no error was being handed to
// the client as result:{}, an empty success, while the same reply on the legacy
// path reaches the client intact and fails in its decoder. One server, an error
// for a legacy client and a clean empty answer for a modern one, is the
// era-dependent divergence the package doc is about.
func TestResultlessReplyIsNotASuccess(t *testing.T) {
	got := drive(t, brokenReply,
		[]map[string]any{modern(5, "tools/call")}, 700*time.Millisecond)

	if strings.Contains(got, `"result":{}`) {
		t.Fatalf("a reply with neither result nor error became an empty success:\n%s", got)
	}
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("expected an error for a reply carrying neither:\n%s", got)
	}
}

// TestLegalNullResultStillPasses guards the fix above from over-reaching. A
// JSON null is a legal result and four bytes long, so it must still reach the
// client rather than being mistaken for an absent one.
func TestLegalNullResultStillPasses(t *testing.T) {
	got := drive(t, nullResult,
		[]map[string]any{modern(6, "tools/list")}, 700*time.Millisecond)

	if strings.Contains(got, "neither result nor error") {
		t.Fatalf("a legal null result was rejected as absent:\n%s", got)
	}
	if !strings.Contains(got, `"id":6`) {
		t.Fatalf("no reply for id 6:\n%s", got)
	}
}
