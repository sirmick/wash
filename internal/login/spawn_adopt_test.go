package login

import (
	"errors"
	"testing"
)

// The auto-attach path lists sessions BEFORE taking the spawn lock, so
// by the time it holds the lock the answer may have changed. These
// tests pin the two halves of that: the auto path re-asks and adopts,
// and the explicit "new session" path does not.
//
// Neither test can fork: RouterBinary is empty, so any attempt to
// actually spawn fails. That is the assertion — adopting is the only
// way to come back without an error.

func TestSpawnIfNoneAdoptsSessionWonByAnotherRequest(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{
		1000: {{Pid: 1111, UID: 1000, SessID: "s-winner", Sock: "/run/wash/1000/sessions/s-winner.sock"}},
	}}
	sp := &Spawner{Sessions: reg, RunRoot: t.TempDir()}

	got, adopted, err := sp.SpawnIfNone(Identity{UID: 1000, Name: "alice"}, "alice")
	if err != nil {
		t.Fatalf("SpawnIfNone: unexpected error %v", err)
	}
	if !adopted {
		t.Errorf("adopted = false, want true")
	}
	if got.SessID != "s-winner" {
		t.Errorf("SessID = %q, want the session already there (%q)", got.SessID, "s-winner")
	}
}

func TestSpawnIfNoneForksWhenTheUserTrulyHasNone(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{}}
	sp := &Spawner{Sessions: reg, RunRoot: t.TempDir()}

	_, adopted, err := sp.SpawnIfNone(Identity{UID: 1000, Name: "alice"}, "alice")
	if adopted {
		t.Errorf("adopted = true with no sessions to adopt")
	}
	// It got as far as trying to exec, which is the point: nothing
	// short-circuited it.
	if err == nil {
		t.Errorf("expected the fork to fail with no RouterBinary, got nil")
	}
	if errors.Is(err, ErrSessionCap) {
		t.Errorf("unexpected cap error with the cap disabled: %v", err)
	}
}

// The picker's "new session" button means it: an existing session is
// not a reason to hand back that one instead.
func TestSpawnStillForksWhenSessionsExist(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{
		1000: {{Pid: 1111, UID: 1000, SessID: "s-a"}},
	}}
	sp := &Spawner{Sessions: reg, RunRoot: t.TempDir()}

	if _, err := sp.Spawn(Identity{UID: 1000, Name: "alice"}, "second"); err == nil {
		t.Errorf("Spawn adopted an existing session; it must always fork")
	}
}

// The cap is re-checked under the lock too, so a session that appeared
// while we queued still counts against it.
func TestSpawnCapRecheckedUnderTheLock(t *testing.T) {
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{
		1000: {{Pid: 1111, UID: 1000, SessID: "s-a"}},
	}}
	sp := &Spawner{MaxPerUID: 1, Sessions: reg, RunRoot: t.TempDir()}

	if _, _, err := sp.SpawnIfNone(Identity{UID: 1000, Name: "alice"}, "alice"); err != nil {
		t.Fatalf("at the cap but adoptable: want the existing session, got %v", err)
	}
}
