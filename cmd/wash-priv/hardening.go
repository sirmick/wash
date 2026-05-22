package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// applyHardening applies the OS-level process protections that matter
// for a process that holds a cleartext sudo password in memory:
//
//   - PR_SET_DUMPABLE 0: disables core dumps AND blocks ptrace from
//     non-root processes (no /proc/<pid>/mem reads, no gdb attach).
//     This is the single biggest mitigation; everything else is
//     defense in depth.
//   - RLIMIT_CORE = 0: belt + suspenders against core dumps in case
//     PR_SET_DUMPABLE gets re-enabled by something downstream (e.g.
//     a future setuid spawn the kernel auto-re-enables — we never
//     execve here, but be explicit).
//   - mlockall(MCL_CURRENT|MCL_FUTURE): prevents memory pages
//     containing the password from being swapped to disk. Often
//     fails on embedded targets where RLIMIT_MEMLOCK is tiny —
//     gracefully degrade with a log instead of refusing to start.
//
// All errors go through logf (the caller's logger); we never panic
// from here. The wash-priv process is more valuable running than
// running with maximum hardening — without it, the user can't sudo
// at all.
func applyHardening(logf func(format string, args ...any)) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		logf("hardening: PR_SET_DUMPABLE failed: %v (continuing)", err)
	}
	var none syscall.Rlimit
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &none); err != nil {
		logf("hardening: RLIMIT_CORE failed: %v (continuing)", err)
	}
	if err := unix.Mlockall(unix.MCL_CURRENT | unix.MCL_FUTURE); err != nil {
		logf("hardening: mlockall failed: %v (continuing — password may swap)", err)
	}
}
