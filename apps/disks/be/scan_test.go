package disks

import (
	"context"
	"strings"
	"testing"
)

// fakeSP is a controllable privileged ShellProvider for the scan tests.
type fakeSP struct {
	name   string
	script string
	mgr    Manager
}

func (f fakeSP) Name() string                              { return f.name }
func (f fakeSP) Detect() bool                              { return true }
func (f fakeSP) Privileged() bool                          { return true }
func (f fakeSP) ScanScript(context.Context) (string, bool) { return f.script, true }
func (f fakeSP) ParseScan(out string) (Manager, bool, error) {
	if strings.TrimSpace(out) == "" {
		return Manager{}, false, nil
	}
	return f.mgr, true, nil
}

// TestScanPrivilegedSingleEscalation is the regression guard for "the scan must
// be ONE privileged call, not one per manager": every privileged provider's
// script is folded into a single `sh -c` run.
func TestScanPrivilegedSingleEscalation(t *testing.T) {
	defer swap(&providers, []Provider{
		fakeSP{name: "lvm", script: "echo lvm-data", mgr: Manager{Kind: "lvm"}},
		fakeSP{name: "btrfs", script: "echo btrfs-data", mgr: Manager{Kind: "btrfs"}},
		fakeSP{name: "zfs", script: "echo zfs-data", mgr: Manager{Kind: "zfs"}},
	})()

	calls := 0
	var argv []string
	run := func(_ context.Context, a []string, _ string) ([]byte, error) {
		calls++
		argv = a
		// Emulate the shell running the combined script: each provider's marker
		// followed by its section.
		return []byte("@@WASHPROVIDER:lvm\nlvm-data\n" +
			"@@WASHPROVIDER:btrfs\nbtrfs-data\n" +
			"@@WASHPROVIDER:zfs\nzfs-data\n"), nil
	}

	mgrs := scanPrivileged(context.Background(), run)

	if calls != 1 {
		t.Fatalf("want exactly 1 escalation, got %d", calls)
	}
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("argv = %v", argv)
	}
	// The single script carries every provider's marker + commands.
	for _, want := range []string{"@@WASHPROVIDER:lvm", "echo lvm-data", "echo btrfs-data", "echo zfs-data"} {
		if !strings.Contains(argv[2], want) {
			t.Errorf("combined script missing %q:\n%s", want, argv[2])
		}
	}
	kinds := map[string]bool{}
	for _, m := range mgrs {
		kinds[m.Kind] = true
	}
	if !kinds["lvm"] || !kinds["btrfs"] || !kinds["zfs"] {
		t.Fatalf("managers = %v", mgrs)
	}
}

func TestScanPrivilegedNilRunner(t *testing.T) {
	if scanPrivileged(context.Background(), nil) != nil {
		t.Fatal("nil runner (poll) must not escalate")
	}
}

func TestSplitProviderSections(t *testing.T) {
	// btrfs section keeps its own internal @@SUBVOL/@@STATS markers.
	out := "@@WASHPROVIDER:btrfs\nLabel: 'x'\n@@SUBVOL /mnt\nID 256 path sub\n" +
		"@@WASHPROVIDER:zfs\ntank 100 ONLINE\n"
	secs := splitProviderSections(out)
	if !strings.Contains(secs["btrfs"], "@@SUBVOL /mnt") {
		t.Errorf("btrfs section lost inner marker: %q", secs["btrfs"])
	}
	if !strings.Contains(secs["btrfs"], "Label: 'x'") {
		t.Errorf("btrfs section content: %q", secs["btrfs"])
	}
	if !strings.Contains(secs["zfs"], "tank 100 ONLINE") {
		t.Errorf("zfs section: %q", secs["zfs"])
	}
}
