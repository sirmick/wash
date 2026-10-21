package washmount

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHoldersFindsCwdAndFd: a child sitting in the directory (cwd) and one
// holding a file open (fd) are both reported, with their comm and how.
func TestHoldersFindsCwdAndFd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := exec.Command("sleep", "30")
	cwd.Dir = dir
	if err := cwd.Start(); err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	defer func() { _ = cwd.Process.Kill(); _ = cwd.Wait() }()
	fd := exec.Command("sleep", "30")
	fh, err := os.Open(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	fd.ExtraFiles = []*os.File{fh}
	if err := fd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Process.Kill(); _ = fd.Wait() }()

	// /proc entries appear as soon as the child execs; allow a moment.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := map[int]Holder{}
		for _, h := range Holders(dir) {
			got[h.PID] = h
		}
		c, cok := got[cwd.Process.Pid]
		f, fok := got[fd.Process.Pid]
		if cok && fok {
			if c.Via != "cwd" || f.Via != "fd" {
				t.Fatalf("via = %q / %q; want cwd / fd", c.Via, f.Via)
			}
			if c.Comm != "sleep" {
				t.Fatalf("comm = %q; want sleep", c.Comm)
			}
			if _, self := got[os.Getpid()]; self {
				t.Fatal("the walker must exclude its own process")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("holders = %v; want pids %d (cwd) and %d (fd)", got, cwd.Process.Pid, fd.Process.Pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestIsBusy(t *testing.T) {
	for _, e := range []error{
		errors.New("fusermount3: failed to unmount /x: Device or resource busy (code exit status 1)"),
		syscall.EBUSY,
	} {
		if !IsBusy(e) {
			t.Errorf("IsBusy(%v) = false", e)
		}
	}
	for _, e := range []error{nil, errors.New("entry for /x not found in /etc/mtab"), syscall.ENOENT} {
		if IsBusy(e) {
			t.Errorf("IsBusy(%v) = true", e)
		}
	}
}
