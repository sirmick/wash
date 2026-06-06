package disks

// fakeSnapshot returns a deterministic snapshot covering every disk and
// manager shape, used when WASH_DISKS_SOURCE=fake. It lets the Tier-3 UI
// e2e render and assert every section without touching real subsystems or
// requiring root. Keep it broad: two disks (one SSD/nvme, one HDD/sata),
// partitions with and without mounts, plus one of each manager type.
func fakeSnapshot() Snapshot {
	const gb = uint64(1) << 30

	nvme := Disk{
		Name: "nvme0n1", Path: "/dev/nvme0n1",
		Model: "WASH FakeSSD 512G", Serial: "FAKE-NVME-0001", WWN: "eui.000fake0001",
		Size: 512 * gb, Rotational: false, Removable: false, Transport: "nvme",
		ReadBytes: 1_200_000_000, WriteBytes: 800_000_000, IOInflight: 0,
		SmartSupported: true,
		Partitions: []Partition{
			{Name: "nvme0n1p1", Path: "/dev/nvme0n1p1", Size: 512 * (1 << 20),
				FSType: "vfat", Label: "EFI", UUID: "1234-ABCD",
				Mount: &Mount{Point: "/boot/efi", Opts: "rw,relatime", FSTotal: 511 * (1 << 20), FSUsed: 60 * (1 << 20), FSAvail: 451 * (1 << 20)}},
			{Name: "nvme0n1p2", Path: "/dev/nvme0n1p2", Size: 511 * gb,
				FSType: "ext4", Label: "root", UUID: "aaaa-bbbb-cccc-dddd",
				Mount: &Mount{Point: "/", Opts: "rw,relatime", FSTotal: 500 * gb, FSUsed: 210 * gb, FSAvail: 290 * gb}},
		},
	}

	hdd := Disk{
		Name: "sda", Path: "/dev/sda",
		Model: "WASH FakeHDD 2T", Serial: "FAKE-SATA-0007", WWN: "0xfakewwn7",
		Size: 2000 * gb, Rotational: true, Removable: false, Transport: "sata",
		ReadBytes: 40_000_000_000, WriteBytes: 12_000_000_000, IOInflight: 2,
		SmartSupported: true,
		Partitions: []Partition{
			// Unmounted partition — exercises the no-mount path.
			{Name: "sda1", Path: "/dev/sda1", Size: 100 * gb, FSType: "ntfs", Label: "Backup", UUID: "11aa22bb"},
			// RAID member — shows a holder, no mount.
			{Name: "sda2", Path: "/dev/sda2", Size: 1900 * gb, Holders: []string{"md0"}},
		},
	}

	mdMgr := Manager{Kind: "md", Objects: []any{
		MDArray{
			Name: "md0", Path: "/dev/md0", Level: "raid1", State: "clean",
			Size: 1900 * gb, RaidDisks: 2, Degraded: false, SyncAction: "idle",
			Members: []MDMember{
				{Name: "sda2", Slot: 0, State: "in_sync"},
				{Name: "sdb2", Slot: 1, State: "in_sync"},
			},
		},
	}}

	lvmMgr := Manager{Kind: "lvm", Objects: []any{
		LVMVG{
			Name: "vg0", Size: 1900 * gb, Free: 400 * gb,
			PVs: []LVMPV{{Name: "md0", Path: "/dev/md0", Size: 1900 * gb, Free: 400 * gb}},
			LVs: []LVMLV{
				{Name: "home", Path: "/dev/vg0/home", Size: 1000 * gb, Attr: "-wi-ao----",
					Mount: &Mount{Point: "/home", Opts: "rw,relatime", FSTotal: 1000 * gb, FSUsed: 620 * gb, FSAvail: 380 * gb}},
				{Name: "data", Path: "/dev/vg0/data", Size: 500 * gb, Attr: "-wi-ao----",
					Mount: &Mount{Point: "/srv", Opts: "rw,relatime", FSTotal: 500 * gb, FSUsed: 50 * gb, FSAvail: 450 * gb}},
			},
		},
	}}

	btrfsMgr := Manager{Kind: "btrfs", Objects: []any{
		BtrfsFS{
			UUID: "fade-beef", Label: "media", Size: 4000 * gb, Used: 1500 * gb, Mount: "/media",
			Devices: []BtrfsDev{
				{Name: "sdc", Path: "/dev/sdc", Size: 2000 * gb, Used: 750 * gb},
				{Name: "sdd", Path: "/dev/sdd", Size: 2000 * gb, Used: 750 * gb},
			},
			Subvolumes: []BtrfsSubvol{
				{ID: 256, Path: "@snapshots"},
				{ID: 257, Path: "@movies"},
			},
		},
	}}

	zfsMgr := Manager{Kind: "zfs", Objects: []any{
		ZPool{
			Name: "tank", State: "ONLINE", Size: 8000 * gb, Alloc: 3000 * gb, Free: 5000 * gb,
			Frag: 7, ScanStatus: "scrub repaired 0B in 02:14:05 with 0 errors",
			Vdevs: []ZVdev{
				{Name: "mirror-0", Type: "mirror", State: "ONLINE", Children: []ZVdev{
					{Name: "sde", Type: "disk", State: "ONLINE"},
					{Name: "sdf", Type: "disk", State: "ONLINE"},
				}},
			},
			Datasets: []ZDataset{
				{Name: "tank", Used: 3000 * gb, Avail: 5000 * gb, Refer: 96 * (1 << 10), Mountpoint: "/tank"},
				{Name: "tank/ds", Used: 2900 * gb, Avail: 5000 * gb, Refer: 2900 * gb, Mountpoint: "/tank/ds"},
			},
		},
	}}

	return Snapshot{
		TS:           1_700_000_000,
		Capabilities: Capabilities{SMART: true, MD: true, LVM: true, Btrfs: true, ZFS: true},
		Disks:        []Disk{nvme, hdd},
		Managers:     []Manager{mdMgr, lvmMgr, btrfsMgr, zfsMgr},
	}
}
