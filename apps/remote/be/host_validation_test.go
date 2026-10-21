package remote

import (
	"context"
	"strings"
	"testing"
)

// TestConnectRefusesOptionLikeHost: a host that ssh would parse as an option
// (typed, or announced by a hostile mDNS responder) must never start a run;
// it is published as down with the reason, so the UI shows why.
func TestConnectRefusesOptionLikeHost(t *testing.T) {
	rec := &recMutator{}
	s := &supervisor{svc: rec, sockDir: t.TempDir(), procs: map[string]*hostProc{}}
	s.attempt = func(context.Context, attemptSpec) (string, string) {
		t.Error("attempt must not run for an invalid host")
		return "", ""
	}
	const bad = "-oProxyCommand=x"
	s.connect(bad, "", 0)
	if len(s.procs) != 0 {
		t.Fatalf("a run was started for an invalid host: %v", s.procs)
	}
	hs, ok := rec.last(bad)
	if !ok || hs.Status != StatusDown || !strings.Contains(hs.Error, "must not begin with") {
		t.Fatalf("published state = %+v (found=%v); want down with a validation error", hs, ok)
	}
}
