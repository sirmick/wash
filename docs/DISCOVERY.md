# wash-discovery — finding hosts on the network

Surface connect **candidates** — wash boxes the user hasn't saved or connected
to yet — so two machines on the same flat subnet find each other with zero
config. This box announces itself, browses the local link for peers doing the
same, and the `wash-connect` window grows an **"On your network"** list you can
Connect to or save with one click.

Status: built and green on `branches/wash-discovery` (uncommitted working tree).
The first provider is mDNS; build is clean and unit tests pass (`go test -short
./apps/remote/be/... ./internal/mdns/...`). See §6 for what's done vs. deferred.

This is the discovery half of the remote story in [REMOTE.md §6.1](REMOTE.md) —
that doc covers *connecting*; this one covers *finding what to connect to*.

---

## 1. Goals & scope

- **Zero-config peer discovery.** Bring up two wash boxes on one subnet and each
  sees the other in `wash-connect` without typing a hostname.
- **Connectable facts only.** A candidate carries an IP and SSH port — enough to
  hand straight to wash-remote's connect flow. We surface an *address*, not just
  a `.local` name, because the IP is the reliable SSH target.
- **A provider seam, not just mDNS.** Discovery is structured so later,
  subnet-crossing sources (a unicast CIDR probe, ssh-config / known_hosts
  import) drop in without touching the FE — every provider emits the same
  `Candidate` and the discoverer aggregates them, tagged by `source`.
- **Best-effort.** If the multicast socket can't open (locked-down network, no
  multicast interface), discovery logs and continues — the rest of wash-remote
  is unaffected.

### Non-goals

- **Cross-subnet / cross-VLAN discovery.** mDNS is link-local (TTL=1) and cannot
  cross a router — by design. That case is the job of the providers still to
  come (§6), not of mDNS.
- **A general zeroconf stack.** The mDNS implementation speaks just enough wire
  protocol for wash-to-wash discovery — no probing/conflict-resolution, no
  reverse lookups, no third-party dependency.
- **MDNS name resolution.** We don't resolve `<host>.local` for the rest of the
  system; we extract the A-record address from the announcement and connect to
  that.

---

## 2. Architecture

Two layers, one new internal package + one provider wired into the existing
`com.wash.remote` background supervisor:

```
  internal/mdns/            self-contained mDNS/DNS-SD responder + browser
        │  Entry{Instance, Host, Addrs, Port, Text}
        ▼
  apps/remote/be/discovery.go    discoverer: providers → Candidate aggregation
        │  State.Candidates []Candidate   (via sdk.StateService)
        ▼
  apps/connect/fe/main.tsx       "On your network" list in wash-connect
```

- **`internal/mdns`** is a reusable, dependency-light library (its only import
  is `golang.org/x/net/dns/dnsmessage` + `.../ipv4`). It opens one multicast
  socket and runs *advertise* and/or *browse* per `Options`.
- **`discovery.go`** lives in the `remote` package (single consumer today — kept
  local per the "no premature service" rule). It owns the provider seam, the
  candidate cache + pruning, and publishing into `State`.
- **The FE** subscribes to the same `StateService` it already uses for connected
  hosts and bookmarks, and renders `state.candidates` as a third section.

### Why a separate `internal/mdns` and not a zeroconf dep

A full zeroconf library carries probing, conflict resolution, reverse lookups,
and a wire surface we don't need — and a third-party dependency in the hot path
of a background service. wash needs exactly: announce one service type, browse
for the same. That's ~490 lines of well-scoped Go (a port of the PTR/SRV/TXT/A
packet dance) with no new module dependency beyond `x/net`, which is already
pulled by other parts of the tree.

---

## 3. The mDNS library (`internal/mdns`)

A `Server` is one open mDNS socket running advertise and/or browse, configured
by `Options`:

| Option | Effect |
|---|---|
| `Advertise *ServiceInfo` | non-nil → respond to `_wash._tcp` queries with this box's records + emit gratuitous announcements |
| `OnEntry func(Entry)` | non-nil → browse: send periodic PTR queries, call `OnEntry` per parsed peer (called from the read goroutine — keep it non-blocking) |
| `QueryInterval` | browse re-query cadence (default 60s) |

