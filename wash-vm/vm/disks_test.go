package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDisksRealKernel is the wash-disks Tier-4 gate (docs/STORAGE.md): boot the
// baked image with real virtio scratch disks, build genuine md/LVM/btrfs (and
// ZFS when WASH_VM_ZFS=1) on them with the real kernel + tools, then assert our
// providers parsed them correctly via `wash-disks --dump-snapshot`. This proves
// the parsers against actual subsystems — independent of the router/priv/UI.
//
// Needs the storage-capable image (scripts/build-vm-image-alpine.sh, which now
// bakes mdadm/lvm2/btrfs-progs); skips cleanly when the tooling/artifacts are
// absent, like the other VM gates.
func TestDisksRealKernel(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// Six raw virtio scratch disks → /dev/vda…/dev/vdf (attach order; the
	// rootfs is a RAM initramfs, so these are the only block devices).
	dir := t.TempDir()
	var extra []string
	for i := 0; i < 6; i++ {
		extra = append(extra, "-drive",
			fmt.Sprintf("file=%s,if=virtio,format=raw,cache=unsafe", scratchDisk(t, dir, i)))
	}

	vm, err := Launch(ctx, Opts{Kernel: kernel, Initramfs: initramfs, Mem: "1536M", Extra: extra})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer vm.Close()
	if err := vm.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v\nconsole:\n%s", err, vm.ConsoleLog())
	}

	run := func(cmd string) proto_Response {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v\nconsole:\n%s", cmd, err, vm.ConsoleLog())
		}
		return proto_Response{Exit: r.Exit, Out: r.Out, Err: r.Err}
	}
	mustRun := func(cmd string) string {
		t.Helper()
		r := run(cmd)
		if r.Exit != 0 {
			t.Fatalf("cmd %q exit=%d\nstdout:\n%s\nstderr:\n%s", cmd, r.Exit, r.Out, r.Err)
		}
		return r.Out
	}

	// Skip rather than fail if the image predates the storage tooling.
	if run("command -v mdadm >/dev/null && command -v lvs >/dev/null && command -v mkfs.btrfs >/dev/null").Exit != 0 {
		t.Skip("image lacks storage tooling (rebuild scripts/build-vm-image-alpine.sh, RENDER_VER 4-storage)")
	}

	// Modules (belt-and-braces; early.sh already loads them) + wait for the
	// scratch disks to appear.
	run("for m in virtio_blk dm-mod md-mod raid1 btrfs; do modprobe $m 2>/dev/null; done")
	if run("for i in $(seq 1 40); do [ -b /dev/vda ] && [ -b /dev/vdf ] && exit 0; sleep 0.25; done; exit 1").Exit != 0 {
		t.Fatalf("scratch disks did not appear:\n%s", run("ls -l /dev/vd* 2>&1; dmesg | tail -20").Out)
	}

	// --- build real subsystems ---
	// md raid1 mirror over vda+vdb (blank disks → no create prompt).
	mustRun("mdadm --create /dev/md0 --level=1 --raid-devices=2 --metadata=1.2 --run /dev/vda /dev/vdb 2>&1")
	// LVM PV/VG/LV on vdc.
	mustRun("pvcreate -f -y /dev/vdc 2>&1")
	mustRun("vgcreate vgtest /dev/vdc 2>&1")
	mustRun("lvcreate -y -L 64M -n lvtest vgtest 2>&1")
	// btrfs on vdd, mounted, with a subvolume.
	mustRun("mkfs.btrfs -f /dev/vdd 2>&1")
	mustRun("mkdir -p /mnt/btr && mount /dev/vdd /mnt/btr && btrfs subvolume create /mnt/btr/sub 2>&1")

	wantZFS := os.Getenv("WASH_VM_ZFS") == "1"
	if wantZFS {
		mustRun("modprobe zfs 2>&1")
		mustRun("zpool create -f tank mirror /dev/vde /dev/vdf 2>&1")
		mustRun("zfs create tank/ds 2>&1")
	}

	// --- the assertion: our providers parse the real subsystems ---
	out := mustRun("/usr/lib/wash/wash-disks --dump-snapshot")
	var snap dumpSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("parse --dump-snapshot: %v\noutput:\n%s", err, out)
	}

	// Capabilities reflect installed tooling.
	if !snap.Capabilities.MD || !snap.Capabilities.LVM || !snap.Capabilities.Btrfs {
		t.Errorf("capabilities = %+v (want md+lvm+btrfs)", snap.Capabilities)
	}

	// md: a raid1 array with two members.
	md := snap.manager("md")
	if md == nil {
		t.Fatalf("no md manager in snapshot:\n%s", out)
	}
	var mdArr struct {
		Name      string `json:"name"`
		Level     string `json:"level"`
		RaidDisks int    `json:"raid_disks"`
		Members   []struct {
			Name string `json:"name"`
		} `json:"members"`
	}
	decodeFirst(t, md, &mdArr)
	if mdArr.Level != "raid1" || mdArr.RaidDisks != 2 || len(mdArr.Members) != 2 {
		t.Errorf("md array = %+v", mdArr)
	}

	// LVM: vgtest with lvtest.
	lvm := snap.manager("lvm")
	if lvm == nil {
		t.Fatalf("no lvm manager:\n%s", out)
	}
	var vg struct {
		Name string `json:"name"`
		LVs  []struct {
			Name string `json:"name"`
		} `json:"lvs"`
	}
	decodeFirst(t, lvm, &vg)
	if vg.Name != "vgtest" || !hasLV(vg.LVs, "lvtest") {
		t.Errorf("lvm vg = %+v", vg)
	}

	// btrfs: the filesystem we mkfs'd, mounted at /mnt/btr, with the subvolume.
	bt := snap.manager("btrfs")
	if bt == nil {
		t.Fatalf("no btrfs manager:\n%s", out)
	}
	var fs struct {
		Mount      string `json:"mount"`
		Devices    []any  `json:"devices"`
		Subvolumes []struct {
			Path string `json:"path"`
		} `json:"subvolumes"`
	}
	decodeFirst(t, bt, &fs)
	if fs.Mount != "/mnt/btr" || len(fs.Devices) < 1 {
		t.Errorf("btrfs fs = %+v", fs)
	}
	if !hasSubvol(fs.Subvolumes, "sub") {
		t.Errorf("btrfs subvolume 'sub' missing: %+v", fs.Subvolumes)
	}

	if wantZFS {
		zfs := snap.manager("zfs")
		if zfs == nil {
			t.Fatalf("WASH_VM_ZFS set but no zfs manager:\n%s", out)
		}
		var pool struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Vdevs []struct {
				Type string `json:"type"`
			} `json:"vdevs"`
		}
		decodeFirst(t, zfs, &pool)
		if pool.Name != "tank" || pool.State != "ONLINE" {
			t.Errorf("zpool = %+v", pool)
		}
	}

	t.Logf("wash-disks parsed real md/lvm/btrfs%s from a live kernel",
		map[bool]string{true: "/zfs", false: ""}[wantZFS])
}

