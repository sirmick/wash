package disks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sysBlock / procDiskstats / procMounts / devDisk are package vars so tests
// can point them at fixture trees. Production uses the real paths.
var (
	sysBlock      = "/sys/block"
	procDiskstats = "/proc/diskstats"
	procMounts    = "/proc/mounts"
	devDisk       = "/dev/disk"
)

func nowUnix() int64 { return time.Now().Unix() }

// diskPrefixes are the whole physical/virtual disks the user thinks of as
// "a disk". dm-*/md* are logical and surface under their managers; loop/ram
// are noise. Mirrors wash-top's realDisk classification.
var diskPrefixes = []string{"sd", "vd", "hd", "nvme", "mmcblk", "xvd"}

func isPhysicalDisk(name string) bool {
	for _, p := range diskPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// collectDisks walks /sys/block and assembles the physical-disk list with
// partitions, fullness, and cumulative I/O. All unprivileged.
func collectDisks() []Disk {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return nil
	}
	stats := readDiskstats()
	mounts := readMounts()
	uuids := readDevDiskMap("by-uuid")
	labels := readDevDiskMap("by-label")

	out := make([]Disk, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !isPhysicalDisk(name) {
			continue
		}
		d := readDisk(name, stats, mounts, uuids, labels)
		out = append(out, d)
	}
	return out
}

func readDisk(name string, stats map[string]diskstat, mounts map[string]mountInfo, uuids, labels map[string]string) Disk {
	base := filepath.Join(sysBlock, name)
	d := Disk{
		Name:       name,
		Path:       "/dev/" + name,
		Size:       readSectors(filepath.Join(base, "size")),
		Rotational: readBoolFile(filepath.Join(base, "queue", "rotational")),
		Removable:  readBoolFile(filepath.Join(base, "removable")),
		Model:      readTrim(filepath.Join(base, "device", "model")),
		Transport:  inferTransport(name, base),
	}
	d.Serial = firstNonEmpty(
		readTrim(filepath.Join(base, "device", "serial")),
		readTrim(filepath.Join(base, "serial")),
	)
	d.WWN = firstNonEmpty(
		readTrim(filepath.Join(base, "wwid")),
		readTrim(filepath.Join(base, "device", "wwid")),
	)
	if s, ok := stats[name]; ok {
		d.ReadBytes = s.readBytes
		d.WriteBytes = s.writeBytes
		d.IOInflight = s.inflight
	}

	d.Partitions = readPartitions(base, name, stats, mounts, uuids, labels)

	// Whole-disk filesystem / membership: only meaningful when there is no
	// partition table (a bare mkfs, or a RAID/LVM/ZFS member).
	if len(d.Partitions) == 0 {
		d.Holders = readHolders(filepath.Join(base, "holders"))
		d.FSType, d.UUID, d.Label, d.Mount = resolveFS(d.Path, name, mounts, uuids, labels)
	}
	return d
}

func readPartitions(base, disk string, stats map[string]diskstat, mounts map[string]mountInfo, uuids, labels map[string]string) []Partition {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var parts []Partition
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pname := e.Name()
		pdir := filepath.Join(base, pname)
		// A partition subdir has a "partition" file holding its number.
		if _, err := os.Stat(filepath.Join(pdir, "partition")); err != nil {
			continue
		}
		p := Partition{
			Name:    pname,
			Path:    "/dev/" + pname,
			Size:    readSectors(filepath.Join(pdir, "size")),
			Holders: readHolders(filepath.Join(pdir, "holders")),
		}
		p.FSType, p.UUID, p.Label, p.Mount = resolveFS(p.Path, pname, mounts, uuids, labels)
		parts = append(parts, p)
	}
	return parts
}

// resolveFS fills filesystem identity + mount for a device path. fstype/opts
// come from /proc/mounts when mounted; uuid/label from the /dev/disk/by-*
// symlinks (no device read → no root). Unmounted-and-unlabeled devices
// return empty fstype — honest about what we can see without privilege.
func resolveFS(devPath, name string, mounts map[string]mountInfo, uuids, labels map[string]string) (fstype, uuid, label string, mount *Mount) {
	uuid = uuids[name]
	label = labels[name]
	if mi, ok := mounts[devPath]; ok {
		fstype = mi.fstype
		mount = &Mount{Point: mi.point, Opts: mi.opts}
		if total, used, avail, ok := statfsUsage(mi.point); ok {
			mount.FSTotal, mount.FSUsed, mount.FSAvail = total, used, avail
		}
	}
	return fstype, uuid, label, mount
}

// ---- low-level /sys + /proc readers ----

type diskstat struct {
	readBytes, writeBytes, inflight uint64
}