**Service type:** wash advertises and browses `_wash._tcp.local.` (DNS-SD over
mDNS). The SRV record carries the **SSH port** a peer connects to (not the mDNS
port); TXT carries `wash=1`; A records carry the box's routable IPv4 addresses.

**Socket coexistence.** The socket binds `:5353` with `SO_REUSEADDR` +
`SO_REUSEPORT` (see `sockopt_linux.go`) so it shares the port with a system
Avahi / mDNSResponder rather than failing to bind. Both responders answering is
fine — mDNS is designed for it. The group `224.0.0.251` is joined on every up,
multicast-capable, non-loopback interface; it's not fatal if some interfaces
refuse the join, as long as one succeeds.

**Self-packet filtering.** The read loop drops packets whose source IP is one of
this box's own addresses, so a box neither answers its own queries nor discovers
itself. (The loopback integration test deliberately disables this to drive both
ends on one host.)

**Announcing** (when `Advertise` is set): one self-contained response packet
carrying PTR (shared, no cache-flush) + SRV/TXT/A (cache-flush bit set, since we
own them), TTL 120s. After start it sends a short burst (per RFC 6762) then goes
quiet, relying on answering subsequent queries.

**Browsing** (when `OnEntry` is set): sends a PTR question for the service, then
re-queries on `QueryInterval`. Responses are assembled by correlating PTR → SRV
/ TXT / A across both Answers and Additionals. Two guards matter:

- **Foreign-service rejection.** A busy LAN multicasts records for printers,
  Spotify, Chromecast, etc. Only instances under `_wash._tcp.local.` are
  emitted; everything else is dropped (covered by `TestForeignServiceIgnored`).
- **PTR-less responses.** Some responders omit the PTR on a directed answer, so
  instances that arrived via SRV/TXT alone are still emitted.

---

## 4. The discoverer (`apps/remote/be/discovery.go`)

```go
type Candidate struct {
    Host   string // friendly display name (".local" trimmed)
    Addr   string // the IP to SSH to — more reliable than a .local name
    Port   int    // omitted on the wire when it's the default 22
    Source string // which provider found it — "mdns" today
    Wash   bool   // it answered on the wash service
}
```

- **Aggregation + dedup.** Candidates are keyed by `source|addr`. Re-seeing one
  refreshes its timestamp; a genuinely new one triggers a republish.
- **Pruning.** A sweep every minute drops candidates not seen within a 5-minute
  TTL (comfortably longer than the 60s mDNS re-query, so a still-present peer
  never flickers out).
- **Stable publishing.** On every change the full set is snapshotted, sorted by
  `(host, addr)`, and written into `State.Candidates` via `StateService.Mutate`
  — the FE re-renders from the same subscription it already has.
- **Lifecycle.** The discoverer runs for the supervisor's (router's) lifetime:
  advertising should persist so peers can find us whenever, and browsing is
  cheap (passive listen + a slow re-query).

### What we advertise — and the docker-bridge trap

`buildAdvertisement()` announces this box as `<hostname>` on the SSH port with
`wash=1`, over its **routable IPv4 addresses**. `routableIPv4()` filters to
non-loopback, non-link-local addresses on real interfaces and **excludes docker
plumbing** (`docker0`, `veth*`, `br-<12hex>`). This matters: docker bridge
addresses like `172.17.0.1` are unreachable from a peer *and every docker host
shares them*, so advertising them would hand out bogus connect targets. wash's
own bridges (`br0`/`br1`) and VLANs (`eth0.10`) are intentionally **not**
excluded. (This mirrors the same-named helper in `apps/session/be/netifaces.go`
— kept local rather than shared, single consumer.)

**Opt-out:** set `WASH_DISCOVERY_NO_ADVERTISE` (any non-empty value) to browse
without announcing. A Settings-UI toggle is a later follow-up; the env var is
the escape hatch until then. If the box has no hostname or no routable address,
it browses but does not advertise.

### Static provider (`WASH_DISCOVERY_STATIC`)

The first cross-subnet provider behind the seam: a comma-separated list of
manually-pinned hosts that mDNS can't reach (it's link-local). Each entry is
`name=addr[:port]` or a bare `addr[:port]`; the default SSH port (22) is elided.
They surface as `source:"static"` candidates and are **sticky** — with no
announcement to refresh them they're exempt from TTL pruning. This env is also
the deterministic hook the e2e drives (`discovery.spec.ts`), since real
multicast needs two hosts. Example:

