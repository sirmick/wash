// Wire types — mirror apps/disks/be/types.go. The whole pipeline is JSON.

export interface Mount {
  point: string;
  opts: string;
  fs_used: number;
  fs_total: number;
  fs_avail: number;
}

export interface Partition {
  name: string;
  path: string;
  size: number;
  fstype: string;
  label: string;
  uuid: string;
  mount: Mount | null;
  holders: string[] | null;
}

export interface Disk {
  name: string;
  path: string;
  model: string;
  serial: string;
  wwn: string;
  size: number;
  rotational: boolean;
  removable: boolean;
  transport: string;
  read_bytes: number;
  write_bytes: number;
  io_inflight: number;
  smart_supported: boolean;
  partitions: Partition[] | null;
  fstype: string;
  label: string;
  uuid: string;
  mount: Mount | null;
  holders: string[] | null;
}

export interface Capabilities {
  smart: boolean;
  md: boolean;
  lvm: boolean;
  btrfs: boolean;
  zfs: boolean;
}

export interface Manager {
  kind: 'md' | 'lvm' | 'btrfs' | 'zfs';
  objects: unknown[] | null;
}

export interface Snapshot {
  kind: 'snapshot';
  ts: number;
  interval_ms: number;
  capabilities: Capabilities;
  disks: Disk[] | null;
  managers: Manager[] | null;
}

// ---- manager object shapes ----

export interface MDMember {
  name: string;
  slot: number;
  state: string;
}
export interface MDArray {
  name: string;
  path: string;
  level: string;
  state: string;
  size: number;
  raid_disks: number;
  degraded: boolean;
  sync_action: string;
  sync_pct: number;
  members: MDMember[] | null;
}

export interface LVMPV {
  name: string;
  path: string;
  size: number;
  free: number;
}
export interface LVMLV {
  name: string;
  path: string;
  size: number;
  attr: string;
  mount: Mount | null;
}
export interface LVMVG {
  name: string;
  size: number;
  free: number;
  pvs: LVMPV[] | null;
  lvs: LVMLV[] | null;
}

export interface BtrfsDev {
  name: string;
  path: string;
  size: number;
  used: number;
  read_errs: number;
  write_errs: number;
}
export interface BtrfsSubvol {
  id: number;
  path: string;
}
export interface BtrfsFS {
  uuid: string;
  label: string;
  size: number;
  used: number;
  mount: string;
  devices: BtrfsDev[] | null;
  subvolumes: BtrfsSubvol[] | null;
}

export interface ZVdev {
  name: string;
  type: string;
  state: string;
  read_err: number;
  write_err: number;
  cksum_err: number;
  children: ZVdev[] | null;
}
export interface ZDataset {
  name: string;
  used: number;
  avail: number;
  refer: number;
  mountpoint: string;
}
// ---- SMART (M2) ----

export interface SmartAttr {
  id: number;
  name: string;
  value: number;
  worst: number;
  thresh: number;
  raw: string;
  when_failed: string;
}
export interface SmartReport {
  name: string;
  passed: boolean;
  have_status: boolean;
  model: string;
  serial: string;
  firmware: string;
  temp_c: number;
  power_on_hours: number;
  power_cycles: number;
  attrs: SmartAttr[] | null;
}

export interface ZPool {
  name: string;
  state: string;
  size: number;
  alloc: number;
  free: number;
  frag: number;
  scan_status: string;
  vdevs: ZVdev[] | null;
  datasets: ZDataset[] | null;
}
