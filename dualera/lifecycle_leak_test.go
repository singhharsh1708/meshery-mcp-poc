package dualera_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/singhharsh1708/meshery-mcp-poc/dualera"
)

// TestClosingABridgeReleasesItsServerPump checks whether a bridge whose
// client disconnects leaves its server pump running. A host that creates one
// bridge per session would accumulate them.
func TestClosingABridgeReleasesItsServerPump(t *testing.T) {
	settle := func() {
		for i := 0; i < 20; i++ {
			runtime.GC()
			time.Sleep(20 * time.Millisecond)
		}
	}
	settle()
	before := runtime.NumGoroutine()

	const bridges = 25
	for i := 0; i < bridges; i++ {
		serverIn, bridgeToServer := io.Pipe()
		bridgeFromServer, serverOut := io.Pipe()
		clientIn, bridgeToClient := io.Pipe()

		// A server that stays up and answers initialize, like a real one.
		go func() {
			sc := bufio.NewScanner(serverIn)
			enc := json.NewEncoder(serverOut)
			for sc.Scan() {
				var m struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
					continue
				}
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID),
					"result": map[string]any{"ok": true}})
			}
		}()
		go func() { _, _ = io.Copy(io.Discard, clientIn) }()

		b := dualera.New(bridgeToServer, bridgeFromServer, bridgeToClient)
		// The client sends one legacy request and then goes away.
		_ = b.Run(context.Background(), strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"))
		// The client is gone, so release the pump reading the server.
		// The wrapped server is still up and its stream is still open, which is
		// the situation that matters: only Close can release a pump blocked on it.
		_ = b.Close()
		// This test's own plumbing: the fake server reads until the bridge's
		// write end closes, and the drain until the client end does.
		_ = bridgeToServer.Close()
		_ = bridgeToClient.Close()
	}

	settle()
	after := runtime.NumGoroutine()
	t.Logf("goroutines before=%d after=%d for %d bridges", before, after, bridges)
	// Each bridge's own server and client-drain goroutines are the test's, not
	// the bridge's, so allow generous slack and look only for growth on the
	// order of the bridge count.
	if after-before >= bridges {
		t.Errorf("goroutines grew by %d across %d bridges: the server pump outlives the client", after-before, bridges)
	}
}
