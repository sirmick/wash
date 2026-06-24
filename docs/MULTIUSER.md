# wash — Multi-user authentication & session plan

v1 multi-user support for wash. Adds **one new binary** (`wash-login`),
**three new flags** to `wash-router`, and **zero protocol changes**.
Every existing wash mechanism — capability manifests, asset-pull, app
supervision, the wire — survives untouched.

This document is the locked plan for `wash-login` + per-user
`wash-router` topology. Decisions here are settled; see
[ARCHITECTURE.md](ARCHITECTURE.md) for the underlying invariants this
plan respects.

## Motivation

wash v1's "localhost-trust, single-user" model blocks every deployment
past a personal box behind a tunnel. The fix is multi-user
authentication and per-uid isolation, without breaking the "router is
pure transport" invariant or the "single static binary" pitch.

## Architectural commitment

These invariants are non-negotiable. The design works around them.

- wash speaks **HTTP/WS only**. TLS is delegated to nginx / Caddy /
  Tailscale-serve / stunnel / SSH tunnel.
- The **router is transport, not interpreter** — it never
  authenticates, never sees user identity, never knows about sibling
  routers or sessions.
- **Browser-side code has zero ambient authority.** Same trust model
  as v1.
- **Apps run as the user**, never as a privileged daemon. Per-uid
  isolation is per-process, not per-thread.
- **Pure-Go, CGO-disabled, single static binary** for every wash
  binary on every target including embedded.

## Topology

```
browser ──HTTPS/WSS──▶ nginx :443 ──HTTP/WS──▶ wash-login :11000
                       (TLS terminator,                │
                        rate limit, ACL)               │  HTTP: serves shell, lib, login page,
                                                       │        validates cookie, picks sessid
                                                       │  SCM_RIGHTS handoff of TCP fd
                                                       ▼
                                          /run/wash/<uid>/sessions/<sessid>.sock
                                                       │
                                                       │  per-user router takes ownership
                                                       │  of the TCP fd; does WS Accept itself
                                                       ▼
                                    wash-router (uid=N, --name <s>)
                                          │
                                          ├─ wash-session
                                          ├─ wash-term
                                          ├─ wash-fm
                                          └─ ...
```

