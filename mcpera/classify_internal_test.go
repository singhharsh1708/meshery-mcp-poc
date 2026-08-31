package mcpera

import "testing"

// TestClassifyReadsBothModernSignalsIndependently is the disagreeing fixture for
// the era verdict.
//
// A server counts as modern either because it answered server/discover or
// because it served a modern call and returned a modern-shaped result. Both are
// true of a normal dual-era server, so fixtures built from real servers make the
// two agree, and either disjunct could be deleted with the suite still green.
// The revision makes server/discover optional, so the disagreeing cases are not
// hypothetical: a server may implement one and not the other.
func TestClassifyReadsBothModernSignalsIndependently(t *testing.T) {
	for _, tc := range []struct {
		name              string
		answersDiscover   bool
		servesModernCall  bool
		modernResult      bool
		answersInitialize bool
		want              Era
	}{
		// Only the discover half is true. server/discover is optional, so a
		// server may answer it without serving the call this probe made.
		{"discover alone", true, false, false, false, Modern},
		// Only the served-call half is true, which is what a server that
		// declined to implement the optional probe looks like.
		{"served call alone", false, true, true, false, Modern},
		// The served call was answered but in the legacy shape, which is not a
		// modern signal on its own.
		{"served call, legacy shape", false, true, false, false, Unknown},
		// Each modern signal alone, alongside the legacy opening, is dual.
		{"discover alone plus initialize", true, false, false, true, Dual},
		{"served call alone plus initialize", false, true, true, true, Dual},
		{"initialize only", false, false, false, true, Legacy},
		{"nothing", false, false, false, false, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Report{
				AnswersDiscover:      tc.answersDiscover,
				ServesModernCall:     tc.servesModernCall,
				ModernResultIsModern: tc.modernResult,
				AnswersInitialize:    tc.answersInitialize,
			}
			r.classify()
			if r.Era != tc.want {
				t.Errorf("Era = %v, want %v (discover=%v servedCall=%v modernShape=%v initialize=%v)",
					r.Era, tc.want, tc.answersDiscover, tc.servesModernCall, tc.modernResult, tc.answersInitialize)
			}
		})
	}
}

// TestSilentDowngradeNeedsBothHalves pins the other compound verdict. It fires
// only when a modern call was served and the result came back in the legacy
// shape, so neither half alone is enough.
func TestSilentDowngradeNeedsBothHalves(t *testing.T) {
	for _, tc := range []struct {
		name         string
		served       bool
		modernResult bool
		want         bool
	}{
		{"served with a legacy-shaped result", true, false, true},
		{"served with a modern result", true, true, false},
		{"never served the call", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Report{ServesModernCall: tc.served, ModernResultIsModern: tc.modernResult}
			r.classify()
			if r.SilentDowngrade != tc.want {
				t.Errorf("SilentDowngrade = %v, want %v", r.SilentDowngrade, tc.want)
			}
		})
	}
}

// TestDiscoverNeedsAResultThatDescribesSomething covers the gap between
// answering a method and implementing it. A server that returns an empty
// success to server/discover has told a client nothing about itself, and
// counting that as an answer puts it in the dual-era row, the one a client
// would trust most.
func TestDiscoverNeedsAResultThatDescribesSomething(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result string
		want   bool
	}{
		{"a real discover answer", `{"serverInfo":{"name":"s"},"capabilities":{}}`, true},
		{"one field is enough", `{"protocolVersions":["2026-07-28"]}`, true},
		{"empty object", `{}`, false},
		{"null", `null`, false},
		{"absent", ``, false},
		{"not an object", `"ok"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describesAServer([]byte(tc.result)); got != tc.want {
				t.Errorf("describesAServer(%s) = %v, want %v", tc.result, got, tc.want)
			}
		})
	}
}
