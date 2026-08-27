// Package dualera makes a legacy MCP server answer modern clients too.
//
// Protocol revision 2026-07-28 removed the initialize handshake. A server built
// before it answers initialize and nothing else, and the specification's
// compatibility table says a modern client meeting one fails, possibly by having
// its request run under legacy semantics without either side noticing. The only
// posture that works with every client is dual-era: answer both openings.
//
// Getting there normally means upgrading the SDK, which for the Go ecosystem is
// a v1 beta and an API break from the 0.x line. This bridge is the other route.
// It speaks both eras to the client, holds one legacy session open to the
// server it wraps, and translates between them, so an existing server becomes
// dual-era without being rebuilt.
//
// What it does per request:
//
//   - initialize is passed through, so a legacy client is served exactly as
//     before.
//   - server/discover is answered from the capabilities the wrapped server
//     reported during the bridge's own handshake, advertising both eras.
//   - A request carrying a modern protocolVersion in _meta is forwarded to the
//     established session and its result is returned in the modern shape, with
//     resultType and serverInfo, so the client can tell it was served by a
//     modern-aware peer rather than silently downgraded.
package dualera

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Versions the bridge advertises, newest first.
var SupportedVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18"}

// LegacyVersion is what the bridge negotiates with the server it wraps.
const LegacyVersion = "2025-11-25"

// Modern protocol constants, mirrored so this package stays dependency-free.
const (
	ModernVersion          = "2026-07-28"
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaServerInfo         = "io.modelcontextprotocol/serverInfo"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

// internalIDBase keeps the bridge's own requests out of the client's id space.
// A client that happens to use these ids would otherwise see our replies.
const internalIDBase = 1 << 52

// maxInflight bounds concurrent upstream calls.
const maxInflight = 64

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (m *message) isRequest() bool  { return m.Method != "" && len(m.ID) > 0 }
func (m *message) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// Bridge serves both protocol eras in front of one legacy server.
type Bridge struct {
	toServer   io.Writer
	fromServer *bufio.Scanner
	toClient   io.Writer

	mu       sync.Mutex
	pending  map[string]chan *message
	nextID   int64
	writeMu  sync.Mutex
	clientMu sync.Mutex

	// handshakeMu guards the one legacy session the bridge holds. A failure is
	// deliberately not cached: the wrapped server may simply not have finished
	// starting, and a bridge that disabled itself on the first attempt would
	// stay broken after the server became healthy.
	handshakeMu sync.Mutex
	handshake   *handshakeInfo

	// inflight bounds concurrent upstream calls so a client cannot make the
	// bridge spawn unboundedly many goroutines.
	inflight chan struct{}
}

type handshakeInfo struct {
	Capabilities json.RawMessage
	ServerInfo   json.RawMessage
	Instructions string
	Version      string
}

// New returns a bridge that talks to a server over the given pipes.
func New(toServer io.Writer, fromServer io.Reader, toClient io.Writer) *Bridge {
	sc := bufio.NewScanner(fromServer)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &Bridge{
		toServer:   toServer,
		fromServer: sc,
		toClient:   toClient,
		pending:    make(map[string]chan *message),
		nextID:     internalIDBase,
		inflight:   make(chan struct{}, maxInflight),
	}
}

// Run pumps the server's output and serves the client's input until either side
// closes. It returns the client-side error, if any.
func (b *Bridge) Run(ctx context.Context, fromClient io.Reader) error {
	go b.pumpServer()

	sc := bufio.NewScanner(fromClient)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		b.handle(ctx, &msg, line)
	}
	return sc.Err()
}

// pumpServer routes the server's messages: replies to whoever is waiting,
// everything else straight to the client.
func (b *Bridge) pumpServer() {
	for b.fromServer.Scan() {
		line := b.fromServer.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg message
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg.isResponse() {
			b.mu.Lock()
			ch, ok := b.pending[string(msg.ID)]
			delete(b.pending, string(msg.ID))
			b.mu.Unlock()
			if ok {
				cp := msg
				ch <- &cp
				continue
			}
		}
		b.writeClient(line)
	}
	// The wrapped server's stream has ended, so nothing will ever answer the
	// calls still waiting. Failing them is the difference between a client
	// seeing an error and a client hanging.
	b.failPending("the wrapped server closed its output")
}

// failPending hands every waiting call an error and empties the table.
func (b *Bridge) failPending(reason string) {
	b.mu.Lock()
	waiting := b.pending
	b.pending = make(map[string]chan *message)
	b.mu.Unlock()

	body, err := json.Marshal(map[string]any{"code": -32603, "message": reason})
	if err != nil {
		return
	}
	for _, ch := range waiting {
		select {
		case ch <- &message{Error: body}:
		default:
		}
	}
}

func (b *Bridge) handle(ctx context.Context, msg *message, raw []byte) {
	switch {
	case msg.Method == "server/discover" && msg.isRequest():
		b.async(ctx, msg, b.serveDiscover)
	case msg.isRequest() && isModern(msg.Params):
		b.async(ctx, msg, b.serveModern)
	default:
		// Legacy traffic and notifications pass through untouched.
		b.writeServer(raw)
	}
}