**wash-login is NOT in the data path after handoff.** It does the
HTTP/cookie-validation dance but does NOT perform the WebSocket
upgrade itself. Instead, it hijacks the raw TCP socket *before*
writing the 101 response, dials the per-user router's ctl socket,
sends the TCP fd + the buffered HTTP-upgrade request bytes via
SCM_RIGHTS, and closes its handle. The per-user router reconstitutes
the connection (prepending the replay bytes to the fd's read stream),
runs `websocket.Accept` on the synthesized HTTP request, and from
that point owns the WebSocket end-to-end. Subsequent bytes flow
`browser → nginx → kernel → per-user router → apps`, with wash-login
holding zero state for the session.

### Receiver-side WS framing

The per-user router needs to perform the HTTP+WS upgrade on a
post-accept TCP fd that was handed over by wash-login. This is done
by feeding the replay bytes + fd into a single-conn `http.Server`
whose handler calls `websocket.Accept`, then wrapping the resulting
`*websocket.Conn` in the existing `NewWSTransport` and handing to
`HandleShell`. ~150 LOC of plumbing; no new WS frame codec required.

## Components

### wash-login (new)

Privileged front-door binary. Runs as `wash-system` uid with
`CAP_SETUID`, `CAP_SETGID`, `CAP_KILL`, and group `wash` membership.
Single instance per host.

Responsibilities:

1. **Serves static HTTP**: login page, shell runtime, `@wash/ui`
   library, PWA manifest, icons. All embedded via `//go:embed`.
2. **Authenticates** incoming users. Default: `/etc/shadow`
   simple-attempt, pure Go. v2: pluggable `--auth-helper` exec
   interface.
3. **Hosts a degenerate wash session** for the pre-handoff UI:
   greeter app (pre-auth) and picker app (post-auth, when needed).
4. **Discovers sessions** by scanning `/proc` for `wash-router`
   processes belonging to the authed uid.
5. **Spawns new sessions**: `fork → setuid(uid) → exec wash-router`
   with `--listen-unix` + `--name`. Socket-activated.
6. **Hands off the TCP fd via SCM_RIGHTS** to the per-user router's
   ctl socket *before* writing the HTTP 101 response. After Sendmsg,
   wash-login closes its handle; the kernel-dup'd fd lives on in the
   per-user router. wash-login holds no state for the session.
7. **One piece of persistent state**: an HMAC secret at
   `/etc/wash/secret.key` (mode 0600 owned by wash-system) for
   signing session cookies.

Routes:

| Route | Purpose |
|---|---|
| `GET /` | Shell bootstrap if authed; greeter if not. |
| `GET /login` | Greeter app (degenerate wash session). |
| `POST /auth` | Validate credentials, set signed cookie, redirect. |
| `GET /logout` | Clear cookie. Optional `?end_session=<sessid>` SIGTERMs that router. Optional `?end_all=true` SIGTERMs all of this uid's routers. |
| `GET /shell.{html,js,css}`, `GET /lib/*`, `GET /favicon.ico`, `GET /manifest.webmanifest` | Static shell-runtime assets. |
| `GET /ws` | Pre-handoff WS: serves picker/greeter locally. |
| `GET /ws/s/<sessid>/` | Post-selection WS: proxies to the per-user router. |

### wash-router (existing, three new flags)

The router gains:

- `--listen-unix <path>` — listen on a Unix socket as the ctl socket
  for incoming SCM_RIGHTS handoffs from wash-login. Each accepted
  message carries a TCP fd + replay-byte payload; the router runs the
  HTTP/WS upgrade on that fd and attaches the resulting WebSocket as
  a shell view.
- `--name <s>` — human-readable session name. Stored in memory at
  startup; echoed in `stat` RPC responses; visible in
  `/proc/<pid>/cmdline`. **Immutable** for the life of the process.
- `--idle-timeout <duration>` — self-exit after this long with no
  attached shell. Default 30 minutes.

The router does **not** know about sessids, sibling routers, or
wash-login. Its socket path is the only routing identity it has, and
it doesn't parse it. Apps inside the router run unchanged.

Both wash-router and wash-login embed shell assets (the brotli'd
shell runtime is a few hundred KB; duplication is acceptable). The
existing single-user `--listen` TCP+HTTP+WS mode stays for the
loopback / embedded `--max-sessions-per-uid=1` deployment.

### Supervisor

wash-login *is* the supervisor. No systemd integration required.
No PID files. No state files.

- **Spawn**: fork → setuid(uid) → exec wash-router with the right
  argv. Socket-activated: wash-login `bind()`s the listen socket,
  passes the fd to the child via inheritance.
- **Discover**: scan `/proc`, filter by uid + cmdline pattern +
  State != Z/T. Parse argv for sessid and name.
- **Stat**: per-router `stat` RPC over the ctl socket returns
  `{started, last_active, app_count, window_count,
  focused_window_title}` for picker rendering.
- **End**: SIGTERM the router pid (found via /proc scan). Router
  cleans up apps and exits.
- **Reap**: routers self-exit on idle. wash-login is reactive only.
  Orphaned routers (after wash-login restart) reparent to PID 1 and
  continue working; wash-login finds them on next /proc scan.

wash-login is **stateless** apart from the HMAC secret. Restarting
it has zero session impact.

### Picker (new wash app)

Lives at `apps/picker/`. Hosted by wash-login's degenerate wash
session. Manifest declares a `wash_login_rpc` capability that
ordinary session apps do not have.

RPCs against wash-login:

- `list_sessions()` — returns `[{sessid, name, started, last_active,
  app_count, window_count, focused_title}]`.
- `spawn_session(name string) → sessid` — fork-exec a new router.
- `end_session(sessid)` — SIGTERM that router.

On "Resume" or "New" the picker sends an `app_msg` to wash-login →
wash-login responds with `{reconnect_to: "/ws/s/<sessid>/"}` → the
browser's shell.js closes the WS and reconnects.

### Auth backends

**Default — rely on the system's `login` mechanism**:

```
wash-login receives (username, password)
   ├─ spawn `su -c true <username>` attached to a PTY
   ├─ wait for the "Password: " prompt
   ├─ write the password followed by newline
   ├─ wait for su's exit code (0 = valid, non-zero = invalid)
   └─ on success: parse /etc/passwd for uid/gid; mint cookie
```

The "no NSS, no fancy syscalls" path: we don't reimplement crypt,
don't link libpam, don't shell out to `getent`. wash-login asks the
system to perform an actual login (via the universal `/bin/su`
binary, which goes through PAM and inherits whatever auth chain the
distro is configured for — local, LDAP, SSSD, Kerberos), and reads
the result. `-c true` makes su exec `/bin/true` instead of a shell,
so there's no .profile execution, no motd, no utmp churn — just an
exit code.

User list on the login page: parsed from `/etc/passwd` directly,
filtered by uid ≥ 1000 and shell not in {nologin, false, halt,
shutdown, sync}. Sorted by name, rendered as clickable tiles that
prefill the username via `?user=`.

**Dev / CI: `--auth-test user:password`** hard-codes one credential
for repeatable testing without touching real Unix accounts. The e2e
suite uses this.

**v2: pluggable `--auth-helper <cmd>`** exec interface for SSO,
Tailscale-whois, custom backends. Out of v1 scope.

Cookie shape: `Authorization=<base64({uid, expiry}).<hmac>` (or
equivalent signed-cookie form). `HttpOnly; Secure; SameSite=Strict;
Path=/`. Default expiry 24h sliding.

## The /proc registry

```go
func listSessions(uid uint32) []Session {
    entries, _ := os.ReadDir("/proc")
    var out []Session
    for _, e := range entries {
        pid, ok := atoiPid(e.Name())
        if !ok { continue }
        st, err := readStatus(pid)
        if err != nil || st.Uid != uid { continue }
        if st.State == 'Z' || st.State == 'T' { continue }
        cmd, err := readCmdline(pid)
        if err != nil || !isWashRouter(cmd) { continue }
        sessid, name, ok := parseRouterArgv(cmd)
        if !ok { continue }
        out = append(out, Session{Pid: pid, SessID: sessid, Name: name})
    }
    return out
}
```

Filter rules:

- `Uid:` line in `/proc/<pid>/status` matches the authed uid.
- `State:` not `Z` (zombie) or `T` (stopped).
- `/proc/<pid>/cmdline` argv[0] basename is `wash-router`.
- argv contains `--listen-unix /run/wash/<uid>/sessions/<sessid>.sock`.

Dynamic data (`last_active`, `app_count`, `window_count`) comes from
the per-router `stat` ctl-RPC, fanned out in parallel at picker
render time. ~1ms per session, capped by `--max-sessions-per-uid`
(default 8).

## Session lifecycle

| Event | Mechanism |
|---|---|
| **First login** (count=0) | Auth → spawn `wash-router --name <username>` → socket-activate → SCM_RIGHTS handoff. No picker. |
| **Subsequent login** (count=1, auto-attach on) | Auth → /proc scan finds one → dial + handoff. No picker. |
| **Subsequent login** (count≥2, or auto-attach off) | Auth → picker → user clicks Resume or New. |
| **New session via picker** | RPC `spawn_session(name)` → fork → socket-activate → reconnect URL → handoff. |
| **Second tab, same sessid** | Take-over: router sends `session-moved` on old fd, closes it, serves the new fd. |
| **Second tab, different sessid** | Both live, independent. |
| **Page refresh** | New WS → take-over. Same code path as second-tab-same-sessid. |
| **Log out** (shell menu) | Browser GETs `/logout?end_session=<sessid>` → cookie cleared + router SIGTERM'd. |
| **End session** (picker) | RPC `end_session(sessid)` → wash-login SIGTERMs the pid. Cookie untouched. |
| **Idle reap** | Router's own `--idle-timeout` timer fires. Router exits. |
| **wash-login restart** | Active sessions unaffected. New connection establishment stalls briefly during restart. |
| **Router crash** | /proc no longer shows it. Browser sees WS close (kernel sends FIN/RST). User re-auths or retries. |

## Logout (first-class wash feature)

Logout is a user-visible action in the shell, not just a back-end
cookie endpoint.

**Three operations, distinct verbs:**

| Action | Effect |
|---|---|
| **Log out** (shell menu) | Clear cookie + SIGTERM this session's router. Browser navigates to `/login`. |
| **End session** (picker) | SIGTERM that router. Cookie untouched. Other sessions unaffected. |
| **Log out of all sessions** | Clear cookie + SIGTERM all routers for this uid. |

The default Log out ends this session: a logged-out user who returns
shouldn't auto-attach to a session they explicitly walked away from.

**Implementation:**

- `wash-session` FE adds a "Log out" menu item.
- On click: `window.location.href = '/logout?end_session=' + currentSessid`.
- `currentSessid` is read from the WS URL the browser connected to.
- wash-login's `/logout` route handles the SIGTERM + cookie clear.

## Decisions locked

| Decision | Choice |
|---|---|
| TLS | External (nginx/Caddy/Tailscale). wash binaries do not link `crypto/tls`. |
| Auth default | `/etc/shadow` simple-attempt, pure Go. |
| Multi-session | Multiple `wash-router` processes per uid, one per session. |
| Session naming | argv `--name`, immutable for session lifetime. Defaults: first=username, subsequent=user-supplied or timestamp. |
| Session registry | /proc walk + cmdline parse. No state file. |
| Handoff | wash-login SCM_RIGHTS the raw TCP fd + replay bytes to per-user router. Per-user router does the HTTP+WS upgrade. wash-login is not in the data path post-handoff. |
| Within-session conflict | Take-over (Cockpit-style). |
| Idle reaping | Distributed: each router self-exits. |
| Cap | `--max-sessions-per-uid`, default 8 server, 1 embedded. |
| Cookie | HMAC-signed with a key at `/etc/wash/secret.key`. |
| Logout | First-class wash feature with three verbs (log out, end session, log out of all). |
| Greeter & picker | wash apps, hosted by wash-login's degenerate session. |
| Per-router state | Stays per-router in v1: clipboard, bulk queue, sudo cache, fswatch, ptys. |
| /proc reading | wash-system uid uses cmdline (mode 0444). Does not read environ. |

## Decisions deferred (v2+)

| Item | Reason to defer |
|---|---|
| `--auth-helper` (PAM, LDAP, SSO) | Not v1-blocking. Default shadow-file covers the common case. |
| Per-user daemons (audio mixer, notifications, cross-session clipboard, cross-session wash-priv) | Audio is the first one that genuinely requires it. |
| SNI passthrough / per-user subdomains | nginx covers most cases. |
| Multi-view (two devices, synced) | Router invariant change; not necessary if multi-session covers the common cases. |
| Per-session FS sandbox | `--fs-root=$HOME` per session is fine for v1. |
| Within-app FE state reattach (xpra-style scrollback) | Per-app SDK work; orthogonal. |
| Session rename | Ephemeral names are an acceptable v1 stance. |
| Structured journald logging | Plain stderr is fine v1. |
| nginx deployment doc | Write when there's a deployer to write it for. |

## Trust model

wash-login runs as a dedicated `wash-system` Unix uid with **three
Linux capabilities** and **membership in group `wash`**:

| Capability   | What it enables                              | Why wash-login needs it             |
|--------------|----------------------------------------------|-------------------------------------|
| `CAP_SETUID` | `setuid(N)` to any uid, including 0          | fork → setuid → exec per-user router |
| `CAP_SETGID` | `setgid(N)` to any gid                       | same                                |
| `CAP_KILL`   | send signals to processes owned by other uids | SIGTERM routers on `/logout?end_session` |

Group `wash` grants dial-access to the per-user router ctl sockets.
Sockets are `mode 0660 group wash` so only wash-login (and the
owning user) can connect — the group inheritance happens
automatically because the runtime root `/run/wash` is `mode 2770
group wash` (setgid), so the setgid bit + group `wash` propagate down
to `/run/wash/<uid>/`, `sessions/`, and the socket itself.

Crucially, wash-login does **not** create or chown those per-uid
directories — it has no `CAP_CHOWN`. The per-user router (already
setuid'd to the target uid, and granted group `wash` as a
*supplementary* group via `CAP_SETGID`) `MkdirAll`s its own
`sessions/` dir and ctl socket as itself. They land owned
`<user>:wash` with no privileged chown anywhere. wash-login only
needs to write the per-uid `spawn.lock` (at `/run/wash/spawn-<uid>.lock`,
which it owns) to serialize concurrent first-spawns. Regular users
aren't in group `wash`, so they can't write `/run/wash` to pre-create
(squat) another uid's directory.

### What this trust model means in practice

**wash-login is effectively root for identity-switching purposes.**
`CAP_SETUID` permits `setuid(0)` with no password check; any code
execution inside wash-login can become root instantly. The other
caps it doesn't have (`CAP_NET_ADMIN`, `CAP_SYS_MODULE`, …) don't
matter at that point — root reacquires anything it needs.

This is the inherent cost of a single process that spawns child
processes as many users. Capabilities narrow the privilege envelope
from "full root" to "root for identity-switching" — a real reduction
in attack surface, not a magic sandbox.

### Mitigations

1. **Small, auditable surface.** wash-login is ~1500 LOC of custom
   code over `net/http` (cookie HMAC, form parse, picker render,
   hijack + SCM_RIGHTS). Reviewable in an afternoon.
2. **seccomp-bpf** (TODO, DEPLOY.md) denies syscalls beyond what
   wash-login actually uses: fork, execve, sendmsg, kill, socket,
   accept, read, write — nothing exotic.
3. **No outbound network.** wash-login listens on :11000 only;
   never opens outbound sockets.

### Privilege separation (v2)

The long-term answer is the sshd / postfix shape: a tiny
**wash-spawnd** holds `CAP_SETUID` / `CAP_SETGID` and exposes one
IPC operation (`spawn(uid, name) → sock_path`). **wash-login** then
runs with **zero** capabilities and asks wash-spawnd whenever it
needs to spawn or signal a router. An HTTP compromise in wash-login
no longer reaches `setuid(0)` because wash-login literally can't
call `setuid()`.

Deferred from v1 because it requires inventing wash-spawnd, its IPC
protocol, its lifecycle / restart story, and the bookkeeping that
matches /proc-scanning to the spawnd's authoritative session list.
Not load-bearing for "wash-login is deployable" but load-bearing for
"wash-login has minimal privilege."

### Out-of-the-box setup

```bash
make                       # builds out/wash-login (prints a caps hint)
sudo make wash-login-caps  # setcap cap_setuid,cap_setgid,cap_kill+ep
sudo make wash-login-deploy # ^ + creates the `wash` system group if missing
```

After `wash-login-deploy`, wash-login can run as any user that's in
group `wash` (typically a dedicated `wash-system` system user). The
dev path — wash-login running as your own uid spawning routers as
your own uid — works **without caps** because target uid == self uid
short-circuits the setuid call.

The packages ship an init service for the host's init system: a systemd
unit (`/usr/lib/systemd/system/wash-login.service`, deb/rpm) and an
OpenRC service (`/etc/init.d/wash-login`, apk). For hosts with neither —
containers, a MikroTik RouterOS OCI, anything supervised by
**supervisord** — an example program is installed (docs only, not
activated) at `/usr/share/wash-login/supervisord.conf`: copy its
`[program:wash-login]` stanza into your supervisor config. All three
read args from the single-source env file `/etc/default/wash-login`
(`/etc/conf.d/wash-login` on apk), so cookie policy / bind / limits are
configured in one place regardless of init system.

## File-system & socket layout

```
/etc/wash/
    secret.key                # HMAC key for cookies. Mode 0600, owner wash-system.

/run/wash/                    # Mode 2770 group wash (setgid), owner wash-system.
                              # Provisioned by systemd RuntimeDirectory= / the
                              # OpenRC initd; normalised by wash-login on spawn.
    spawn-<uid>.lock          # Per-uid flock for spawn serialization. Owner wash-system.
    <uid>/                    # Made by the target-uid router. Owner <user>:wash (setgid).
        router-<sessid>.log   # Router stdout/stderr. Owner <user>:wash.
        sessions/             # Owner <user>:wash (setgid).
            <sessid>.sock     # Per-router Unix socket. Mode 0660, group wash.

binary layout:
    /usr/bin/wash-login       # Privileged front door.
    /usr/bin/wash-router      # Per-user router.
    /usr/libexec/wash/<app>   # Per-app binaries (or wherever --apps-dir points).
```

The privileged-surface contract:

- `/run/wash` mode 2770 group `wash` (setgid), owner wash-system —
  only wash-system and group-`wash` processes can write here. The
  setgid bit propagates group `wash` to everything created beneath it.
- `/run/wash/<uid>/` owned `<user>:wash`, created by the target-uid
  router itself (no privileged chown) — the user owns it; wash-login
  reaches in via group `wash`.
- `/run/wash/<uid>/sessions/<sessid>.sock` mode 0660 group `wash` —
  wash-login (member of group wash) can dial; the user can read/write
  their own sockets; nothing else has access.
- wash-system uid is in group `wash`. That group membership is the
  only thing granting wash-login cross-uid socket access; no
  `CAP_DAC_OVERRIDE` needed.

## Implementation milestones

### M1 — wash-router accepts SCM_RIGHTS handoffs

- `--listen-unix <path>`, `--name <s>`, `--idle-timeout <d>` flags.
- Unix-socket ctl listener that accepts SCM_RIGHTS messages: extract
  the passed TCP fd + replay-byte payload, wrap as a `net.Conn` with
  the replay bytes prepended, run HTTP+WS upgrade on it via a
  single-conn `http.Server` whose handler calls `websocket.Accept`
  and wraps the result in `NewWSTransport` for `HandleShell`.
- `SO_PEERCRED` check: refuse handoffs from peers outside the
  configured allowed-uid (default: same as router's own uid).
- Take-over on second concurrent attach (send session-moved, close
  the old transport, accept the new).
- Self-exit on idle.
- `stat` RPC handler returning router metadata.

**Exit criterion:** a Go test spawns wash-router with `--listen-unix
/tmp/.../t.sock --name testsess --idle-timeout 1m`, opens a
TCP socketpair as a stand-in browser, writes a synthetic WS-upgrade
request into one end, SCM_RIGHTS-sends the other end + the request
bytes to the router's ctl socket, then reads the 101 response and
runs the wash-wire handshake — receiving the `wash_session`
app-declared event, sending close, observing graceful shutdown.

### M2 — wash-login skeleton + login

- New `cmd/wash-login/` binary running on TCP :11000.
- HTTP server with login page, embedded shell + login assets,
  `/etc/shadow` auth.
- HMAC cookie at `/etc/wash/secret.key`.
- `/logout` (cookie-clear only — SIGTERM logic lands in M3).
- Loopback-only bind by default; `--insecure-listen` to opt in to
  0.0.0.0.
- Disabled-shell rejection (no spawning for nologin/false users).
- WS upgrade at `/ws` serves greeter locally (a hard-coded
  one-app wash session).

**Exit criterion:** browser hits `http://localhost:11000/`, sees
login form, posts credentials, gets cookie, sees a "you are authed"
greeter window.

### M3 — Spawn + SCM_RIGHTS handoff + first-class logout

- `/proc` scan for sessions of authed uid.
- `fork → setuid → exec wash-router` with `--listen-unix`.
  Socket-activated.
- Per-uid spawn `flock` to prevent concurrent-first-spawn races.
- `/ws/s/<sessid>/`: hijack the connection *before* WS upgrade,
  read buffered request bytes via `bufio.Reader.Buffered()`,
  dial the per-user router's ctl socket, SCM_RIGHTS the TCP fd +
  replay bytes, close our handle. wash-login is out of the data
  path from this point.
- Single-session auto-attach when count=1, auto-spawn when count=0.
- `/logout?end_session=<sessid>` SIGTERM path.
- `wash-session` adds "Log out" menu item.

**Exit criterion:** browser logs in → handed to a wash-router →
desktop appears → clicks Log out → cookie cleared, router killed,
browser at login screen. Close laptop and reopen: previous session
visible. `lsof` on wash-login shows zero session fds during active
use.

### M4 — Multi-session + picker

- `apps/picker/` — new wash app with `wash_login_rpc` capability.
- `list_sessions`, `spawn_session(name)`, `end_session(sessid)` RPCs
  against wash-login.
- Take-over flow tested across two browser tabs.
- `--max-sessions-per-uid` cap enforced (default 8).
- `/logout?end_all=true` + picker "Log out of all sessions" action.

**Exit criterion:** user can have ≥2 named sessions simultaneously,
switchable via picker, cap enforced, all-logout works.

## Open work items

These are real holes called out during design. Most fold into M2–M4
naturally; this section is the checklist.

1. **HMAC secret rotation policy.** Rotating the key invalidates all
   sessions. Document this.
2. **/etc/shadow group dance.** Document `usermod -aG shadow
   wash-system` in install instructions.
3. **Concurrent first-spawn race.** Per-uid `flock` on
   `/run/wash/spawn-<uid>.lock` around the spawn path. (M3.) The lock
   lives at the run-root (wash-login-owned), not under `/run/wash/<uid>/`,
   because that dir is created later by the target-uid router.
4. **Disabled-shell accounts.** Reject after credential check. (M2.)
5. **Brute-force protection.** Delegate to nginx `limit_req_zone`.
6. **Plaintext-HTTP guard.** Refuse non-loopback bind without
   `--insecure-listen`. (M2.)
7. **Spawn-to-socket race.** Socket activation. (M3.)
8. **Cap behavior.** When `--max-sessions-per-uid` is hit, picker
   "+ new" shows "End an existing session first." (M4.)
9. **Picker capability enforcement.** wash-login refuses
   `list/spawn/end_session` from apps lacking the `wash_login_rpc`
   capability. (M4.)

## Glossary

- **wash-login**: the privileged front-door binary. HTTP + auth +
  supervisor + greeter/picker host + WS proxy.
- **wash-router**: per-user-per-session router process. Sessid-blind;
  sessid lives in its argv only as an identifier for /proc discovery.
- **sessid**: short stable slug (e.g., `s-7f2a`), unique per uid.
  Lives in the ctl socket path. Not user-visible by default.
- **session name**: human-readable label in argv (`--name`).
  Immutable.
- **ctl socket**: `/run/wash/<uid>/sessions/<sessid>.sock`. wash-login
  dials it to deliver a TCP fd via SCM_RIGHTS as a session view.
- **session view**: one attached connection inside a router. Routers
  serve at most one view at a time; take-over evicts the previous.
- **degenerate wash session**: wash-login's internal use of the
  wash-wire dispatcher to host the greeter and picker apps without
  spawning a separate router process.
