# wash-mount — remote filesystem mounts

Mount another machine's filesystem into wash as a **real kernel FUSE mount**, so
every process on the box sees it — not just wash-aware apps — and so filesystem
*watching* stays live and correct over the network.

Status: built and green on `branches/wash-mount` — end-to-end through the UI on
the two-VM `make e2e-mount-vm` gate, with the full repo gate (unit + multicall +
VM gates + packaging) passing. See §8 for known limitations.

---

## 1. Goals & scope

- **Every process sees it.** A real kernel mount at `~/wash/remote/<host>/…`, not
  an app-level virtual view — the terminal, native apps, and wash apps all see
  one uniform tree.
- **Network-agnostic watching.** From a wash app's point of view, watching a
  remote path is identical to watching a local one. This is the hard part: local
  inotify on a FUSE mount is blind to changes made on the remote host.
- **wash-to-wash.** Scope is mounting *wash hosts* (the same set wash-remote
  already reaches — those run a full wash). Plain-sshd boxes still work for data,
  but live watch needs wash on the far end.

### Why FUSE, and why not sshfs

sshfs is orphaned/unmaintained and has no SFTP pipelining (poor over WAN). The
modern kernel-mount answer is a native Go FUSE filesystem: `hanwen/go-fuse` (pure
Go, no libfuse/cgo, talks `/dev/fuse` directly) with a `pkg/sftp` backend — a
port of go-fuse's loopback example with the POSIX backend swapped for SFTP.

---

## 2. The two-channel design

A mount uses **two channels, split by concern**, because data and change-
notification have opposite requirements:

| channel | carries | why |
|---|---|---|
| **SFTP** | data + metadata (read/write/stat/readdir at offsets) | purpose-built, ready-made random-access IO; works against any sshd |
| **wash watch** | "it changed" | SFTP cannot express change-notification; the remote host's own inotify can |

They stay coherent because both ultimately hit the remote host's real
filesystem: the remote's inotify is the single source of truth for *all* changes
(SFTP-driven, host-local, or third-party), so a write over SFTP is observed by
the same inotify that feeds the watch channel.