// readDiskstats parses /proc/diskstats into per-device cumulative byte
// counters. Sector fields (6=read, 10=written) are ×512; field 12 is I/O
// in flight. Keyed by device name.
func readDiskstats() map[string]diskstat {
	b, err := os.ReadFile(procDiskstats)
	if err != nil {
		return nil
	}
	out := map[string]diskstat{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 12 {
			continue
		}
		name := f[2]
		readSec, _ := strconv.ParseUint(f[5], 10, 64)
		writeSec, _ := strconv.ParseUint(f[9], 10, 64)
		inflight, _ := strconv.ParseUint(f[11], 10, 64)
		out[name] = diskstat{
			readBytes:  readSec * 512,
			writeBytes: writeSec * 512,
			inflight:   inflight,
		}
	}
	return out
}

type mountInfo struct {
	point, fstype, opts string
}

// readMounts parses /proc/mounts into a device-path → mount map. Only real
// device nodes (/dev/...) are kept; tmpfs/proc/sys/cgroup pseudo-mounts are
// dropped. Octal-escaped spaces in the mountpoint are decoded.
func readMounts() map[string]mountInfo {
	b, err := os.ReadFile(procMounts)
	if err != nil {
		return nil
	}
	out := map[string]mountInfo{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		dev := f[0]
		if !strings.HasPrefix(dev, "/dev/") {
			continue
		}
		// Resolve /dev/disk/by-* and /dev/mapper symlinks would need a
		// readlink; /proc/mounts already gives the canonical /dev/<name>
		// for ordinary partitions, which is what we key on.
		out[dev] = mountInfo{
			point:  unescapeMount(f[1]),
			fstype: f[2],
			opts:   f[3],
		}
	}
	return out
}

// readDevDiskMap reads /dev/disk/<kind>/ symlinks and returns a
// canonical-name → identifier map (e.g. "sda1" → "A1B2-C3D4" for by-uuid).
func readDevDiskMap(kind string) map[string]string {
	dir := filepath.Join(devDisk, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out[filepath.Base(target)] = unescapeMount(e.Name())
	}
	return out
}

// readHolders lists the basenames under a holders/ dir (the stacked devices
// consuming this one: md0, dm-0, …). Empty when nothing is layered on top.
func readHolders(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// inferTransport derives the bus type from the device name and the /sys
// device symlink target (which embeds the controller path).
func inferTransport(name, base string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	case strings.HasPrefix(name, "mmcblk"):
		return "mmc"
	case strings.HasPrefix(name, "xvd"):
		return "xen"
	}
	// sd*/hd*: distinguish USB from SATA via the device symlink path.
	if target, err := os.Readlink(filepath.Join(base, "device")); err == nil {
		if strings.Contains(target, "usb") {
			return "usb"
		}
		if strings.Contains(target, "ata") {
			return "sata"
		}
	}
	return ""
}

func readSectors(path string) uint64 {
	v, _ := strconv.ParseUint(readTrim(path), 10, 64)
	return v * 512
}

func readBoolFile(path string) bool {
	return readTrim(path) == "1"
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// unescapeMount decodes the octal \040-style escapes the kernel uses for
// spaces/tabs/newlines/backslashes in /proc/mounts and by-* link names.
func unescapeMount(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// pseudoFS are kernel/virtual filesystem types excluded from the df-style
// Filesystems list — they aren't storage the user cares about sizing.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "mqueue": true, "debugfs": true, "tracefs": true,
	"securityfs": true, "pstore": true, "bpf": true, "configfs": true, "fusectl": true,
	"hugetlbfs": true, "ramfs": true, "autofs": true, "binfmt_misc": true,
	"rpc_pipefs": true, "nsfs": true, "overlay": true, "squashfs": true,
	"selinuxfs": true, "efivarfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true,
}

// collectFilesystems parses /proc/mounts into the df-style list: every real
// (non-pseudo) mounted filesystem with its statfs fullness. Unprivileged, so
// ZFS datasets / btrfs / LVM volumes all appear in the poll without a scan.
// Deduped by mountpoint (the last mount at a path wins, as df shows).
func collectFilesystems() []Filesystem {
	b, err := os.ReadFile(procMounts)
	if err != nil {
		return nil
	}
	seen := map[string]int{} // mountpoint → index in out
	var out []Filesystem
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		fstype := f[2]
		if pseudoFS[fstype] || strings.HasPrefix(fstype, "fuse.") {
			continue
		}
		fs := Filesystem{
			Source: unescapeMount(f[0]),
			Mount:  unescapeMount(f[1]),
			FSType: fstype,
		}
		if total, used, avail, ok := statfsUsage(fs.Mount); ok {
			// Skip zero-size mounts (e.g. empty autofs-style entries).
			if total == 0 {
				continue
			}
			fs.Total, fs.Used, fs.Avail = total, used, avail
		}
		if i, dup := seen[fs.Mount]; dup {
			out[i] = fs
			continue
		}
		seen[fs.Mount] = len(out)
		out = append(out, fs)
	}
	return out
}

// statfsUsage returns total/used/avail bytes for a mountpoint via statfs(2).
// Used = total - free (free includes root-reserved space; avail excludes it).
func statfsUsage(point string) (total, used, avail uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(point, &st); err != nil {
		return 0, 0, 0, false
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	avail = st.Bavail * bs
	free := st.Bfree * bs
	used = total - free
	return total, used, avail, true
}
