package disks

import "testing"

const btrfsCombined = `Label: 'media'  uuid: fade-beef-1111
	Total devices 2 FS bytes used 1610612736
	devid    1 size 2000398934016 used 805306368 path /dev/sdc
	devid    2 size 2000398934016 used 805306368 path /dev/sdd

@@SUBVOL /media
ID 256 gen 12 top level 5 path @snapshots
ID 257 gen 13 top level 5 path @movies
@@STATS /media
[/dev/sdc].write_io_errs    0
[/dev/sdc].read_io_errs     1
[/dev/sdd].write_io_errs    0
[/dev/sdd].read_io_errs     0
`

func TestParseBtrfsShow(t *testing.T) {
	fss := parseBtrfsShow(btrfsCombined) // tolerates trailing marker sections
	if len(fss) != 1 {
		t.Fatalf("fss = %d", len(fss))
	}
	fs := fss[0]
	if fs.Label != "media" || fs.UUID != "fade-beef-1111" {
		t.Fatalf("identity = %+v", fs)
	}
	if fs.Used != 1610612736 {
		t.Fatalf("used = %d", fs.Used)
	}
	if len(fs.Devices) != 2 || fs.Devices[0].Path != "/dev/sdc" || fs.Devices[0].Name != "sdc" {
		t.Fatalf("devices = %+v", fs.Devices)
	}
	if fs.Size != 2*2000398934016 {
		t.Fatalf("size (sum of devices) = %d", fs.Size)
	}
}

func TestParseBtrfsCombined(t *testing.T) {
	mounts := []btrfsMount{{dev: "/dev/sdc", point: "/media"}}
	fss := parseBtrfsCombined(btrfsCombined, mounts)
	if len(fss) != 1 {
		t.Fatalf("fss = %d", len(fss))
	}
	fs := fss[0]
	if fs.Mount != "/media" {
		t.Fatalf("mount = %q", fs.Mount)
	}
	if len(fs.Subvolumes) != 2 || fs.Subvolumes[0].ID != 256 || fs.Subvolumes[0].Path != "@snapshots" {
		t.Fatalf("subvols = %+v", fs.Subvolumes)
	}
	// sdc has a read error from device stats.
	var sdc BtrfsDev
	for _, d := range fs.Devices {
		if d.Name == "sdc" {
			sdc = d
		}
	}
	if sdc.ReadErrs != 1 || sdc.WriteErrs != 0 {
		t.Fatalf("sdc errs = %+v", sdc)
	}
}

func TestParseBtrfsDevStats(t *testing.T) {
	st := parseBtrfsDevStats("[/dev/sdc].write_io_errs 3\n[/dev/sdc].read_io_errs 5\n")
	if st["/dev/sdc"].write != 3 || st["/dev/sdc"].read != 5 {
		t.Fatalf("stats = %+v", st)
	}
}
