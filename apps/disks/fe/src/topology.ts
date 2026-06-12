// Pure tree-builder: turns a Snapshot into a flat list of selectable rows
// for the left panel (depth encodes nesting). Kept framework-free so it can
// be unit-tested under `node --test`. The component renders each row by kind
// and formats sizes; this module owns only the structure + stable ids.

import type {
  Snapshot,
  Disk,
  Partition,
  Filesystem,
  Manager,
  MDArray,
  LVMVG,
  LVMLV,
  BtrfsFS,
  BtrfsSubvol,
  ZPool,
  ZVdev,
  ZDataset,
} from './types.ts';

export type RowKind =
  | 'section'
  | 'fs'
  | 'disk'
  | 'part'
  | 'md'
  | 'lvm-vg'
  | 'lvm-lv'
  | 'btrfs'
  | 'btrfs-subvol'
  | 'zpool'
  | 'zvdev'
  | 'zdataset';

export interface Row {
  id: string;
  depth: number;
  kind: RowKind;
  name: string;
  data: unknown;
  selectable: boolean;
}

function section(id: string, name: string): Row {
  return { id, depth: 0, kind: 'section', name, data: null, selectable: false };
}

export function buildRows(snap: Snapshot | null): Row[] {
  if (!snap) return [];
  const rows: Row[] = [];

  // Filesystems first — the df-style list (incl. ZFS datasets, btrfs, LVM
  // volumes), populated unprivileged from /proc/mounts + statfs.
  const filesystems = snap.filesystems ?? [];
  if (filesystems.length > 0) {
    rows.push(section('sec-fs', 'Filesystems'));
    for (const fs of filesystems) {
      rows.push({ id: `fs:${fs.mount}`, depth: 1, kind: 'fs', name: fs.mount, data: fs, selectable: true });
    }
  }

  const disks = snap.disks ?? [];
  rows.push(section('sec-disks', 'Disks'));
  for (const d of disks) {
    rows.push({ id: `disk:${d.name}`, depth: 1, kind: 'disk', name: d.name, data: d, selectable: true });
    for (const p of d.partitions ?? []) {
      rows.push({ id: `part:${p.name}`, depth: 2, kind: 'part', name: p.name, data: p, selectable: true });
    }
  }

  const managers = snap.managers ?? [];
  if (managers.length > 0) {
    rows.push(section('sec-vol', 'Volumes'));
    for (const m of managers) {
      appendManager(rows, m);
    }
  }
  return rows;
}

function appendManager(rows: Row[], m: Manager): void {
  switch (m.kind) {
    case 'md':
      for (const a of (m.objects ?? []) as MDArray[]) {
        rows.push({ id: `md:${a.name}`, depth: 1, kind: 'md', name: a.name, data: a, selectable: true });
      }
      break;
    case 'lvm':
      for (const vg of (m.objects ?? []) as LVMVG[]) {
        rows.push({ id: `vg:${vg.name}`, depth: 1, kind: 'lvm-vg', name: vg.name, data: vg, selectable: true });
        for (const lv of vg.lvs ?? []) {
          rows.push({ id: `lv:${vg.name}/${lv.name}`, depth: 2, kind: 'lvm-lv', name: lv.name, data: lv, selectable: true });
        }
      }
      break;
    case 'btrfs':
      for (const fs of (m.objects ?? []) as BtrfsFS[]) {
        const label = fs.label || fs.uuid;
        rows.push({ id: `btrfs:${fs.uuid}`, depth: 1, kind: 'btrfs', name: label, data: fs, selectable: true });
        for (const sv of fs.subvolumes ?? []) {
          rows.push({ id: `subvol:${fs.uuid}:${sv.id}`, depth: 2, kind: 'btrfs-subvol', name: sv.path, data: sv, selectable: true });
        }
      }
      break;
    case 'zfs':
      for (const pool of (m.objects ?? []) as ZPool[]) {
        rows.push({ id: `pool:${pool.name}`, depth: 1, kind: 'zpool', name: pool.name, data: pool, selectable: true });
        for (const v of pool.vdevs ?? []) {
          appendVdev(rows, v, 2);
        }
        for (const ds of pool.datasets ?? []) {
          rows.push({ id: `ds:${ds.name}`, depth: 2, kind: 'zdataset', name: ds.name, data: ds, selectable: true });
        }
      }
      break;
  }
}

function appendVdev(rows: Row[], v: ZVdev, depth: number): void {
  rows.push({ id: `vdev:${v.name}`, depth, kind: 'zvdev', name: v.name, data: v, selectable: true });
  for (const c of v.children ?? []) {
    appendVdev(rows, c, depth + 1);
  }
}

export function findRow(rows: Row[], id: string | null): Row | undefined {
  if (!id) return undefined;
  return rows.find((r) => r.id === id);
}

// usagePct returns 0..100 for a used/total pair, clamped. total<=0 → 0.
export function usagePct(used: number, total: number): number {
  if (total <= 0) return 0;
  return Math.max(0, Math.min(100, (used / total) * 100));
}

// Re-exported for the detail pane's narrowing convenience.
export type { Disk, Partition, Filesystem, MDArray, LVMVG, LVMLV, BtrfsFS, BtrfsSubvol, ZPool, ZVdev, ZDataset };
