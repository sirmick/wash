package remote

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"
	"github.com/sirmick/wash/internal/washmount"
)

// mountRec records published mount state.
type mountRec struct {
	mu sync.Mutex
	st State
}

func (r *mountRec) Mutate(fn func(*State)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(&r.st)
}

func (r *mountRec) row(mp string) (MountState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.st.Mounts {
		if m.MountPoint == mp {
			return m, true
		}
	}
	return MountState{}, false
}

func newTestMountManager(t *testing.T) (*mountManager, *mountRec) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rec := &mountRec{}
	m := newMountManager(nil, nil)
	m.svc = rec
	m.baseDir = t.TempDir()
	return m, rec
}

// TestFailedMountRowCanBeDismissed: a mount that failed publishes an error
// row; "unmount" on that row clears it (and forgets the persisted entry),
// instead of returning early because there is no live entry to detach.
func TestFailedMountRowCanBeDismissed(t *testing.T) {
	m, rec := newTestMountManager(t)
	addSavedMount("-bad", "/srv") // persisted, as if ticked on an earlier launch
	err := m.mount("-bad", "/srv", true)
	if !errors.Is(err, errInvalidMountHost) {
		t.Fatalf("mount = %v; want errInvalidMountHost", err)
	}
	mp := m.mountPointFor("-bad", "/srv")
	row, ok := rec.row(mp)
	if !ok || row.Status != MountDown || !strings.Contains(row.Error, "must not begin with") {
		t.Fatalf("row = %+v (found=%v); want an error row", row, ok)
	}
	m.unmount(mp, false)
	if _, ok := rec.row(mp); ok {
		t.Fatal("error row survived unmount")
	}
	if isSavedMount("-bad", "/srv") {
		t.Fatal("persisted entry survived dismissal")
	}
}

// TestRestoreRetriesUntilForgotten: a persisted mount that keeps failing is
// retried with growing backoff, and the loop stops once the user forgets it.
func TestRestoreRetriesUntilForgotten(t *testing.T) {
	m, _ := newTestMountManager(t)
	orig := mountWithDialer
	var mu sync.Mutex
	attempts := 0
	mountWithDialer = func(func() (*sftp.Client, error), washmount.Options) (*fuse.Server, error) {
		mu.Lock()
		attempts++
		mu.Unlock()
		return nil, errors.New("host unreachable")
	}
	t.Cleanup(func() { mountWithDialer = orig })

	// The fake clock hands each wait to the test and fires only when the
	// test releases it, so "forget the mount" lands deterministically before
	// the loop re-checks whether it is still saved.
	waits := make(chan time.Duration)
	release := make(chan struct{})
	m.after = func(d time.Duration) <-chan time.Time {
		waits <- d
		<-release
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	addSavedMount("box", "/srv")
	done := make(chan struct{})
	go func() {
		m.restoreWithRetry(savedMount{Host: "box", RemoteRoot: "/srv"})
		close(done)
	}()
	var seen []time.Duration
	for len(seen) < 3 {
		select {
		case d := <-waits:
			seen = append(seen, d)
			if len(seen) == 3 {
				removeSavedMount("box", "/srv") // the user dismissed it
			}
			release <- struct{}{}
		case <-time.After(5 * time.Second):
			t.Fatalf("retry loop stalled after %v", seen)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop did not stop after the mount was forgotten")
	}
	want := []time.Duration{restoreRetryMin, 2 * restoreRetryMin, 4 * restoreRetryMin}
	for i, d := range want {
		if seen[i] != d {
			t.Errorf("wait %d = %s; want %s", i, seen[i], d)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Errorf("mount attempts = %d; want 3", attempts)
	}
}

// TestUnmountBusyKeepsMount: a graceful unmount refused as busy leaves the
// mount up and reports the holders; a forced unmount then detaches it.
func TestUnmountBusyKeepsMount(t *testing.T) {
	m, rec := newTestMountManager(t)
	origG, origH := unmountGraceful, describeHolders
	t.Cleanup(func() { unmountGraceful, describeHolders = origG, origH })
	gracefulCalls := 0
	unmountGraceful = func(*fuse.Server) error {
		gracefulCalls++
		return errors.New("fusermount3: failed to unmount: Device or resource busy")
	}
	describeHolders = func(string) string { return "bash (pid 4120, cwd)" }

	mp := m.mountPointFor("box", "/srv")
	// A live entry needs a non-nil server for the graceful path; the seam
	// never dereferences it.
	m.entries[mp] = &mountEntry{server: &fuse.Server{}, host: "box", remoteRoot: "/srv"}
	m.setMount(MountState{Host: "box", RemoteRoot: "/srv", MountPoint: mp, Status: MountUp})

	m.unmount(mp, false)
	row, ok := rec.row(mp)
	if !ok || row.Status != MountUp || !strings.Contains(row.Busy, "bash (pid 4120") {
		t.Fatalf("after busy unmount row = %+v (found=%v); want mounted + busy", row, ok)
	}
	if _, live := m.entries[mp]; !live {
		t.Fatal("busy unmount must keep the entry")
	}
	if gracefulCalls != 1 {
		t.Fatalf("graceful calls = %d; want 1", gracefulCalls)
	}

	// Force skips the graceful rung and runs the escalation (which, with a
	// nil-ish server and no real mount, just logs) and drops the row.
	m.unmount(mp, true)
	if _, ok := rec.row(mp); ok {
		t.Fatal("forced unmount left the row")
	}
	if _, live := m.entries[mp]; live {
		t.Fatal("forced unmount left the entry")
	}
	if gracefulCalls != 1 {
		t.Fatalf("force must not call the graceful seam again; calls = %d", gracefulCalls)
	}
}

// TestMountPointCollisionGetsSuffix: two different roots sharing a basename on
// one host must not silently collapse into one mount.
func TestMountPointCollisionGetsSuffix(t *testing.T) {
	m, _ := newTestMountManager(t)
	mp1, ph1, ok := m.reserve("box", "/home/me/project")
	if !ok || ph1 == nil {
		t.Fatal("first reserve failed")
	}
	mp2, ph2, ok := m.reserve("box", "/srv/project")
	if !ok || ph2 == nil {
		t.Fatal("second reserve (different root) must succeed")
	}
	if mp2 == mp1 || !strings.HasPrefix(mp2, mp1+"-") {
		t.Fatalf("second mountpoint %q should be %q plus a suffix", mp2, mp1)
	}
	if _, _, ok := m.reserve("box", "/home/me/project"); ok {
		t.Fatal("re-reserving a mounted target must report already mounted")
	}
	if _, _, ok := m.reserve("box", "/srv/project"); ok {
		t.Fatal("re-reserving the suffixed target must report already mounted")
	}
}
