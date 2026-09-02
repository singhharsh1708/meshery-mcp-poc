package dualera_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/singhharsh1708/meshery-mcp-poc/dualera"
)

// TestRequestAfterServerDeathIsAnswered checks what a client gets when it sends
// a request that needs the wrapped server after that server has already gone.
// A hang here is worse than an error: the client cannot tell it apart from a
// slow call and has nothing to retry against.
func TestRequestAfterServerDeathIsAnswered(t *testing.T) {
	serverIn, bridgeToServer := io.Pipe()
	bridgeFromServer, serverOut := io.Pipe()
	clientIn, bridgeToClient := io.Pipe()

	// The server reads nothing and dies immediately.
	go func() {
		_, _ = io.Copy(io.Discard, serverIn)
	}()
	_ = serverOut.Close()

	b := dualera.New(bridgeToServer, bridgeFromServer, bridgeToClient)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Give the bridge a moment to notice the closed server stream, then send a
	// modern request, which is the path that has to consult the server.
	clientR, clientW := io.Pipe()
	go func() { _ = b.Run(ctx, clientR) }()
	time.Sleep(200 * time.Millisecond)
	go func() {
		_, _ = clientW.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n"))
	}()

	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(clientIn)
		for sc.Scan() {
			var m struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &m) == nil && string(m.ID) == "7" {
				got <- sc.Text()
				return
			}
		}
	}()

	select {
	case line := <-got:
		if !strings.Contains(line, "error") {
			t.Errorf("expected an error reply, got %s", line)
		} else {
			t.Logf("answered: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Error("the client got no reply at all within 3s: a request needing a dead server hangs instead of erroring")
	}
}

// TestDeadServerDoesNotExhaustTheInflightSlots is the consequence of the hang
// above. Each waiting request holds one of the sixty-four slots, so a client
// that keeps asking after the server has gone would fill them all and then be
// refused for capacity, which reports a busy bridge rather than a dead server.
func TestDeadServerDoesNotExhaustTheInflightSlots(t *testing.T) {
	serverIn, bridgeToServer := io.Pipe()
	bridgeFromServer, serverOut := io.Pipe()
	clientIn, bridgeToClient := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, serverIn) }()
	_ = serverOut.Close()

	b := dualera.New(bridgeToServer, bridgeFromServer, bridgeToClient)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clientR, clientW := io.Pipe()
	go func() { _ = b.Run(ctx, clientR) }()
	time.Sleep(200 * time.Millisecond)

	const n = 80 // more than maxInflight
	go func() {
		for i := 0; i < n; i++ {
			_, _ = clientW.Write([]byte(`{"jsonrpc":"2.0","id":` + strconv.Itoa(i) +
				`,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n"))
		}
	}()

	seen := 0
	deadline := time.After(10 * time.Second)
	sc := bufio.NewScanner(clientIn)
	lines := make(chan string, n+8)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	for seen < n {
		select {
		case <-lines:
			seen++
		case <-deadline:
			t.Fatalf("only %d of %d requests were answered; the rest are holding slots forever", seen, n)
		}
	}

	// Refusing part of a burst is correct: maxInflight is a deliberate bound.
	// What must not happen is the slots staying taken. After the burst drains,
	// a further request has to reach the dead-server answer rather than being
	// turned away for capacity.
	time.Sleep(300 * time.Millisecond)
	go func() {
		_, _ = clientW.Write([]byte(`{"jsonrpc":"2.0","id":9001,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n"))
	}()
	select {
	case line := <-lines:
		if strings.Contains(line, "too many requests in flight") {
			t.Errorf("the inflight slots were never released: %s", line)
		}
		if !strings.Contains(line, "wrapped server") {
			t.Errorf("expected the dead-server error, got %s", line)
		}
	case <-time.After(5 * time.Second):
		t.Error("the request after the burst was never answered")
	}
}

// TestAbandonedSetIsBounded covers the slow leak. An abandoned id is only
// forgotten when the late reply it watches for arrives, so a wrapped server
// that never answers leaves one entry per abandoned call for the life of the
// process. The set is capped and the oldest entry gives way.
func TestAbandonedSetIsBounded(t *testing.T) {
	serverIn, bridgeToServer := io.Pipe()
	bridgeFromServer, serverOut := io.Pipe()
	clientIn, bridgeToClient := io.Pipe()

	// A server that reads everything and answers nothing, so every call the
	// bridge makes on its behalf is eventually abandoned.
	go func() { _, _ = io.Copy(io.Discard, serverIn) }()
	go func() { _, _ = io.Copy(io.Discard, clientIn) }()
	defer func() {
		_ = bridgeToServer.Close()
		_ = serverOut.Close()
		_ = bridgeToClient.Close()
	}()

	b := dualera.New(bridgeToServer, bridgeFromServer, bridgeToClient)
	// A context that is already done, so each call abandons as soon as it has
	// registered rather than waiting on a server that will never answer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	clientR, clientW := io.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx, clientR) }()

	// Well past the cap, because a burst also loses some calls to the inflight
	// bound and those never reach the abandonment path at all.
	const calls = dualera.MaxAbandonedForTest * 4
	for i := 0; i < calls; i++ {
		if _, err := clientW.Write([]byte(`{"jsonrpc":"2.0","id":` + strconv.Itoa(i) +
			`,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n")); err != nil {
			break
		}
	}
	_ = clientW.Close()
	<-done

	// Let the in-flight handlers finish abandoning.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := b.AbandonedLenForTest(); n > dualera.MaxAbandonedForTest {
			t.Fatalf("abandoned set grew to %d, past the %d cap", n, dualera.MaxAbandonedForTest)
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := b.AbandonedLenForTest()
	t.Logf("abandoned set settled at %d after %d calls", n, calls)
	if n <= dualera.MaxAbandonedForTest/2 {
		t.Fatalf("only %d calls were abandoned, too few to exercise the cap of %d", n, dualera.MaxAbandonedForTest)
	}
	if n > dualera.MaxAbandonedForTest {
		t.Errorf("abandoned set = %d, want at most %d", n, dualera.MaxAbandonedForTest)
	}
}