// async serves a request that has to wait on the wrapped server without holding
// up the client loop. JSON-RPC matches replies by id, so answering out of order
// is allowed, and answering in order would let one slow or unanswered request
// starve everything behind it.
func (b *Bridge) async(ctx context.Context, msg *message, serve func(context.Context, *message)) {
	cp := *msg
	select {
	case b.inflight <- struct{}{}:
	default:
		b.replyError(cp.ID, -32603, "too many requests in flight")
		return
	}
	go func() {
		defer func() { <-b.inflight }()
		serve(ctx, &cp)
	}()
}

// serveDiscover answers from the wrapped server's own handshake, so the
// capabilities a modern client sees are the ones that server really reported.
func (b *Bridge) serveDiscover(ctx context.Context, msg *message) {
	info, err := b.ensureHandshake(ctx)
	if err != nil {
		b.replyError(msg.ID, -32603, "cannot reach the wrapped server: "+err.Error())
		return
	}
	result := map[string]any{
		"supportedVersions": SupportedVersions,
		"capabilities":      json.RawMessage(orEmptyObject(info.Capabilities)),
		"resultType":        "complete",
		"_meta":             map[string]any{MetaServerInfo: json.RawMessage(orEmptyObject(info.ServerInfo))},
	}
	if info.Instructions != "" {
		result["instructions"] = info.Instructions
	}
	b.replyResult(msg.ID, result)
}

// serveModern forwards a modern request onto the established legacy session and
// returns the result in the modern shape.
func (b *Bridge) serveModern(ctx context.Context, msg *message) {
	info, err := b.ensureHandshake(ctx)
	if err != nil {
		b.replyError(msg.ID, -32603, "cannot reach the wrapped server: "+err.Error())
		return
	}
	resp, err := b.call(ctx, msg.Method, msg.Params)
	if err != nil {
		b.replyError(msg.ID, -32603, err.Error())
		return
	}
	if len(resp.Error) > 0 {
		b.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "error": json.RawMessage(resp.Error)})
		return
	}
	b.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(msg.ID),
		"result":  json.RawMessage(withModernMarkers(resp.Result, info.ServerInfo)),
	})
}

// withModernMarkers adds resultType and serverInfo to a legacy result without
// disturbing what is already there. A modern client uses exactly these to tell
// a modern peer from a silent downgrade.
func withModernMarkers(result, serverInfo json.RawMessage) []byte {
	var obj map[string]json.RawMessage
	if len(result) == 0 || json.Unmarshal(result, &obj) != nil {
		return orEmptyObject(result)
	}
	// A JSON null decodes into a nil map without erroring, and writing to one
	// panics. A result that is null or not an object carries nowhere to put the
	// markers, so it goes back as it came.
	if obj == nil {
		return orEmptyObject(result)
	}
	if _, ok := obj["resultType"]; !ok {
		obj["resultType"] = json.RawMessage(`"complete"`)
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := obj["_meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	if _, ok := meta[MetaServerInfo]; !ok {
		meta[MetaServerInfo] = json.RawMessage(orEmptyObject(serverInfo))
	}
	if encoded, err := json.Marshal(meta); err == nil {
		obj["_meta"] = encoded
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return orEmptyObject(result)
	}
	return out
}

// ensureHandshake performs the bridge's own legacy initialize once, and caches
// what the server reported.
func (b *Bridge) ensureHandshake(ctx context.Context) (*handshakeInfo, error) {
	b.handshakeMu.Lock()
	defer b.handshakeMu.Unlock()
	if b.handshake != nil {
		return b.handshake, nil
	}

	params, _ := json.Marshal(map[string]any{
		"protocolVersion": LegacyVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "dualera", "version": "0"},
	})
	resp, err := b.call(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	if len(resp.Error) > 0 {
		return nil, fmt.Errorf("initialize failed: %s", resp.Error)
	}
	var r struct {
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      json.RawMessage `json:"serverInfo"`
		Instructions    string          `json:"instructions"`
		ProtocolVersion string          `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		return nil, err
	}
	// The wrapped server expects the initialized notification before it will
	// serve requests.
	b.writeServer([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	b.handshake = &handshakeInfo{
		Capabilities: r.Capabilities, ServerInfo: r.ServerInfo,
		Instructions: r.Instructions, Version: r.ProtocolVersion,
	}
	return b.handshake, nil
}

// call sends a request to the wrapped server under a bridge-owned id and waits
// for its reply.
func (b *Bridge) call(ctx context.Context, method string, params json.RawMessage) (*message, error) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	key := fmt.Sprintf("%d", id)
	ch := make(chan *message, 1)
	b.pending[key] = ch
	b.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if len(params) > 0 {
		req["params"] = json.RawMessage(params)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	b.writeServer(raw)

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, key)
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

func isModern(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	raw, ok := p.Meta[MetaProtocolVersion]
	if !ok {
		return false
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return v >= ModernVersion
}

func orEmptyObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func (b *Bridge) replyResult(id json.RawMessage, result any) {
	b.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (b *Bridge) replyError(id json.RawMessage, code int, msg string) {
	b.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": msg}})
}

func (b *Bridge) write(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	b.writeClient(raw)
}

func (b *Bridge) writeClient(line []byte) {
	b.clientMu.Lock()
	defer b.clientMu.Unlock()
	_, _ = b.toClient.Write(append(append([]byte{}, line...), '\n'))
}

func (b *Bridge) writeServer(line []byte) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, _ = b.toServer.Write(append(append([]byte{}, line...), '\n'))
}
