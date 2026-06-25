package vscode

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestKillChildReapsProcessGroup is the regression test for the code-server
// orphan leak: code-server forks its own node worker children, and on a
// SIGTERM shutdown Pdeathsig only reaps the *direct* child — the workers
// orphaned to PID 1 and busy-looped at ~100% CPU. killChild must group-kill
// the whole tree.
//
// `sh -c 'sleep 60 & sleep 60'` stands in for code-server: a parent that
// backgrounds a worker in the *same* process group (sh -c has no job
// control, so the backgrounded job stays in our group).
func TestKillChildReapsProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { _ = cmd.Wait() }() // reap the direct child's zombie

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}

	m := &manager{proc: cmd}
	m.killChild()

	// Kill(-pgid, 0) probes the group: ESRCH means no member survives, i.e.
	// the backgrounded worker was reaped too — not just the direct child.
	// SIGKILL delivery + exit is async, so poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			break // whole group gone — the fix holds
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after killChild — workers leaked", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// killChild must also drop our handle so a later stop()/watch() is a no-op.
	m.mu.Lock()
	got := m.proc
	m.mu.Unlock()
	if got != nil {
		t.Fatalf("m.proc not cleared after killChild")
	}
}

// TestKillChildNilProcIsSafe: shutdown before code-server ever started must
// not panic (the common case — the service is up but nobody opened a
// workbench yet).
func TestKillChildNilProcIsSafe(t *testing.T) {
	m := &manager{}
	m.killChild() // must not panic
}
