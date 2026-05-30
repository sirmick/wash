package session

import "testing"

// TestServiceFEKind pins the service-id → FE-kind mapping the sidebar gateway
// relies on (docs/NET.md §2.11, B1d). A typo here silently drops a service's
// status pushes (serviceFEKind returns "" → the state forwarder ignores them),
// so the mapping is worth a guard even though each case is one line.
func TestServiceFEKind(t *testing.T) {
	cases := map[string]string{
		NotifyAppID:        "notify.state",
		BulkAppID:          "bulk.state",
		PrivAppID:          "priv.state",
		NetdAppID:          "net.state",
		"com.wash.unknown": "", // non-gatewayed app — we don't speak for it
	}
	for appID, want := range cases {
		if got := serviceFEKind(appID); got != want {
			t.Errorf("serviceFEKind(%q) = %q, want %q", appID, got, want)
		}
	}
}
