package disks

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReadSyncPct(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		content string
		want    float64
	}{
		{"none\n", 0},
		{"idle\n", 0},
		{"1234 / 2468\n", 50},
		{"0 / 100\n", 0},
	}
	for i, c := range cases {
		p := filepath.Join(dir, "sc")
		mustWrite(t, p, c.content)
		if got := readSyncPct(p); got != c.want {
			t.Errorf("case %d %q: got %v want %v", i, c.content, got, c.want)
		}
	}
}

func TestMDProvider(t *testing.T) {
	root := t.TempDir()
	sb := filepath.Join(root, "sys", "block")
	defer swap(&sysBlock, sb)()

	// Healthy raid1 over sda2 + sdb2.
	md := filepath.Join(sb, "md0", "md")
	mustWrite(t, filepath.Join(sb, "md0", "size"), "3905980000\n")
	mustWrite(t, filepath.Join(md, "level"), "raid1\n")
	mustWrite(t, filepath.Join(md, "array_state"), "clean\n")
	mustWrite(t, filepath.Join(md, "raid_disks"), "2\n")
	mustWrite(t, filepath.Join(md, "degraded"), "0\n")
	mustWrite(t, filepath.Join(md, "sync_action"), "idle\n")
	mustWrite(t, filepath.Join(md, "sync_completed"), "none\n")
	mustWrite(t, filepath.Join(md, "dev-sda2", "slot"), "0\n")
	mustWrite(t, filepath.Join(md, "dev-sda2", "state"), "in_sync\n")
	mustWrite(t, filepath.Join(md, "dev-sdb2", "slot"), "1\n")
	mustWrite(t, filepath.Join(md, "dev-sdb2", "state"), "in_sync\n")

	p := mdProvider{}
	if !p.Detect() {
		t.Fatal("Detect should be true with an md array present")
	}
	mgr, present, err := p.Collect(context.Background(), nil)
	if err != nil || !present {
		t.Fatalf("Collect: present=%v err=%v", present, err)
	}
	if mgr.Kind != "md" || len(mgr.Objects) != 1 {
		t.Fatalf("mgr = %+v", mgr)
	}
	a := mgr.Objects[0].(MDArray)
	if a.Name != "md0" || a.Level != "raid1" || a.State != "clean" || a.RaidDisks != 2 || a.Degraded {
		t.Fatalf("array = %+v", a)
	}
	if a.Size != 3905980000*512 {
		t.Fatalf("size = %d", a.Size)
	}
	if len(a.Members) != 2 {
		t.Fatalf("members = %v", a.Members)
	}
}

func TestMDProviderAbsent(t *testing.T) {
	root := t.TempDir()
	defer swap(&sysBlock, filepath.Join(root, "sys", "block"))()
	mustMkdir(t, sysBlock)
	if (mdProvider{}).Detect() {
		t.Error("Detect should be false with no md arrays")
	}
}

func TestFakeSnapshotShape(t *testing.T) {
	defer swap2(t, "WASH_DISKS_SOURCE", "fake")()
	snap := collectSnapshot(context.Background(), nil)
	if len(snap.Disks) != 2 {
		t.Fatalf("disks = %d", len(snap.Disks))
	}
	kinds := map[string]bool{}
	for _, m := range snap.Managers {
		kinds[m.Kind] = true
	}
	for _, want := range []string{"md", "lvm", "btrfs", "zfs"} {
		if !kinds[want] {
			t.Errorf("fake snapshot missing manager kind %q", want)
		}
	}
	if !snap.Capabilities.SMART || !snap.Capabilities.ZFS {
		t.Errorf("fake capabilities = %+v", snap.Capabilities)
	}
}

// swap2 sets an env var and returns a restore func.
func swap2(t *testing.T, key, val string) func() {
	t.Helper()
	t.Setenv(key, val)
	return func() {}
}
