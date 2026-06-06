package disks

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// btrfsProvider reads btrfs via the CLI. btrfs-progs JSON support is uneven
// across versions, so we parse text. To keep it to one priv approval the
// several sub-commands are combined behind a single `sh -c` with section
// markers; the parsers below are pure text → struct so they unit-test cleanly.
type btrfsProvider struct{}

func init() { registerProvider(btrfsProvider{}) }

func (btrfsProvider) Name() string     { return "btrfs" }
func (btrfsProvider) Privileged() bool { return true }

// Detect: btrfs tool present AND at least one btrfs mount (cheap, unprivileged).
func (btrfsProvider) Detect() bool {
	return toolOnPath("btrfs") && len(btrfsMounts()) > 0
}

const (
	btrfsMarkSubvol = "@@SUBVOL "
	btrfsMarkStats  = "@@STATS "
)

func (btrfsProvider) Collect(ctx context.Context, run RunFunc) (Manager, bool, error) {
	mounts := btrfsMounts()
	if len(mounts) == 0 {
		return Manager{}, false, nil
	}
	// One combined script: global `filesystem show` then per-mount subvolume
	// list + device stats, each fenced by a marker line.
	var sb strings.Builder
	sb.WriteString("btrfs filesystem show --raw\n")
	for _, m := range mounts {
		sb.WriteString("echo '" + btrfsMarkSubvol + m.point + "'\n")
		sb.WriteString("btrfs subvolume list -- " + shq(m.point) + " 2>/dev/null || true\n")
		sb.WriteString("echo '" + btrfsMarkStats + m.point + "'\n")
		sb.WriteString("btrfs device stats -- " + shq(m.point) + " 2>/dev/null || true\n")
	}
	out, err := run(ctx, []string{"sh", "-c", sb.String()}, "read btrfs filesystems")
	if err != nil {
		return Manager{}, false, err
	}
	fss := parseBtrfsCombined(string(out), mounts)
	if len(fss) == 0 {
		return Manager{}, false, nil
	}
	objs := make([]any, len(fss))
	for i, f := range fss {
		objs[i] = f
	}
	return Manager{Kind: "btrfs", Objects: objs}, true, nil
}

type btrfsMount struct{ dev, point string }

// btrfsMounts returns one entry per btrfs mountpoint (deduped).
func btrfsMounts() []btrfsMount {
	var out []btrfsMount
	seen := map[string]bool{}
	for dev, mi := range readMounts() {
		if mi.fstype != "btrfs" || seen[mi.point] {
			continue
		}
		seen[mi.point] = true
		out = append(out, btrfsMount{dev: dev, point: mi.point})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].point < out[j].point })
	return out
}

// parseBtrfsCombined splits the combined output and assembles per-FS structs,
// attaching each mount's subvolumes + device error counts to the FS whose
// device list contains that mount's device.
func parseBtrfsCombined(text string, mounts []btrfsMount) []BtrfsFS {
	showPart := text
	subvols := map[string][]BtrfsSubvol{} // mountpoint → subvols
	stats := map[string]map[string]devErr{}

	// Split on markers, keeping the leading (pre-marker) show section.
	if i := strings.Index(text, "\n@@"); i >= 0 {
		showPart = text[:i]
	}
	for _, seg := range splitMarkers(text) {
		switch {
		case strings.HasPrefix(seg.mark, btrfsMarkSubvol):
			subvols[strings.TrimPrefix(seg.mark, btrfsMarkSubvol)] = parseBtrfsSubvols(seg.body)
		case strings.HasPrefix(seg.mark, btrfsMarkStats):
			stats[strings.TrimPrefix(seg.mark, btrfsMarkStats)] = parseBtrfsDevStats(seg.body)
		}
	}

	fss := parseBtrfsShow(showPart)
	// Attach mount + subvols + per-device errors.
	devToMount := map[string]string{}
	for _, m := range mounts {
		devToMount[m.dev] = m.point
	}
	for i := range fss {
		for _, d := range fss[i].Devices {
			if mp, ok := devToMount[d.Path]; ok {
				fss[i].Mount = mp
				fss[i].Subvolumes = subvols[mp]
				if st := stats[mp]; st != nil {
					for j := range fss[i].Devices {
						if e, ok := st[fss[i].Devices[j].Path]; ok {
							fss[i].Devices[j].ReadErrs = e.read
							fss[i].Devices[j].WriteErrs = e.write
						}
					}
				}
				break
			}
		}
	}
	return fss
}

