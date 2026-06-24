# TODO — SSH/SFTP FUSE mount correctness bugs

Confirmed by an adversarial audit (find → verify → synthesize, 2026-06-23) of the
SFTP FUSE mount stack: `internal/washmount`, `internal/remotewatch`,
`internal/fswatch`, `apps/remote/be/mounts.go`. 21 bugs survived adversarial
verification (5 reported findings were refuted and dropped).

**Root cause of the serious cluster:** `internal/washmount/root.go` `run()`
**abandons the in-flight goroutine on `opTimeout`** (because `pkg/sftp` isn't
cancellable). That one decision produces the critical buffer corruption, the
markDead-skip, the sem over-admission, and the fd wedge. The structural fix is to
stop abandoning work; for a future cancellable backend (S3 SDK), pass the context
*into* the backend call instead.

The lock ordering in `markDead`/`closeCurrent` is clean — the 8252016 fswatch
deadlock class does **not** recur here.

Suggested order: land the **critical + 2 high + shared mediums** first (they live
in code any future mount backend reuses), then the lows.

## Status (branch wash-connect-ux)

**FIXED:** Critical (private read buffer) · both High (markDead-on-timeout +
mount-ssh keepalive; remotewatch reconnect throttle) · all 5 Mediums — sem
release moved into the worker; Hub drops a stale sub on watch-close; Rename
honors NOREPLACE / rejects EXCHANGE; Read treats io.EOF as success. The
**fd-wedge** Medium is resolved transitively: markDead-on-timeout now closes
the client so the abandoned goroutine completes and releases `h.mu` promptly
— the dedicated single-flight worker the audit suggests is no longer needed
for correctness.

**REMAINING:** the 7 **Low** items (unmount-abort path safety, go-fuse loop
leak on wedged lazy-detach, double-Close clientSub, mount() TOCTOU, Create
rollback, Rename posix-rename fallback, Fsync durability).

---

## Critical

