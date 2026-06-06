package disks

// Wire payloads. Plain Go scalars + slices + maps — the whole pipeline is
// JSON (the bus marshals via encoding/json, the FE parses the same). Never
// []byte / json.RawMessage for structured fields: the router base64-encodes
// byte strings.

type Snapshot struct {
	Kind         string       `json:"kind"`
	TS           int64        `json:"ts"`
	IntMS        int          `json:"interval_ms"`
	Capabilities Capabilities `json:"capabilities"`
	Disks        []Disk       `json:"disks"`
	// Managers carries logical-storage groups (md, lvm, btrfs, zfs) and is
	// present only for detected managers. Empty on a plain box.
	Managers []Manager `json:"managers"`
}

// Capabilities advertises which managers this host supports, so the FE can
// distinguish "not installed" from "installed but no objects".
type Capabilities struct {
	SMART bool `json:"smart"`
	MD    bool `json:"md"`
	LVM   bool `json:"lvm"`
	Btrfs bool `json:"btrfs"`
	ZFS   bool `json:"zfs"`
}

// Disk is one whole physical block device (sd*, vd*, nvme*, mmcblk*, …).
// ReadBytes/WriteBytes are cumulative /proc/diskstats counters; the FE diffs
// consecutive snapshots to render bytes/s, exactly like wash-top.
type Disk struct {
	Name           string      `json:"name"`   // "sda", "nvme0n1"
	Path           string      `json:"path"`   // "/dev/sda"
	Model          string      `json:"model"`
	Serial         string      `json:"serial"`
	WWN            string      `json:"wwn"`
	Size           uint64      `json:"size"`        // bytes
	Rotational     bool        `json:"rotational"`  // true = HDD, false = SSD/flash
	Removable      bool        `json:"removable"`
	Transport      string      `json:"transport"`   // sata/nvme/usb/virtio/mmc/…
	ReadBytes      uint64      `json:"read_bytes"`  // cumulative
	WriteBytes     uint64      `json:"write_bytes"` // cumulative
	IOInflight     uint64      `json:"io_inflight"`
	SmartSupported bool        `json:"smart_supported"`
	Partitions     []Partition `json:"partitions"`

	// Whole-disk usage: set only when the device has no partition table
	// and is used directly (mkfs on the bare disk, or as a RAID/LVM/ZFS
	// member). FSType/Mount stay empty for a normally-partitioned disk.
	FSType  string   `json:"fstype"`
	Label   string   `json:"label"`
	UUID    string   `json:"uuid"`
	Mount   *Mount   `json:"mount"`
	Holders []string `json:"holders"`
}

// Partition is one slice of a disk. Mount is nil when not mounted.
type Partition struct {
	Name    string   `json:"name"` // "sda1"
	Path    string   `json:"path"` // "/dev/sda1"
	Size    uint64   `json:"size"`
	FSType  string   `json:"fstype"`
	Label   string   `json:"label"`
	UUID    string   `json:"uuid"`
	Mount   *Mount   `json:"mount"`
	Holders []string `json:"holders"` // stacked devices using this part (md0, dm-0, …)
}

// Mount is the live mount + fullness of a partition, from /proc/mounts +
// statfs(2). All byte counts.
type Mount struct {
	Point   string `json:"point"`
	Opts    string `json:"opts"`
	FSUsed  uint64 `json:"fs_used"`
	FSTotal uint64 `json:"fs_total"`
	FSAvail uint64 `json:"fs_avail"`
}

// Manager is a detected logical-storage group. Objects is a heterogeneous
// list whose concrete shape depends on Kind (MDArray for "md", etc.); the
// FE branches on Kind.
type Manager struct {
	Kind    string `json:"kind"` // "md" | "lvm" | "btrfs" | "zfs"
	Objects []any  `json:"objects"`
}

// MDArray is one md (software RAID) array. Read entirely from /proc/mdstat
// and /sys/block/md*/md/ — no root needed.
type MDArray struct {
	Name       string     `json:"name"`        // "md0"
	Path       string     `json:"path"`        // "/dev/md0"
	Level      string     `json:"level"`       // "raid1", "raid5", …
	State      string     `json:"state"`       // "clean", "active", "degraded", …
	Size       uint64     `json:"size"`        // bytes
	RaidDisks  int        `json:"raid_disks"`
	Degraded   bool       `json:"degraded"`
	SyncAction string     `json:"sync_action"` // "idle", "resync", "recover", "check"
	SyncPct    float64    `json:"sync_pct"`    // 0..100, valid while syncing
	Members    []MDMember `json:"members"`
}

// MDMember is one component device of an array.
type MDMember struct {
	Name  string `json:"name"`  // "sda1"
	Slot  int    `json:"slot"`  // role slot, -1 for spare
	State string `json:"state"` // "in_sync", "faulty", "spare", "writemostly"
}

// ---- LVM (M3) ----

// LVMVG is one volume group with its physical volumes and logical volumes.
type LVMVG struct {
	Name string  `json:"name"`
	Size uint64  `json:"size"`
	Free uint64  `json:"free"`
	PVs  []LVMPV `json:"pvs"`
	LVs  []LVMLV `json:"lvs"`
}

type LVMPV struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size uint64 `json:"size"`
	Free uint64 `json:"free"`
}

type LVMLV struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // /dev/<vg>/<lv>
	Size  uint64 `json:"size"`
	Attr  string `json:"attr"` // lvs lv_attr column
	Mount *Mount `json:"mount"`
}

// ---- btrfs (M4) ----

// BtrfsFS is one btrfs filesystem (possibly multi-device) with its
// subvolumes and per-device error counts.
type BtrfsFS struct {
	UUID       string        `json:"uuid"`
	Label      string        `json:"label"`
	Size       uint64        `json:"size"`
	Used       uint64        `json:"used"`
	Mount      string        `json:"mount"`
	Devices    []BtrfsDev    `json:"devices"`
	Subvolumes []BtrfsSubvol `json:"subvolumes"`
}

type BtrfsDev struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      uint64 `json:"size"`
	Used      uint64 `json:"used"`
	ReadErrs  uint64 `json:"read_errs"`
	WriteErrs uint64 `json:"write_errs"`
}

type BtrfsSubvol struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

// ---- ZFS (M5) ----

// ZPool is one zpool with its vdev tree and datasets.
type ZPool struct {
	Name       string     `json:"name"`
	State      string     `json:"state"` // ONLINE/DEGRADED/FAULTED/…
	Size       uint64     `json:"size"`
	Alloc      uint64     `json:"alloc"`
	Free       uint64     `json:"free"`
	Frag       int        `json:"frag"` // percent
	ScanStatus string     `json:"scan_status"`
	Vdevs      []ZVdev    `json:"vdevs"`
	Datasets   []ZDataset `json:"datasets"`
}

type ZVdev struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // disk/mirror/raidz1/…
	State    string  `json:"state"`
	ReadErr  uint64  `json:"read_err"`
	WriteErr uint64  `json:"write_err"`
	CksumErr uint64  `json:"cksum_err"`
	Children []ZVdev `json:"children"`
}

type ZDataset struct {
	Name       string `json:"name"`
	Used       uint64 `json:"used"`
	Avail      uint64 `json:"avail"`
	Refer      uint64 `json:"refer"`
	Mountpoint string `json:"mountpoint"`
}
