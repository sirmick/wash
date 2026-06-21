package remote

import "testing"

// TestSavedMountsRoundTrip exercises the "reconnect at launch" persistence:
// add is idempotent, load returns what was saved, and remove forgets one.
func TestSavedMountsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if got := loadSavedMounts(); len(got) != 0 {
		t.Fatalf("fresh config: got %d saved mounts, want 0", len(got))
	}

	addSavedMount("wash@a", "/home/u")
	addSavedMount("wash@a", "/home/u") // idempotent
	addSavedMount("wash@b", "/srv")

	got := loadSavedMounts()
	if len(got) != 2 {
		t.Fatalf("after 2 distinct adds (one dup): got %d, want 2: %+v", len(got), got)
	}

	removeSavedMount("wash@a", "/home/u")
	got = loadSavedMounts()
	if len(got) != 1 || got[0].Host != "wash@b" || got[0].RemoteRoot != "/srv" {
		t.Fatalf("after remove: got %+v; want [{wash@b /srv}]", got)
	}

	// Removing a non-present mount is a no-op.
	removeSavedMount("wash@nope", "/x")
	if len(loadSavedMounts()) != 1 {
		t.Fatalf("remove-missing changed the set: %+v", loadSavedMounts())
	}
}
