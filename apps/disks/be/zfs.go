package disks

import (
	"context"
	"strconv"
	"strings"
)

// zfsProvider reads ZFS via the CLI (privileged; /dev/zfs ioctls need root).
// One `sh -c` combines the three queries (one priv approval): `zpool list -Hp`
// (tab-separated, exact bytes), `zpool status` (vdev tree + errors + scan),
// and `zfs list -Hp` (datasets). Pure parsers below for unit testing.
type zfsProvider struct{}

func init() { registerProvider(zfsProvider{}) }

func (zfsProvider) Name() string     { return "zfs" }
func (zfsProvider) Privileged() bool { return true }

// Detect: zpool tool present (cheap). The pool list itself needs the
// privileged Collect; no pools → present=false.
func (zfsProvider) Detect() bool { return toolOnPath("zpool") }

const (
	zfsMarkStatus   = "@@STATUS"
	zfsMarkDatasets = "@@DATASETS"
)

func (zfsProvider) ScanScript(_ context.Context) (string, bool) {
	return strings.Join([]string{
		"zpool list -Hp -o name,size,alloc,free,frag,health",
		"echo " + zfsMarkStatus,
		"zpool status",
		"echo " + zfsMarkDatasets,
		"zfs list -Hp -o name,used,avail,refer,mountpoint",
	}, "\n"), true
}

func (zfsProvider) ParseScan(out string) (Manager, bool, error) {
	pools := parseZfsCombined(out)
	if len(pools) == 0 {
		return Manager{}, false, nil
	}
	objs := make([]any, len(pools))
	for i, p := range pools {
		objs[i] = p
	}
	return Manager{Kind: "zfs", Objects: objs}, true, nil
}

func parseZfsCombined(text string) []ZPool {
	listPart := text
	if i := strings.Index(text, "\n@@"); i >= 0 {
		listPart = text[:i]
	}
	var statusPart, dsPart string
	for _, seg := range splitMarkers(text) {
		switch strings.TrimSpace(seg.mark) {
		case zfsMarkStatus:
			statusPart = seg.body
		case zfsMarkDatasets:
			dsPart = seg.body
		}
	}

	pools := parseZpoolList(listPart)
	status := parseZpoolStatus(statusPart)
	datasets := parseZfsList(dsPart)

	for i := range pools {
		if s, ok := status[pools[i].Name]; ok {
			pools[i].ScanStatus = s.scan
			pools[i].Vdevs = s.vdevs
			if pools[i].State == "" {
				pools[i].State = s.state
			}
		}
		for _, ds := range datasets {
			if ds.Name == pools[i].Name || strings.HasPrefix(ds.Name, pools[i].Name+"/") {
				pools[i].Datasets = append(pools[i].Datasets, ds)
			}
		}
	}
	return pools
}

// parseZpoolList parses `zpool list -Hp -o name,size,alloc,free,frag,health`
// (tab-separated; -p gives exact bytes and a bare frag percentage).
func parseZpoolList(text string) []ZPool {
	var out []ZPool
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		p := ZPool{Name: f[0], State: f[5]}
		p.Size, _ = strconv.ParseUint(f[1], 10, 64)
		p.Alloc, _ = strconv.ParseUint(f[2], 10, 64)
		p.Free, _ = strconv.ParseUint(f[3], 10, 64)
		p.Frag, _ = strconv.Atoi(strings.TrimSuffix(f[4], "%"))
		out = append(out, p)
	}
	return out
}

// parseZfsList parses `zfs list -Hp -o name,used,avail,refer,mountpoint`.
func parseZfsList(text string) []ZDataset {
	var out []ZDataset
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		d := ZDataset{Name: f[0], Mountpoint: f[4]}
		d.Used, _ = strconv.ParseUint(f[1], 10, 64)
		d.Avail, _ = strconv.ParseUint(f[2], 10, 64)
		d.Refer, _ = strconv.ParseUint(f[3], 10, 64)
		out = append(out, d)
	}
	return out
}

type poolStatus struct {
	state string
	scan  string
	vdevs []ZVdev
}

// parseZpoolStatus parses `zpool status`. The config section is an
// indentation-encoded tree; the top row is the pool itself, whose children
// are the top-level vdevs. Returns per-pool state/scan/vdev-tree.
func parseZpoolStatus(text string) map[string]poolStatus {
	out := map[string]poolStatus{}
	for _, block := range splitPoolBlocks(text) {
		name := lineValue(block, "pool:")
		if name == "" {
			continue
		}
		ps := poolStatus{
			state: lineValue(block, "state:"),
			scan:  lineValue(block, "scan:"),
		}
		ps.vdevs = parseVdevConfig(block)
		out[name] = ps
	}
	return out
}

// splitPoolBlocks splits `zpool status` output into per-pool blocks (each
// begins with a "  pool:" line).
func splitPoolBlocks(text string) []string {
	var blocks []string
	var cur strings.Builder
	started := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "pool:") {
			if started {
				blocks = append(blocks, cur.String())
				cur.Reset()
			}
			started = true
		}
		if started {
			cur.WriteString(line + "\n")
		}
	}
	if started {
		blocks = append(blocks, cur.String())
	}
	return blocks
}

// lineValue returns the text after "key:" on the first matching line.
func lineValue(block, key string) string {
	for _, line := range strings.Split(block, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, key) {
			return strings.TrimSpace(strings.TrimPrefix(t, key))
		}
	}
	return ""
}

// parseVdevConfig parses the indented config tree in a status block and
// returns the pool row's children (the top-level vdevs).
func parseVdevConfig(block string) []ZVdev {
	lines := strings.Split(block, "\n")
	start := -1
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) >= 1 && f[0] == "NAME" { // the config header row
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	// Build a pointer tree (so grandchildren attach correctly), then convert
	// to the value tree the wire type wants.
	type pnode struct {
		indent   int
		name     string
		state    string
		typ      string
		r, w, c  uint64
		children []*pnode
	}
	var root *pnode
	var stack []*pnode
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			break // blank line ends the config section
		}
		// Indent: drop a single leading tab, then count leading spaces.
		l := strings.TrimPrefix(line, "\t")
		indent := len(l) - len(strings.TrimLeft(l, " "))
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n := &pnode{indent: indent, name: f[0], state: f[1], typ: classifyVdev(f[0])}
		if len(f) >= 5 {
			n.r, n.w, n.c = parseU(f[2]), parseU(f[3]), parseU(f[4])
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			root = n // the pool row itself
		} else {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
		}
		stack = append(stack, n)
	}
	if root == nil {
		return nil
	}
	var conv func(n *pnode) ZVdev
	conv = func(n *pnode) ZVdev {
		v := ZVdev{Name: n.name, State: n.state, Type: n.typ, ReadErr: n.r, WriteErr: n.w, CksumErr: n.c}
		for _, ch := range n.children {
			v.Children = append(v.Children, conv(ch))
		}
		return v
	}
	out := make([]ZVdev, 0, len(root.children))
	for _, ch := range root.children {
		out = append(out, conv(ch))
	}
	return out
}

func classifyVdev(name string) string {
	for _, p := range []string{"mirror", "raidz3", "raidz2", "raidz1", "raidz", "draid", "spare", "log", "cache", "replacing"} {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return "disk"
}

func parseU(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
