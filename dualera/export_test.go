package dualera

// Test-only views of internals. This file is compiled only under `go test`, so
// the bound and the set stay unexported in the package's real API.

const MaxAbandonedForTest = maxAbandoned

func (b *Bridge) AbandonedLenForTest() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.abandoned)
}
