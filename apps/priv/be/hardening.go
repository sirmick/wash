package priv

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// applyHardening applies the OS-level protections that matter for a
// process that holds a cleartext sudo password in memory:
//
//   - PR_SET_DUMPABLE 0: disables core dumps AND blocks ptrace +
//     /proc/<pid>/mem reads from non-root processes.
//   - RLIMIT_CORE = 0: belt-and-braces against core dumps in case
//     PR_SET_DUMPABLE gets re-enabled downstream.
//
// We deliberately do NOT call mlockall(2) here. mlockall locks the
// entire process address space, which on Go runtimes (~1 GiB of
// reserved VM) OOMs small VMs and never actually helps unless the
// host has swap. Page-level locking of the password buffer itself
// is done via securemem.secureLock at the point of assignment —
// the gpg-agent pattern.
//
// All errors are best-effort: log and continue. The priv process is
// more valuable running than running with maximum hardening — without
// it, the user can't sudo at all.
func applyHardening(logf func(format string, args ...any)) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		logf("hardening: PR_SET_DUMPABLE failed: %v (continuing)", err)
	}
	var none syscall.Rlimit
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &none); err != nil {
		logf("hardening: RLIMIT_CORE failed: %v (continuing)", err)
	}
}
