package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// writeScript creates a 0755 /bin/sh stub at dir/name with the given
// body. Unlike writeExec (registry_test.go) these ARE executed: the
// probe path needs a real process with a real exit status.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

func TestProbe_SilentNotAnAppExitDeclines(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "wash-helper", fmt.Sprintf("exit %d", wire.ExitNotAnApp))

	out, err := Probe(context.Background(), bin)
	if !errors.Is(err, ErrNotAnApp) {
		t.Fatalf("err = %v, want ErrNotAnApp", err)
	}
	if len(out) != 0 {
		t.Fatalf("stdout = %q, want empty", out)
	}
}

// A helper declines before it can print anything, so output alongside
// the exit code means something else went wrong — and a real failure
// must stay visible rather than being swallowed as a decline.
func TestProbe_NotAnAppExitWithOutputIsNotADecline(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"stderr", fmt.Sprintf("echo boom >&2; exit %d", wire.ExitNotAnApp)},
		{"stdout", fmt.Sprintf("echo boom; exit %d", wire.ExitNotAnApp)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := writeScript(t, dir, "wash-helper", tc.body)

			if _, err := Probe(context.Background(), bin); err == nil {
				t.Fatal("err = nil, want a probe failure")
			} else if errors.Is(err, ErrNotAnApp) {
				t.Fatalf("err = %v, want a plain failure not a decline", err)
			}
		})
	}
}

// Exit 2 is what Go's flag package produces for an unknown flag. It must
// NOT be read as a decline, or an app that fails to parse its own
// arguments would vanish from the catalog instead of being listed with
// a reason.
func TestProbe_FlagErrorExitIsNotADecline(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "wash-helper", "exit 2")

	if _, err := Probe(context.Background(), bin); err == nil {
		t.Fatal("err = nil, want a probe failure")
	} else if errors.Is(err, ErrNotAnApp) {
		t.Fatalf("err = %v, want a plain failure not a decline", err)
	}
}

func TestScan_DeclinedBinaryIsSkippedEntirely(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "wash-helper", fmt.Sprintf("exit %d", wire.ExitNotAnApp))

	r := NewRegistry()
	if err := r.Scan(context.Background(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	// Not listed-disabled: no catalog row, no boot log line.
	if got := r.Entries(); len(got) != 0 {
		t.Fatalf("Entries() = %d entries, want 0 (declined binaries are dropped)", len(got))
	}
}

func TestScan_FailedProbeIsStillListedDisabled(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "wash-broken", "echo 'usage: ...' >&2; exit 1")

	r := NewRegistry()
	if err := r.Scan(context.Background(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	got := r.Entries()
	if len(got) != 1 {
		t.Fatalf("Entries() = %d entries, want 1", len(got))
	}
	if got[0].Enabled() {
		t.Fatal("entry is enabled, want disabled")
	}
	if got[0].Reason == "" {
		t.Fatal("Reason is empty, want the failure surfaced")
	}
}