// parseBtrfsShow parses `btrfs filesystem show --raw`.
//
//	Label: 'media'  uuid: fade-beef-...
//	        Total devices 2 FS bytes used 1610612736
//	        devid    1 size 2000398934016 used 805306368 path /dev/sdc
func parseBtrfsShow(text string) []BtrfsFS {
	var out []BtrfsFS
	var cur *BtrfsFS
	flush := func() {
		if cur != nil {
			for _, d := range cur.Devices {
				cur.Size += d.Size
			}
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "Label:":
			flush()
			cur = &BtrfsFS{
				Label: strings.Trim(fieldAfter(line, "Label:"), " '"),
				UUID:  fieldAfter(line, "uuid:"),
			}
			if cur.Label == "none" {
				cur.Label = ""
			}
		case cur != nil && strings.HasPrefix(strings.TrimSpace(line), "Total devices"):
			if i := indexField(f, "used"); i >= 0 && i+1 < len(f) {
				cur.Used, _ = strconv.ParseUint(f[i+1], 10, 64)
			}
		case cur != nil && f[0] == "devid":
			d := BtrfsDev{}
			if i := indexField(f, "size"); i >= 0 && i+1 < len(f) {
				d.Size, _ = strconv.ParseUint(f[i+1], 10, 64)
			}
			if i := indexField(f, "used"); i >= 0 && i+1 < len(f) {
				d.Used, _ = strconv.ParseUint(f[i+1], 10, 64)
			}
			if i := indexField(f, "path"); i >= 0 && i+1 < len(f) {
				d.Path = f[i+1]
				d.Name = strings.TrimPrefix(d.Path, "/dev/")
			}
			cur.Devices = append(cur.Devices, d)
		}
	}
	flush()
	return out
}

// parseBtrfsSubvols parses `btrfs subvolume list`:
//
//	ID 256 gen 12 top level 5 path @snapshots
func parseBtrfsSubvols(text string) []BtrfsSubvol {
	var out []BtrfsSubvol
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "ID" {
			continue
		}
		sv := BtrfsSubvol{}
		sv.ID, _ = strconv.Atoi(f[1])
		if i := indexField(f, "path"); i >= 0 && i+1 < len(f) {
			sv.Path = strings.Join(f[i+1:], " ")
		}
		out = append(out, sv)
	}
	return out
}

type devErr struct{ read, write uint64 }

// parseBtrfsDevStats parses `btrfs device stats`:
//
//	[/dev/sdc].write_io_errs   0
//	[/dev/sdc].read_io_errs    2
func parseBtrfsDevStats(text string) map[string]devErr {
	out := map[string]devErr{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.HasPrefix(f[0], "[") {
			continue
		}
		close := strings.IndexByte(f[0], ']')
		if close < 0 {
			continue
		}
		dev := f[0][1:close]
		key := f[0][close+1:] // ".write_io_errs"
		v, _ := strconv.ParseUint(f[1], 10, 64)
		e := out[dev]
		switch {
		case strings.Contains(key, "write_io_errs"):
			e.write = v
		case strings.Contains(key, "read_io_errs"):
			e.read = v
		}
		out[dev] = e
	}
	return out
}

// ---- small text helpers ----

type marked struct{ mark, body string }

// splitMarkers cuts text into segments led by lines starting with "@@".
func splitMarkers(text string) []marked {
	var out []marked
	var cur *marked
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "@@") {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &marked{mark: strings.TrimSpace(line)}
			continue
		}
		if cur != nil {
			cur.body += line + "\n"
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// fieldAfter returns the whitespace-delimited token following key on its line.
func fieldAfter(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := strings.Fields(line[i+len(key):])
	if len(rest) == 0 {
		return ""
	}
	return rest[0]
}

func indexField(fields []string, key string) int {
	for i, f := range fields {
		if f == key {
			return i
		}
	}
	return -1
}

// shq single-quotes a shell argument.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
