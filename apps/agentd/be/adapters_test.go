package agentd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pgroup lists the pids in a process group, from /proc — the fifth field
// of /proc/<pid>/stat.
func pgroup(t *testing.T, pgid int) []int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc:", err)
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// comm may contain spaces; it is parenthesised, so split after it.
		rest := string(b)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+1:]
		}
		f := strings.Fields(rest)
		// After the comm: state, ppid, pgrp, ...
		if len(f) >= 3 && f[2] == strconv.Itoa(pgid) {
			out = append(out, pid)
		}
	}
	return out
}

// An adapter is usually `npx → node → agent`. Killing the pid alone reaps
// the wrapper and orphans the rest, still holding its half of the wire —
// so the adapter is launched in its own process group and stop() kills
// the group. This is the same shape (Setpgid + kill -pgid) as dialAdapter
// uses, on a process tree we can build without an adapter.
func TestKillGroupReapsTheWholeTree(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 300 & sleep 300 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start sh:", err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	// The grandchildren exist and share the group.
	deadline := time.Now().Add(3 * time.Second)
	for len(pgroup(t, pgid)) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(pgroup(t, pgid)); n < 3 {
		t.Fatalf("expected sh + 2 sleeps in group %d, found %d", pgid, n)
	}

	killGroup(cmd.Process)
	_ = cmd.Wait()

	// Nothing survives — including the sleeps the shell forked, which a
	// plain Process.Kill would have left running.
	deadline = time.Now().Add(3 * time.Second)
	for len(pgroup(t, pgid)) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if left := pgroup(t, pgid); len(left) != 0 {
		t.Fatalf("processes survived the group kill: %v", left)
	}
}
