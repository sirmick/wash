package disks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDiskstats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diskstats")
	// Two whole-disk lines + one partition. Fields: major minor name
	// reads merged read_sectors read_ms writes merged write_sectors
	// write_ms ios_in_flight io_ms ...
	content := "" +
		"   8       0 sda 100 0 2000 50 200 0 4000 80 1 300 0\n" +
		"   8       1 sda1 10 0 40 5 20 0 80 8 0 30 0\n" +
		" 259       0 nvme0n1 5 0 16 2 5 0 24 3 3 20 0\n"
	mustWrite(t, path, content)
	defer swap(&procDiskstats, path)()

	stats := readDiskstats()
	if got := stats["sda"]; got.readBytes != 2000*512 || got.writeBytes != 4000*512 || got.inflight != 1 {
		t.Fatalf("sda = %+v", got)
	}
	if got := stats["nvme0n1"]; got.readBytes != 16*512 || got.writeBytes != 24*512 || got.inflight != 3 {
		t.Fatalf("nvme0n1 = %+v", got)
	}
}

func TestReadMounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mounts")
	content := "" +
		"/dev/sda1 / ext4 rw,relatime 0 0\n" +
		"proc /proc proc rw,nosuid 0 0\n" +
		"tmpfs /run tmpfs rw 0 0\n" +
		"/dev/sdb1 /mnt/my\\040disk xfs rw 0 0\n"
	mustWrite(t, path, content)
	defer swap(&procMounts, path)()

	m := readMounts()
	if len(m) != 2 {
		t.Fatalf("want 2 real-device mounts, got %d: %+v", len(m), m)
	}
	if m["/dev/sda1"].point != "/" || m["/dev/sda1"].fstype != "ext4" {
		t.Fatalf("sda1 = %+v", m["/dev/sda1"])
	}
	if m["/dev/sdb1"].point != "/mnt/my disk" {
		t.Fatalf("octal-escape not decoded: %q", m["/dev/sdb1"].point)
	}
}

func TestUnescapeMount(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"a\\040b":        "a b",
		"tab\\011here":   "tab\there",
		"back\\134slash": "back\\slash",
		"\\040lead":      " lead",
	}
	for in, want := range cases {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectFilesystems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mounts")
	// Real filesystems (incl. zfs datasets + an LVM mapper) + pseudo mounts.
	content := "" +
		"/dev/sda1 / ext4 rw,relatime 0 0\n" +
		"proc /proc proc rw 0 0\n" +
		"tmpfs /run tmpfs rw 0 0\n" +
		"tank /tank zfs rw 0 0\n" +
		"tank/ds /tank/ds zfs rw 0 0\n" +
		"/dev/mapper/vg0-home /home ext4 rw 0 0\n" +
		"cgroup2 /sys/fs/cgroup cgroup2 rw 0 0\n"
	mustWrite(t, path, content)
	defer swap(&procMounts, path)()

	fss := collectFilesystems()
	mounts := map[string]string{} // mount → fstype
	for _, fs := range fss {
		mounts[fs.Mount] = fs.FSType
	}
	// Real filesystems kept (statfs fails on fake paths → zero usage, still listed).
	for _, m := range []string{"/", "/tank", "/tank/ds", "/home"} {
		if _, ok := mounts[m]; !ok {
			t.Errorf("missing filesystem %s; got %v", m, mounts)
		}
	}
	// Pseudo filesystems excluded.
	for _, m := range []string{"/proc", "/run", "/sys/fs/cgroup"} {
		if _, ok := mounts[m]; ok {
			t.Errorf("pseudo fs %s should be excluded", m)
		}
	}
	if mounts["/tank/ds"] != "zfs" {
		t.Errorf("zfs dataset not typed zfs: %v", mounts)
	}
}