### Abandoned Read goroutine writes into a recycled FUSE buffer — cross-request memory corruption
- **Where:** `internal/washmount/node.go:126-142` (with `root.go:121-133`)
- **Bug:** On a read `opTimeout`, `run()` returns EIO (`root.go:130-132`) while the spawned goroutine is still inside `h.f.ReadAt(dest, off)`. `dest` is go-fuse-owned (`req.outPayload` from a `sync.Pool`); once Read returns, go-fuse frees that buffer back to the pool and a subsequent op gets the same slice. The abandoned `ReadAt` then scribbles remote bytes into a buffer owned by an unrelated request — a data race and silent cross-request corruption (one process can read another's bytes). Default `opTimeout` 30s, so the window is real. (Write path is safe — `data` is read-only input.)
- **Fix:** Read into a private buffer, copy only on success: `buf := make([]byte, len(dest)); n, err := h.f.ReadAt(buf, off); ...; return fuse.ReadResultData(buf[:n])`. Never let an abandonable backend call write into the go-fuse `dest`.

## High

### op timeout never marks the client dead — silently-dropped SSH link wedges the mount, no reconnect
- **Where:** `internal/washmount/root.go:124-133`
- **Bug:** `markDead` is called only from the error arm (`root.go:127`). The timeout arm returns EIO but never marks the client dead, so `r.cur` keeps handing back the dead client; every later op blocks 30s and EIOs again. Only an *error-returning* death triggers re-dial, never a *hang-style* one (NAT/conntrack timeout, suspended laptop, cable pull — no RST). Reachable because the mount's ssh (`apps/remote/be/mounts.go ~138-141`) sets **no `ServerAliveInterval`** (the relay supervisor does). Doc comment at `root.go:99-101` is false for this path.
- **Fix:** Call `r.markDead(cl)` in the `case <-cctx.Done():` arm before returning EIO (idempotent, safe). Add `ServerAliveInterval`/`CountMax` to the mount's ssh args so silent deaths surface as errors.

### Reconnect storm: dial-succeeds-but-connection-dies busy-loops with no backoff (ssh fork bomb)
- **Where:** `internal/remotewatch/remotewatch.go:262-291`
- **Bug:** `Client.run()` resets `backoff = 100ms` unconditionally after a successful dial; backoff only grows in the dial-*error* branch. If dial succeeds but the channel dies immediately, the loop re-dials with zero delay. In prod dial == `ssh -o BatchMode=yes <host> wash-fswatchd`, which returns success the instant `cmd.Start()` forks; if ssh then exits at once (auth fail, host-key change, missing `wash-fswatchd`) it forks a new ssh per iteration — a tight fork bomb on both hosts. Untested (`TestClientNoSpuriousReconnect` only covers a stable connection).
- **Fix:** Throttle every iteration: `start := time.Now()` before serve(); after detach, if `time.Since(start) < minHealthy` (a few seconds) grow backoff and sleep; only reset backoff after the connection proved healthy for a minimum duration.

## Medium

### Timed-out Read/Write/Release leaks fileHandle.mu held by the abandoned goroutine, wedging the fd
- **Where:** `internal/washmount/node.go:129-176` (with `root.go:121-133`)
- **Bug:** Read/Write/Release acquire `h.mu` inside the spawned closure. On timeout, `run()` returns EIO while that goroutine still holds `h.mu`; every later op on the handle blocks on `h.mu.Lock()` and itself times out → that fd wedges at EIO. Worsened by the markDead-on-timeout gap (stalled-but-alive transport → never dropped → wedged indefinitely). Scope: one fd, not the whole mount.
- **Fix:** Don't abandon work that holds per-handle state — acquire `h.mu` before spawning and pass ownership in, or give each handle a single-flight worker that marks the handle dead and returns EIO deterministically.

### sem slot freed on op timeout while the abandoned op still holds the channel — defeats MaxInflight
- **Where:** `internal/washmount/root.go:107-133`
- **Bug:** `sem` is released via a deferred return, so it frees on the timeout branch too, while the spawned goroutine is still pumping a real SFTP request on the one ssh channel. Under a slow server, N ops time out, free their slots, and the next N are admitted while the first N still consume channel/window — true in-flight grows past `MaxInflight` exactly when the server is congested.
- **Fix:** Release the slot from the goroutine that actually finishes the call (move `<-r.sem` into the spawned goroutine after `fn` returns), not from `run()`'s deferred path.

### Hub keeps a stale, closed Subscription after unregister→re-register — silent event loss
- **Where:** `internal/fswatch/hub.go:37-61` (with `apps/remote/be/mounts.go:178-255`, `apps/fswatch/be/app.go:222-234`)
- **Bug:** `Hub.forward()` reacts to Events() closing with only `if !ok { return }` — it doesn't delete the `hubPath` or clear subscribers. On unmount the feed sub closes (forward exits) but the stale `hubPath` persists with live subscribers; a re-`registerMount` of the same mountpoint installs a new Client but the Hub keeps the old closed sub and never re-Subscribes. After remount, watchers under that mountpoint silently get no events until the consumer drops the ref to 0.
- **Fix:** On `ok==false`, remove the `hubPath` under lock and clear/notify subscribers so a later Subscribe reopens a fresh Watch through the Router's current Source (or have register/unregister tell the Hub to drop watches under the mountpoint).

### Rename ignores RENAME_NOREPLACE/RENAME_EXCHANGE — silent clobber, EXCHANGE corrupts the dentry tree
- **Where:** `internal/washmount/node.go:238-250`
- **Bug:** Rename takes `flags uint32` but discards it and always calls `cl.PosixRename` (atomic replace). `RENAME_NOREPLACE` gets a silent overwrite instead of EEXIST. Worse: go-fuse branches on `RENAME_EXCHANGE` when the impl returns errno 0 and calls `ExchangeChild` — so after a one-way destructive `PosixRename` returns 0, the kernel swaps the inode/dentry tree as if both entries still existed: kernel view and backing store diverge.
- **Fix:** Inspect flags — `RENAME_NOREPLACE`: Lstat target (or plain Rename) and return EEXIST if present; `RENAME_EXCHANGE`/`RENAME_WHITEOUT`: return EINVAL rather than a destructive replace returning 0.

### Read at/after EOF (num==0, io.EOF) returns EIO instead of 0
- **Where:** `internal/washmount/node.go:126-142` (toErrno `root.go:161-194`)
- **Bug:** `ReadAt` returns `(0, io.EOF)` at/past EOF. The success guard `if err != nil && num > 0` doesn't fire for `num==0`, so raw `io.EOF` reaches `toErrno`. pkg/sftp returns the bare `io.EOF` sentinel (not `*sftp.StatusError`), so `errors.As/Is` all miss it → default EIO. A zero-byte read at EOF (POSIX success) becomes an I/O error. Narrow trigger (reachable in the AttrTimeout window after a concurrent server-side truncate).
- **Fix:** Treat `io.EOF` as success regardless of num: `if errors.Is(err, io.EOF) { return num, nil }` before the `num>0` check, or map `io.EOF` in `toErrno`.

## Low

### Unmount of a non-fuse/stale path can write to an arbitrary FUSE connection's abort file
- **Where:** `internal/washmount/unmount.go:41-66, 89-94`
- **Bug:** `fuseConnMinor` returns `unix.Minor(st.Dev)` with no check the path is a fuse mount. For a stale non-fuse mountpoint, lazyDetach fails, `minorErr==nil`, so forceUnmount writes the abort file built from the *backing* fs's minor. fuse connections are keyed by major-0 minors from the same pool as tmpfs/overlay/sysfs; a numeric collision could abort an unrelated live mount instead of returning ENOENT.
- **Fix:** Verify the mountpoint is actually a fuse mount (check `st.Dev`'s major is the fuse major, or that `/sys/fs/fuse/connections/<minor>` corresponds to this mount) before writing the abort path; return a clear "not mounted" when neither graceful nor lazy applies.

### go-fuse loop goroutines leak on the wedged-backend lazy-detach-success unmount path
- **Where:** `internal/washmount/unmount.go:29-66`
- **Bug:** go-fuse reaps loop goroutines only in `Server.Unmount()` success. On lazy detach, `fusermount3 -uz`/MNT_DETACH returns 0 even while the connection is pinned by a parked op, so the loop goroutines block forever in `syscall.Read` on `/dev/fuse` and `Unmount` returns without escalating to abort or calling `Wait()`. In the long-lived `apps/remote/be` process this leaks the loop-goroutine set per such unmount. (The "owned ssh subprocess also leaks" sub-claim was refuted — `mounts.go:252` closes it.)
- **Fix:** On the wedged case, escalate to abort-after-detach (readers get ENODEV and exit), then `server.Wait()`. Do **not** call `Wait()` after a bare lazy success — it would hang the caller on that exact path.

### Double-Close of a clientSub emits a duplicate unwatch and can prematurely decrement a shared refcount
- **Where:** `internal/remotewatch/remotewatch.go:338-358`
- **Bug:** `clientSub` has no idempotency guard (unlike `feedSub`'s `done`). A second Close skips the decrement (`>0` guard) but the separate `==0` check re-enters and encodes a second unwatch frame. With two live subs sharing a remote (`watched[R]=2`), double-closing one drives 2→1→0 across the two calls and emits a premature unwatch while the other sub is live, stopping B-side events for the survivor until reconnect.
- **Fix:** Make `clientSub.Close` idempotent with a `sync.Once`/`closed bool` under `c.mu`, mirroring `feedSub`'s `done`.

### mount() exists-check is a TOCTOU — two concurrent mounts of the same mountpoint both proceed
- **Where:** `apps/remote/be/mounts.go:178-234`
- **Bug:** The duplicate-mount guard reads `m.entries[mp]`, releases `m.mu`, then does MkdirAll + eager ssh dial + `fs.Mount` without the lock, re-acquiring only for an unconditional insert with no re-check. Concurrent callers are by design (`restoreMounts` spawns `go m.mount(...)`; controls spawn another). A restore racing a manual mount of the same target: both pass `exists==false`, both bring up a `*fuse.Server`+ssh child, the second insert clobbers the first entry → first becomes unreachable and can never be unmounted, leaking a server+ssh child and stacking two mounts on one dir.
- **Fix:** Hold `m.mu` across the whole bring-up, or reserve `mp` with a placeholder entry under the lock before dialing (roll back on failure) so a concurrent caller sees it present.

### Create leaks a half-created file on chmod/lstat failure (no rollback); Mkdir shares the lstat variant
- **Where:** `internal/washmount/node.go:178-224`
- **Bug:** Create does `OpenFile(O_CREATE)` (creates server-side) then a separate Chmod and Lstat; on either failure it only `f.Close()`s and returns the errno — never `cl.Remove(p)`. The app sees Create fail yet a zero-byte default-mode file is left behind. Mkdir has the same shape on its Lstat.
- **Fix:** On chmod/lstat failure after a successful create, best-effort `cl.Remove(p)`/`cl.RemoveDirectory(p)` before returning, or make chmod/lstat best-effort.

### Rename has no fallback when the server lacks the posix-rename extension — spurious ENOSYS for every mv
- **Where:** `internal/washmount/node.go:245-249`
- **Bug:** Rename unconditionally calls `cl.PosixRename`, which always sends `posix-rename@openssh.com` with no capability check; a server without it answers OP_UNSUPPORTED → ENOSYS, so every `mv` fails even though plain `cl.Rename` would work. Comment at `node.go:246-247` implies conditional behavior that doesn't exist. Low because every transport this repo wires hits OpenSSH sftp-server (has the extension since 4.8); bites only a non-OpenSSH daemon or the planned wash-native backend.
- **Fix:** On OP_UNSUPPORTED/ENOSYS from PosixRename, retry with `cl.Rename` (emulate clobber via Remove+Rename only when NOREPLACE isn't requested), or probe `HasExtension` once. Fix the comment.

### Fsync is an unconditional no-op — successful fsync(2) doesn't request server-side durability
- **Where:** `internal/washmount/node.go:158-167`
- **Bug:** Fsync and Flush both `return 0` with no backend call. Flush is harmless (WriteAt is synchronous; Release closes and returns its errno). But Fsync returning 0 with no server flush means fsync(2)/fdatasync guarantees only transmitted-and-acked, not persisted — a real (minor) weakening that atomic-save editors rely on. pkg/sftp exposes `(*File).Sync()` via `fsync@openssh.com`, degrading to ENOSYS when absent.
- **Fix:** In Fsync, attempt `h.f.Sync()` and fall back to 0 only when unsupported. Keep Flush a no-op but document fsync durability is best-effort.