**Why not full wash-native (serve files over wash's own protocol)?** wash's
`internal/fs` is whole-file (`Write(path, content, maxBytes)`, `List`, `Stat`) —
it has no offset/random-access IO. Going fully wash-native would force building
an offset-IO file service on the remote. SFTP gives that for free, so we keep
SFTP for data and add *only* the wash watch channel. (Full-wash-native data is a
possible future optimization, not needed now.)

---

## 3. Watch architecture — a shared service

Today every wash app runs its **own** in-process `fswatch.Manager` (inotify) and
forwards events to its FE over its own channel. There is no shared watch service.

Mounts, however, must live in a **shared, persistent background service**
(system-wide kernel mount, surviving any one window). So remote change events
have to cross from the mount owner to the watching app. We chose to **refactor
watching into a shared cross-process service** rather than give each consumer its
own remote channel. Benefits:

- One `ssh … wash-fswatchd` channel per mounted host, shared by all watchers
  (vs. one per app).
- Collapses N per-app inotify instances into one — easing the
  `fs.inotify.max_user_instances` ceiling that has bitten the e2e suite.

### Path-routing

The service watches through a `fswatch.Router`: a `Source` that dispatches each
`Watch(path)` by longest-matching prefix — a path under a remote mount routes to
that mount's remote watch client; everything else to the local inotify Manager.
The consumer never learns which side answered.

```
remote host (B)                          local desktop (A)
  inotify  ──wash-fswatchd──►  remotewatch.Client ─┐
                                                    ├─► fswatch.Router ─► Hub ─► subscribers
  (sshd) ◄────────SFTP────────  go-fuse mount       │        (local inotify Manager) ┘
```

---

## 4. Components

### Built (in-process, green)

| package / cmd | role |
|---|---|
| `internal/washmount` | go-fuse nodes over a `*sftp.Client`; per-op timeouts → EIO (never hang the kernel), errno mapping, tree-derived paths (survive rename). The data path. |
| `internal/washmount` `Unmount` | escalating escape hatch: graceful → `fusermount3 -uz` → `/sys/fs/fuse/connections/<n>/abort`. A dead backend never strands a wedged mount. |
| `internal/fswatch` `Source`/`Subscription` | the seam: consumers watch a `Source`, not inotify directly. Local `*Sub` and remote feeds both satisfy it. |
| `internal/fswatch` `Feed` | a `Source` driven by pushed `Emit(Event)` — the entry point a remote stream (or poll) calls. |
| `internal/fswatch` `Router` | path-routing `Source` — local inotify vs. remote mount, longest-prefix. |
| `internal/fswatch` `Hub` | shared-service engine: multiplexes many subscriber-ids over one `Source`, one underlying watch per path, refcounted. |
| `internal/remotewatch` | the wash watch wire: `Serve` (B side, wraps the remote `Source`) ↔ `Client` (A side, a drop-in `Source`), with B-path ⇄ mountpoint `PathMap` translation. |
| `internal/mountsession` | ties data (SFTP mount) + watch (remotewatch.Client → Router) into one lifecycle. |
| `internal/sftptest` | in-process SSH+SFTP server for tests. |
| `cmd/wash-fswatchd` | B-side watch daemon (inotify → stdio stream), launched `ssh <host> wash-fswatchd`. Ships in BINS. |
| `apps/fswatch/be` (`com.wash.fswatch`, `wash-fswatch`) | A-side shared watch service: `watch`/`unwatch`/`unwatch_all` + `register_mount`/`unregister_mount` over the `Hub`+`Router`, streaming `fs_event` to subscribers. Serves local inotify and remote-mount paths. The streaming sibling of `StateService`. |
| `internal/sdk` `WatchClient` | the one consumer-side client: relays watch/unwatch to the service and dispatches `fs_event` to per-path callbacks (refcounted). Chains `OnAppMsgFrom`, so it works with or without a Bus. **fm, session, and the filepicker all watch through it — no app runs its own inotify Manager.** |
| `apps/remote/be` (`com.wash.remote`) | the supervisor's mount capability: on a `mount` control it FUSE-mounts `host:remoteRoot` over `ssh -s sftp` (`washmount.Mount`), sends `register_mount` to `com.wash.fswatch`, and publishes per-mount status in `State.Mounts`. `unmount` detaches via the escape hatch + `unregister_mount`. |
| `apps/connect` (`wash-connect`) | the mount UI: each connected host lists its mounts (status + unmount) with a path input to mount another. BE relays `mount`/`unmount` to the supervisor; mounts ride the existing `remote.state` push. |
| `cmd/wash-mount` | optional standalone FUSE mount CLI (FUSE runtime dep; not in BINS, like wash-display). |

### Status: built and green

All of the above is implemented. The two-VM gate **`make e2e-mount-vm`** passes:
mount a remote folder, browse it in a local fm, watch a B-side change propagate
live, plus a torture test co-driving one folder from a local fm (the mount) and
a remote fm (on B). The full repo gate (`test-all` + the remote/mount VM e2e +
packaging matrix) is green.

### Deferred (post-v1)

1. **fm Places/Volumes sidebar** — mounts are browsable by navigating to
   `~/wash/remote/<host>/…`, but don't yet appear as named volumes in fm.
2. **"Mount folder…" picker** — the wash-connect UI takes a typed path; a remote
   `List`/`Stat` folder browser (reuses wash's existing FS-list, *not* offset IO)
   is the nicer entry point.
3. **wash-native data backend** for wash peers (push-watch + relay reuse), behind
   the existing `Backend` seam — SFTP stays the universal floor.
4. **Watch-path reconnect** (the data path + bounded concurrency are done) and a
   **UI-driven two-VM chaos gate** (kill ssh in the VM → fm recovers) — the
   reconnect logic is already covered by a Go-level chaos test.
5. **ControlMaster** to fold data + watch + desktop onto one ssh connection.

---

## 5. Transport & connection

v1 uses **separate ssh channels** on the shared agent: `ssh <host> -s sftp`
(data) and `ssh <host> wash-fswatchd` (watch) — decoupled from the wash-remote
relay code, no new auth. A later optimization shares one TCP connection across
desktop + data + watch via OpenSSH `ControlMaster`.

Mount is a **service on a wash-remote connection**, surfaced under its host in
wash-connect (control) — not a separate app. The supervisor calls
`washmount.Mount` directly (the watch lives in the separate `com.wash.fswatch`
process, so it does *not* use the in-process `mountsession` composition — that
stays the standalone/test path). Access today is by navigating fm to
`~/wash/remote/<host>/…`; the fm Places/Volumes sidebar is deferred.
`cmd/wash-mount` stays a headless/dev tool.

---

## 6. Robustness

- Every backend call is bounded by a timeout → `EIO`; the kernel is never made to
  wait forever (the classic uninterruptible-D-state tarpit).
- Every backend error maps to a real errno.
- The escalating unmount guarantees a dead backend can always be detached.
- Still ahead: a reconnect supervisor + bounded concurrency, and a **Tier-2
  two-VM chaos gate** (kill connection / latency / partition; assert no-wedge,
  reconnect-recover, abort frees mount) reusing the `net-matrix` tc/qemu knobs.

---

## 7. Optionality & runtime prerequisites

The FUSE mount needs the kernel `fuse` module + a **setuid** `fusermount3` and a
`/dev/fuse` the mounting user can open. The wash VM image bakes these (`fuse3`
package, `modprobe fuse`, `chmod 666 /dev/fuse`, `chmod u+s fusermount3` —
go-fuse opens `/dev/fuse` as the user, then execs setuid `fusermount3` for the
privileged `mount(2)`). `wash-fswatchd` (inotify only, no FUSE) ships everywhere
in `BINS`. `wash-mount` (the CLI) is the only FUSE-gated artifact and stays out
of `BINS`, opt-in like wash-display.

Where FUSE is unavailable (the in-browser VM is virtio-only; some locked-down
hosts), mounting is simply not offered — there is **no automatic fall-back to a
virtual SFTP view today** (see Limitations). The library code compiles without
FUSE; it fails at mount time, surfaced as a `MountState{status:"error"}`.

---

## 8. Known limitations

- **No FUSE → no mount, no fallback.** Hosts without the FUSE prerequisites
  (notably the in-browser VM) can't mount, and there is no degraded virtual-view
  fallback yet — the mount just reports an error.
- **wash-to-wash only for watch.** The SFTP data path works against any sshd box,
  but live watch needs `wash-fswatchd` on the far end. Mounting a non-wash host
  is untested and would give data-without-watch (stale until the attr-timeout).
- **Multiple ssh connections per host.** Data (`ssh -s sftp`), watch
  (`ssh wash-fswatchd`), and the wash-remote desktop relay are separate ssh
  sessions on the shared agent. ControlMaster consolidation is deferred.
- **Partial reconnect.** The **data path self-heals**: a dropped ssh re-dials on
  the next op without unmounting (`washmount.MountWithDialer`; the supervisor
  re-execs `ssh -s sftp`), and in-flight ops are bounded by a concurrency
  semaphore. The **watch path does not yet reconnect** — if the `wash-fswatchd`
  ssh drops, live updates stop until remount (data stays correct; the kernel
  attr-timeout gives eventual consistency). Reconnect is proven by a Go-level
  chaos test (kill the connection → recover); a **UI-driven two-VM chaos gate**
  (kill ssh in the VM, assert the fm recovers) is not yet built.
- **Single-uid visibility.** The mount is not `allow_other`, so only the mounting
  user's processes see it (true "every process" within that user's session, not
  cross-user).
- **Cooperative watch GC.** A consumer that crashes without sending
  `unwatch_all` leaks its watches in the service until process exit (no
  instance-gone signal exists for a background service).
- **Cosmetic mountpoint.** Mounts land at `~/wash/remote/<host>/<base>`, which
  renders as `…/wash/wash/remote/…`; the base path could be tidied.
- **No caching tuning.** Beyond the 3 s attr/entry timeout there's no VFS cache;
  random IO over a high-latency link relies on `pkg/sftp`'s pipelining.
- **e2e harness flakiness.** `mount-vm` test 1 occasionally needs a retry due to
  the pre-existing two-VM-boot race in `washvm-remote-run` (not the mount logic);
  `retries: 1` covers it.