// ---- helpers ----

// proto_Response mirrors the fields we read from vm.Exec's reply without
// importing the proto package directly into the test body.
type proto_Response struct {
	Exit int
	Out  string
	Err  string
}

func scratchDisk(t *testing.T, dir string, i int) string {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprintf("scratch%d.img", i))
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Truncate(p, 256<<20); err != nil { // 256 MiB sparse
		t.Fatal(err)
	}
	return p
}

type dumpSnapshot struct {
	Capabilities struct {
		SMART bool `json:"smart"`
		MD    bool `json:"md"`
		LVM   bool `json:"lvm"`
		Btrfs bool `json:"btrfs"`
		ZFS   bool `json:"zfs"`
	} `json:"capabilities"`
	Disks    []json.RawMessage `json:"disks"`
	Managers []struct {
		Kind    string            `json:"kind"`
		Objects []json.RawMessage `json:"objects"`
	} `json:"managers"`
}

// manager returns the objects of the manager with the given kind, or nil.
func (s dumpSnapshot) manager(kind string) []json.RawMessage {
	for _, m := range s.Managers {
		if m.Kind == kind {
			return m.Objects
		}
	}
	return nil
}

func decodeFirst(t *testing.T, objs []json.RawMessage, v any) {
	t.Helper()
	if len(objs) == 0 {
		t.Fatal("manager has no objects")
	}
	if err := json.Unmarshal(objs[0], v); err != nil {
		t.Fatalf("decode manager object: %v\nraw: %s", err, string(objs[0]))
	}
}

func hasLV(lvs []struct {
	Name string `json:"name"`
}, name string) bool {
	for _, lv := range lvs {
		if lv.Name == name {
			return true
		}
	}
	return false
}

func hasSubvol(svs []struct {
	Path string `json:"path"`
}, name string) bool {
	for _, sv := range svs {
		if strings.HasSuffix(sv.Path, name) {
			return true
		}
	}
	return false
}
