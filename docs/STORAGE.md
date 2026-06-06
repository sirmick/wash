# wash-disks — storage information app

`wash-disks` (id `com.wash.disks`, "Disks") is the storage manager for the
wash desktop: connected disks, their status, fullness, live I/O, SMART health,
and the logical-storage layers stacked on top — md (software RAID), LVM, btrfs,
and ZFS.

It is built on the `wash-top` pattern: one Go BE per window
(`instancing=multi`) that polls and pushes JSON snapshots to a Solid FE, pausing
while the window is minimized. The launcher discovers it automatically via the
manifest catalog.

## Trust tiers

The design splits cleanly by privilege, and that split drives everything:

- **Unprivileged — the always-on poll.** Physical block devices, partitions,
  mounts, fullness, I/O, and **md** are all readable without root from
  `/sys/block`, `/proc/mounts`, `statfs(2)`, `/proc/diskstats`, and
  `/proc/mdstat`. This is the M1 core (`collect.go`, `md.go`).
- **Privileged — on demand only.** SMART, LVM, btrfs, and ZFS need root
  (raw-device / `/dev/zfs` / lvm-report access). They run through the SDK's
  `conn.PrivRunInlineSync()` (the same wash-priv path `wash-netd` uses for
  `washnet-read`), triggered by an explicit user action — never in the poll
  loop, so a background Disks window never causes a recurring sudo prompt.

## Architecture

```
apps/disks/be/
  app.go        manifest, snapshot loop, pause-on-minimize, handlers,
                --dump-snapshot one-shot CLI mode
  collect.go    physical layer: /sys/block + /proc/mounts + statfs + diskstats
  provider.go   Provider interface, registry, capability detection, runners
  md.go         md provider (/proc/mdstat + /sys/block/md*/md/), unprivileged
  fake.go       WASH_DISKS_SOURCE=fake snapshot (all manager kinds) for e2e
  types.go      wire payloads (JSON; no []byte per the CBOR/JSON pitfall)
  cmd/main.go   standalone shim
apps/disks/fe/src/
  types.ts      wire mirror
  topology.ts   pure snapshot → selectable-row tree (unit-tested, node --test)
  main.tsx      left tree (Disks + Volumes) + right detail pane, fullness bars,
                I/O sparklines
```

Each logical manager is a `Provider` (md/lvm/btrfs/zfs) and a **detected
capability** — it appears only when present on the host. The wire `Manager`
carries a `kind` and a heterogeneous `objects` list; the FE branches on kind.

### `--dump-snapshot`

`wash-disks --dump-snapshot` runs every collector + provider once and prints the
snapshot JSON, with no router/priv/UI. Run as root it shells the privileged
tools directly. This is the seam the real-kernel VM tests use to assert the
parsers against actual md/lvm/btrfs/zfs, and a handy debugging tool.

## Roadmap

- **M1 — core (done):** physical disks + partitions + mounts + fullness + live
  I/O + md + topology FE + fake source + Tier 1–3 tests + build/packaging wiring.
  Fully unprivileged; no image-package or wash-priv dependency.
- **M2 — SMART:** add `smartmontools` to the image; on-demand `smartctl -aj`
  through wash-priv; health badge + attribute table per disk.
- **M3 — LVM:** `pvs/vgs/lvs --reportformat json` → PV→VG→LV.
- **M4 — btrfs:** `btrfs filesystem show/usage`, `subvolume list`,
  `device stats` → multi-device fs + subvolumes + per-device errors.
- **M5 — ZFS:** `zpool list/status`, `zfs list` → pools, vdev tree +
  scrub/resilver + per-device errors, datasets.

M3–M5 are thin: one Provider + one parser + FE rendering, all on the M2
privileged-probe path. The `Provider` interface accommodates a future dmcrypt
provider as a drop-in.

## Testing

Coverage is layered (see the plan for the full strategy):

1. **Tier 1 — provider parser unit tests** (`go test ./apps/disks/...`): table
   tests against captured real tool/file output.
2. **Tier 2 — FE topology test** (`node --test`): `topology.ts` against a
   synthetic snapshot covering every manager kind.
3. **Tier 3 — full-stack UI e2e** (`e2e/tests/disks.spec.ts`): launched with
   `WASH_DISKS_SOURCE=fake` so every section renders deterministically without
   root.
4. **Tier 4 — real-kernel VM gate** (planned): boots a microVM with real tools +
   virtio scratch disks, builds actual md/lvm/btrfs/zfs, and asserts the
   providers via `wash-disks --dump-snapshot`. ZFS is opt-in (out-of-tree kmod).