// TestCollectDisks builds a synthetic /sys/block tree and asserts the disk +
// partition assembly, holders, and the whole-disk-vs-partition split.
func TestCollectDisks(t *testing.T) {
	root := t.TempDir()
	sb := filepath.Join(root, "sys", "block")

	// sda: partitioned SATA HDD, sda1 mounted at /, sda2 is an md member.
	mkDisk(t, sb, "sda", diskFiles{size: "3907029168", rotational: "1", removable: "0", model: "Fake HDD"})
	mkPart(t, sb, "sda", "sda1", "1024000", nil)
	mkPart(t, sb, "sda", "sda2", "3905980000", []string{"md0"})
	// vdb: a bare virtio disk used directly (no partitions, is a holder base).
	mkDisk(t, sb, "vdb", diskFiles{size: "2097152", rotational: "0", removable: "0", model: "Scratch"})
	mkHolders(t, filepath.Join(sb, "vdb", "holders"), []string{"md0"})
	// loop0 + dm-0 must be ignored (not physical disks).
	mkDisk(t, sb, "loop0", diskFiles{size: "100"})
	mkDisk(t, sb, "dm-0", diskFiles{size: "100"})

	defer swap(&sysBlock, sb)()
	// Empty diskstats/mounts/by-* — exercise the no-data paths.
	defer swap(&procDiskstats, filepath.Join(root, "none"))()
	defer swap(&procMounts, filepath.Join(root, "none"))()
	defer swap(&devDisk, filepath.Join(root, "none"))()

	disks := collectDisks()
	byName := map[string]Disk{}
	for _, d := range disks {
		byName[d.Name] = d
	}
	if _, ok := byName["loop0"]; ok {
		t.Error("loop0 should be excluded")
	}
	if _, ok := byName["dm-0"]; ok {
		t.Error("dm-0 should be excluded")
	}
	sda, ok := byName["sda"]
	if !ok {
		t.Fatal("sda missing")
	}
	if sda.Size != 3907029168*512 || !sda.Rotational || sda.Transport != "" || sda.Model != "Fake HDD" {
		t.Fatalf("sda = %+v", sda)
	}
	if len(sda.Partitions) != 2 {
		t.Fatalf("sda parts = %d", len(sda.Partitions))
	}
	// sda2 carries the md0 holder.
	var sda2 Partition
	for _, p := range sda.Partitions {
		if p.Name == "sda2" {
			sda2 = p
		}
	}
	if len(sda2.Holders) != 1 || sda2.Holders[0] != "md0" {
		t.Fatalf("sda2 holders = %v", sda2.Holders)
	}
	vdb := byName["vdb"]
	if vdb.Transport != "virtio" || len(vdb.Partitions) != 0 {
		t.Fatalf("vdb = %+v", vdb)
	}
	// Whole-disk holders populated since vdb has no partitions.
	if len(vdb.Holders) != 1 || vdb.Holders[0] != "md0" {
		t.Fatalf("vdb holders = %v", vdb.Holders)
	}
}

// ---- test helpers ----

type diskFiles struct {
	size, rotational, removable, model string
}

func mkDisk(t *testing.T, sb, name string, f diskFiles) {
	t.Helper()
	base := filepath.Join(sb, name)
	mustMkdir(t, filepath.Join(base, "queue"))
	mustMkdir(t, filepath.Join(base, "device"))
	mustWrite(t, filepath.Join(base, "size"), f.size+"\n")
	if f.rotational != "" {
		mustWrite(t, filepath.Join(base, "queue", "rotational"), f.rotational+"\n")
	}
	if f.removable != "" {
		mustWrite(t, filepath.Join(base, "removable"), f.removable+"\n")
	}
	if f.model != "" {
		mustWrite(t, filepath.Join(base, "device", "model"), f.model+"\n")
	}
}

func mkPart(t *testing.T, sb, disk, part, size string, holders []string) {
	t.Helper()
	base := filepath.Join(sb, disk, part)
	mustMkdir(t, base)
	mustWrite(t, filepath.Join(base, "partition"), "1\n")
	mustWrite(t, filepath.Join(base, "size"), size+"\n")
	mkHolders(t, filepath.Join(base, "holders"), holders)
}

func mkHolders(t *testing.T, dir string, names []string) {
	t.Helper()
	mustMkdir(t, dir)
	for _, n := range names {
		mustMkdir(t, filepath.Join(dir, n))
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// swap sets *p = v and returns a restore func for defer.
func swap[T any](p *T, v T) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
