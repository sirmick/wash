package packages

import (
	"strings"
	"testing"
)

// canned `opkg find` output. Three rows; one with a hyphenated name.
const opkgFindSample = "" +
	"busybox - 1.36.1-r2 - The Swiss Army Knife of embedded Linux.\n" +
	"luci-app-firewall - git-23.117.55916 - LuCI Support for firewall\n" +
	"hostapd-common - 2024-07-20-b3d0a14d-r1 - hostapd common helper files\n"

// canned `opkg list-installed` output: just "name - version" lines.
const opkgListInstalledSample = "" +
	"busybox - 1.36.1-r2\n" +
	"base-files - 1670~81be8a8869\n"

func TestParseOPKGList(t *testing.T) {
	installed := map[string]string{
		"busybox": "1.36.1-r2",
	}
	got := parseOPKGList([]byte(opkgFindSample), installed)

	wantNames := []string{"busybox", "luci-app-firewall", "hostapd-common"}
	if len(got) != len(wantNames) {
		t.Fatalf("want %d, got %d: %+v", len(wantNames), len(got), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	if !got[0].Installed {
		t.Errorf("busybox should be Installed=true")
	}
	if got[0].Version != "1.36.1-r2" {
		t.Errorf("busybox version = %q, want 1.36.1-r2", got[0].Version)
	}
	if got[1].Installed {
		t.Errorf("luci-app-firewall should be Installed=false")
	}
	if !strings.Contains(got[0].Summary, "Swiss Army") {
		t.Errorf("busybox summary lost: %q", got[0].Summary)
	}
	// Hyphenated name retained.
	if got[1].Name != "luci-app-firewall" {
		t.Errorf("hyphenated name mangled: %q", got[1].Name)
	}
}

func TestParseOPKGList_HandlesListInstalledOutput(t *testing.T) {
	// list-installed has no summary column; verify the same parser
	// happily ignores the missing third field.
	got := parseOPKGList([]byte(opkgListInstalledSample), nil)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Name != "busybox" || got[0].Version != "1.36.1-r2" {
		t.Errorf("busybox row wrong: %+v", got[0])
	}
	if got[1].Summary != "" {
		t.Errorf("list-installed shouldn't carry a summary: %q", got[1].Summary)
	}
}

func TestParseOPKGList_SkipsBlankAndMalformedLines(t *testing.T) {
	in := "\n  \n" +
		"goodone - 1.0 - ok\n" +
		"justname\n" + // no " - " separator → skip
		"\n" +
		"another - 2.0\n"
	got := parseOPKGList([]byte(in), nil)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
}

func TestOPKGBackend_InstallArgv(t *testing.T) {
	b := &opkgBackend{}
	t.Setenv("WASH_PACKAGES_OPKG_BIN", "opkg")

	if got := b.InstallArgv("busybox", ""); !equal(got, []string{"opkg", "install", "busybox"}) {
		t.Errorf("install argv wrong: %v", got)
	}
	if got := b.RemoveArgv("busybox", ""); !equal(got, []string{"opkg", "remove", "busybox"}) {
		t.Errorf("remove argv wrong: %v", got)
	}
	if got := b.UpgradePkgArgv("busybox", ""); !equal(got, []string{"opkg", "upgrade", "busybox"}) {
		t.Errorf("upgrade argv wrong: %v", got)
	}
	// opkg has no kind concept — non-empty kind should reject.
	if got := b.InstallArgv("anything", "task"); got != nil {
		t.Errorf("task kind should return nil, got %v", got)
	}
}

func TestOPKGBackend_GlobalActions(t *testing.T) {
	b := &opkgBackend{}
	t.Setenv("WASH_PACKAGES_OPKG_BIN", "opkg")

	actions := b.GlobalActions()
	wantIDs := []string{"update_system", "update", "upgrade"}
	if len(actions) != len(wantIDs) {
		t.Fatalf("want %d actions, got %d", len(wantIDs), len(actions))
	}
	for i, want := range wantIDs {
		if actions[i].ID != want {
			t.Errorf("actions[%d].ID = %q, want %q", i, actions[i].ID, want)
		}
	}

	cases := map[string][]string{
		"update":        {"opkg", "update"},
		"upgrade":       {"opkg", "upgrade"},
		"update_system": {"sh", "-c", "opkg update && opkg upgrade"},
		"bogus":         nil,
	}
	for id, want := range cases {
		got := b.GlobalActionArgv(id)
		if !equal(got, want) {
			t.Errorf("GlobalActionArgv(%q) = %v, want %v", id, got, want)
		}
	}
}
