import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildRows, findRow, usagePct } from './topology.ts';
import type { Snapshot } from './types.ts';

// A compact snapshot exercising every row kind. Mirrors the BE fake source
// shape closely enough to validate the structure + ids.
function sampleSnapshot(): Snapshot {
  return {
    kind: 'snapshot',
    ts: 1,
    interval_ms: 3000,
    capabilities: { smart: true, md: true, lvm: true, btrfs: true, zfs: true },
    filesystems: [
      { source: '/dev/sda1', mount: '/', fstype: 'ext4', used: 200, total: 500, avail: 300 },
      { source: 'tank/ds', mount: '/tank/ds', fstype: 'zfs', used: 290, total: 790, avail: 500 },
    ],
    disks: [
      {
        name: 'sda', path: '/dev/sda', model: 'X', serial: '', wwn: '',
        size: 1000, rotational: true, removable: false, transport: 'sata',
        read_bytes: 0, write_bytes: 0, io_inflight: 0, smart_supported: true,
        fstype: '', label: '', uuid: '', mount: null, holders: null,
        partitions: [
          { name: 'sda1', path: '/dev/sda1', size: 500, fstype: 'ext4', label: '', uuid: '', mount: null, holders: null },
          { name: 'sda2', path: '/dev/sda2', size: 500, fstype: '', label: '', uuid: '', mount: null, holders: ['md0'] },
        ],
      },
    ],
    managers: [
      { kind: 'md', objects: [{ name: 'md0', path: '/dev/md0', level: 'raid1', state: 'clean', size: 500, raid_disks: 2, degraded: false, sync_action: 'idle', sync_pct: 0, members: [] }] },
      { kind: 'lvm', objects: [{ name: 'vg0', size: 500, free: 100, pvs: [], lvs: [{ name: 'home', path: '/dev/vg0/home', size: 400, attr: '', mount: null }] }] },
      { kind: 'btrfs', objects: [{ uuid: 'u1', label: 'media', size: 100, used: 10, mount: '/m', devices: [], subvolumes: [{ id: 256, path: '@snap' }] }] },
      { kind: 'zfs', objects: [{ name: 'tank', state: 'ONLINE', size: 800, alloc: 300, free: 500, frag: 1, scan_status: '', vdevs: [{ name: 'mirror-0', type: 'mirror', state: 'ONLINE', read_err: 0, write_err: 0, cksum_err: 0, children: [{ name: 'sde', type: 'disk', state: 'ONLINE', read_err: 0, write_err: 0, cksum_err: 0, children: null }] }], datasets: [{ name: 'tank/ds', used: 290, avail: 500, refer: 290, mountpoint: '/tank/ds' }] }] },
    ],
  };
}

test('buildRows: null snapshot → empty', () => {
  assert.deepEqual(buildRows(null), []);
});

test('buildRows: filesystem rows incl. zfs dataset', () => {
  const rows = buildRows(sampleSnapshot());
  const byId = (id: string) => rows.find((r) => r.id === id);
  assert.ok(byId('sec-fs'), 'Filesystems section present');
  const zfs = byId('fs:/tank/ds')!;
  assert.equal(zfs.kind, 'fs');
  assert.equal((zfs.data as any).fstype, 'zfs');
});

test('buildRows: sections + disk/partition nesting', () => {
  const rows = buildRows(sampleSnapshot());
  const byId = (id: string) => rows.find((r) => r.id === id);

  assert.ok(byId('sec-disks'), 'Disks section present');
  assert.ok(byId('sec-vol'), 'Volumes section present');

  const disk = byId('disk:sda')!;
  assert.equal(disk.depth, 1);
  assert.equal(disk.kind, 'disk');

  const p1 = byId('part:sda1')!;
  assert.equal(p1.depth, 2);
  assert.equal(p1.kind, 'part');
});

test('buildRows: every manager kind yields rows', () => {
  const rows = buildRows(sampleSnapshot());
  const kinds = new Set(rows.map((r) => r.kind));
  for (const k of ['md', 'lvm-vg', 'lvm-lv', 'btrfs', 'btrfs-subvol', 'zpool', 'zvdev', 'zdataset']) {
    assert.ok(kinds.has(k as any), `missing row kind ${k}`);
  }
});

test('buildRows: nested zfs vdev gets deeper depth', () => {
  const rows = buildRows(sampleSnapshot());
  const mirror = rows.find((r) => r.id === 'vdev:mirror-0')!;
  const child = rows.find((r) => r.id === 'vdev:sde')!;
  assert.equal(mirror.depth, 2);
  assert.equal(child.depth, 3);
});

test('buildRows: ids are unique', () => {
  const rows = buildRows(sampleSnapshot());
  const ids = rows.map((r) => r.id);
  assert.equal(ids.length, new Set(ids).size);
});

test('findRow + usagePct', () => {
  const rows = buildRows(sampleSnapshot());
  assert.equal(findRow(rows, 'disk:sda')?.name, 'sda');
  assert.equal(findRow(rows, 'nope'), undefined);
  assert.equal(findRow(rows, null), undefined);

  assert.equal(usagePct(50, 100), 50);
  assert.equal(usagePct(0, 0), 0);
  assert.equal(usagePct(200, 100), 100);
});