```
WASH_DISCOVERY_STATIC="lab=10.42.0.9:2222,build-host=10.42.0.20"
```

---

## 5. The frontend — "On your network"

`wash-connect` gains a third list under the connected-hosts and bookmarks
sections, rendered only when there's at least one candidate:

- **Filtered to the genuinely new.** A candidate disappears the moment you act
  on it — `candidateOnly()` excludes anything already connected (by addr or
  origin) or already saved as a bookmark.
- **Each row** shows the friendly name, a dim monospace `addr[:port]`, and a
  cyan **`wash`** chip if the peer announced itself as a wash box (distinct from
  a generic SSH host a future provider might surface).
- **Connect** dials the candidate's `addr` (the reliable IP) under the current
  username — straight into wash-remote's existing connect flow. A non-default
  advertised port rides along as `remote_port`, which the supervisor turns into
  `ssh -p <port>` (a bare positional `host:port` doesn't work for ssh).
- **☆ Save** bookmarks it: the addr is the connect target, the friendly name is
  kept as the bookmark label.

Test hooks: `data-testid="connect-candidates"` (the list),
`connect-candidate-<addr>` (a row), `connect-candidate-connect` / `-save`, and
`connect-candidate-wash` (the chip).

---

## 6. Status & roadmap

**Done (working tree on `branches/wash-discovery`):**

- `internal/mdns` responder + browser, with unit tests: announcement
  round-trip, valid PTR query, foreign-service rejection, name trimming, and a
  real single-host loopback integration test (`-short`-skippable, also skips
  with no multicast interface).
- The mDNS discovery provider: advertise + browse, candidate aggregation,
  dedup, TTL pruning, docker-bridge exclusion, `WASH_DISCOVERY_NO_ADVERTISE`
  opt-out. Unit tests cover observe/dedup, prune, and `Entry`→`Candidate`
  mapping.
- Wired into `com.wash.remote`'s `onReady`, published via `StateService`.
- `wash-connect` "On your network" UI; **Connect honors a non-default advertised
  port** end to end (`remote_port` → `ssh -p`).
- The **`WASH_DISCOVERY_STATIC`** provider (cross-subnet manual pins, sticky).
- **Full-stack e2e** (`e2e/tests/discovery.spec.ts`): a static candidate flows
  BE → FE, renders under "On your network", Connect moves it into the connected
  list, and Save turns it into a bookmark. `ssh -p` is unit-tested in
  `supervisor_test.go`.

**Deferred:**

- **More providers** behind the seam: a unicast CIDR probe and ssh-config /
  known_hosts import (these are what cross subnets, since mDNS can't).
- **Settings toggle** for advertise/browse (env var only for now).
- **Two-host mDNS gate.** A real-multicast Playwright capstone (two VMs on a
  shared L2, following `make e2e-remote-vm`) is still deferred; the e2e above
  drives the static seam, and the loopback integration test exercises real
  multicast on one host.
- **Self-listing on IP change.** The mDNS self-filter snapshots local IPs at
  start; if the box's IP changes mid-session it could briefly discover itself.
- **Commit & merge.** Everything is uncommitted; not yet on `main`.

---

## 7. Files

| Path | Role |
|---|---|
| `internal/mdns/mdns.go` | mDNS/DNS-SD responder + browser |
| `internal/mdns/sockopt_linux.go` | `SO_REUSEADDR`+`SO_REUSEPORT` control fn |
| `internal/mdns/mdns_test.go` | unit tests (round-trip, PTR, foreign-service, trim) |
| `internal/mdns/integration_test.go` | single-host loopback discovery (`-short`-skippable) |
| `apps/remote/be/discovery.go` | provider seam, candidate cache/prune, advertisement build, static provider |
| `apps/remote/be/discovery_test.go` | observe/dedup, prune, mDNS→Candidate mapping, static provider |
| `apps/remote/be/supervisor.go` | `ssh -p` for a non-default connect port |
| `apps/remote/be/app.go` | `State.Candidates` + discoverer start in `onReady` |
| `apps/connect/fe/src/main.tsx` | "On your network" list + `CandidateRow` (sends `remote_port`) |
| `e2e/tests/discovery.spec.ts` | full-stack e2e via the static provider seam |
