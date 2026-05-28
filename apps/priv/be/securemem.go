package priv

import "golang.org/x/sys/unix"

// secureLock pins the pages containing b in RAM so the cleartext
// can't be paged to disk. mlock(2) rounds the address down and the
// length up to whole pages, so the actual locked region is at most
// b's length + two pages (typically 4–16 KiB). This is the
// gpg-agent-style approach: lock the buffer that holds the secret,
// not the whole address space (which is what mlockall(2) would do,
// and which OOMs Go programs in tiny VMs).
//
// Errors are best-effort: if RLIMIT_MEMLOCK is exhausted (or some
// LSM denies the syscall) we log and continue rather than refusing
// to run — the priv app is more valuable available than locked.
func secureLock(b []byte, logf func(format string, args ...any)) {
	if len(b) == 0 {
		return
	}
	if err := unix.Mlock(b); err != nil {
		logf("securemem: mlock %d bytes failed: %v (continuing — password may swap)", len(b), err)
	}
}

// secureUnlock releases the lock acquired by secureLock. Call BEFORE
// zeroing the buffer so the kernel page reclaim path sees consistent
// state. Errors are logged but otherwise ignored.
func secureUnlock(b []byte, logf func(format string, args ...any)) {
	if len(b) == 0 {
		return
	}
	if err := unix.Munlock(b); err != nil {
		logf("securemem: munlock %d bytes failed: %v", len(b), err)
	}
}
