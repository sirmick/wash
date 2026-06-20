# wash-mount — remote filesystem mounts

Mount another machine's filesystem into wash as a **real kernel FUSE mount**, so
every process on the box sees it — not just wash-aware apps — and so filesystem
*watching* stays live and correct over the network.

Status: in development on `branches/wash-mount`. The in-process building blocks
are complete and green; the cross-process service + app integration is in
progress.

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
| `apps/fswatch/be` (`com.wash.fswatch`, `wash-fswatch`) | A-side shared watch service (step 1, local-only): `watch`/`unwatch`/`unwatch_all` over the `Hub`+`Router`, streaming `fs_event` to subscribers. The streaming sibling of `StateService`. |
| `cmd/wash-mount` | optional standalone FUSE mount CLI (FUSE runtime dep; not in BINS, like wash-display). |

### Remaining

1. **`com.wash.fswatch`** — background singleton wrapping the `Hub` with a bus
   `subscribe`/`unsubscribe` + targeted event stream. The shared watch service.
2. **Migrate consumers** — fm, edit, filepicker off their own `fswatch.Manager`
   onto a fswatch-service client. (consumer-facing change)
3. **Supervisor** (`com.wash.remote`) — perform the FUSE data mount, register the
   mount's `remotewatch.Client` into the service's `Router`, publish mount state.
4. **FE** — wash-connect: mounts listed under each server + a "Mount folder…"
   dialog (a remote `List`/`Stat` picker — reuses wash's existing FS-list, *not*
   the offset IO). fm: a Places/Volumes sidebar reading the mount state.
5. **e2e** — the two-VM wash-remote harness (`make e2e-remote-vm`); B's image now
   carries `wash-fswatchd`. Possibly a headless cross-machine `wash-mount` gate
   first to exercise the real ssh+sftp+fuse+watch path before the FE lands.

---

## 5. Transport & connection

v1 uses **separate ssh channels** on the shared agent: `ssh <host> -s sftp`
(data) and `ssh <host> wash-fswatchd` (watch) — decoupled from the wash-remote
relay code, no new auth. A later optimization shares one TCP connection across
desktop + data + watch via OpenSSH `ControlMaster`.

Mount is a **service on a wash-remote connection**, surfaced under its host in
wash-connect (control) and as a volume in fm's sidebar (access) — not a separate
app. `cmd/wash-mount` stays a headless/dev tool.

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

## 7. Optionality

The FUSE mount needs the kernel `fuse` module + `fusermount3`/`CAP_SYS_ADMIN`,
absent in the in-browser VM and some locked-down hosts. So `wash-mount` is opt-in
like wash-display, and where FUSE is unavailable the feature degrades to the
wash-fm virtual SFTP view. `wash-fswatchd` (inotify only, no FUSE) ships
everywhere.
