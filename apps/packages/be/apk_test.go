package packages

import (
	"strings"
	"testing"
)

// canned `apk search -v` output. Two installed-style names, one with
// the typical -rN release suffix; one without (apk lets backends
// publish version-only packages too).
const apkSearchSample = "" +
	"bash-5.2.21-r1 - The GNU Bourne Again shell\n" +
	"bash-completion-2.13-r0 - Programmable completion for Bash\n" +
	"cowsay-3.04-r3 - Configurable talking cow\n"

func TestParseAPKSearch(t *testing.T) {
	installed := map[string]string{
		"bash": "5.2.21-r1",
	}
	got := parseAPKSearch([]byte(apkSearchSample), installed)

	wantNames := []string{"bash", "bash-completion", "cowsay"}
	if len(got) != len(wantNames) {
		t.Fatalf("want %d packages, got %d: %+v", len(wantNames), len(got), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	if !got[0].Installed {
		t.Errorf("bash should be Installed=true")
	}
	if got[0].Version != "5.2.21-r1" {
		t.Errorf("bash version = %q, want 5.2.21-r1", got[0].Version)
	}
	if got[1].Installed {
		t.Errorf("bash-completion should be Installed=false")
	}
	// Summaries survive.
	if !strings.Contains(got[0].Summary, "Bourne Again") {
		t.Errorf("bash summary lost: %q", got[0].Summary)
	}
}

func TestSplitAPKNameVersion(t *testing.T) {
	cases := map[string][2]string{
		// Standard: name + version + release.
		"bash-5.2.21-r1":            {"bash", "5.2.21-r1"},
		"bash-completion-2.13-r0":   {"bash-completion", "2.13-r0"},
		"py3-flask-2.0.1-r2":        {"py3-flask", "2.0.1-r2"},
		// Hyphenated name + version + release.
		"alpine-base-3.21.0-r0":     {"alpine-base", "3.21.0-r0"},
		// No -rN suffix: last segment is the version.
		"plain-1.0":                 {"plain", "1.0"},
		// Single token: name only.
		"orphan":                    {"orphan", ""},
	}
	for in, want := range cases {
		gotName, gotVer := splitAPKNameVersion(in)
		if gotName != want[0] || gotVer != want[1] {
			t.Errorf("splitAPKNameVersion(%q) = (%q, %q), want (%q, %q)",
				in, gotName, gotVer, want[0], want[1])
		}
	}
}

func TestAllDigits(t *testing.T) {
	if !allDigits("123") {
		t.Errorf("\"123\" should be all digits")
	}
	if allDigits("") || allDigits("12a") || allDigits("a") {
		t.Errorf("invalid digit-strings accepted")
	}
}

func TestAPKBackend_InstallArgv(t *testing.T) {
	b := &apkBackend{}
	t.Setenv("WASH_PACKAGES_APK_BIN", "apk")

	if got := b.InstallArgv("bash", ""); !equal(got, []string{"apk", "add", "--no-progress", "bash"}) {
		t.Errorf("install argv wrong: %v", got)
	}
	if got := b.RemoveArgv("bash", ""); !equal(got, []string{"apk", "del", "--no-progress", "bash"}) {
		t.Errorf("remove argv wrong: %v", got)
	}
	if got := b.UpgradePkgArgv("bash", ""); !equal(got, []string{"apk", "upgrade", "--no-progress", "bash"}) {
		t.Errorf("upgrade argv wrong: %v", got)
	}
	if got := b.InstallArgv("anything", "task"); got != nil {
		t.Errorf("task kind should return nil, got %v", got)
	}
}

func TestAPKBackend_GlobalActions(t *testing.T) {
	b := &apkBackend{}
	t.Setenv("WASH_PACKAGES_APK_BIN", "apk")

	actions := b.GlobalActions()
	wantIDs := []string{"update_system", "update", "upgrade", "cache_clean"}
	if len(actions) != len(wantIDs) {
		t.Fatalf("want %d actions, got %d", len(wantIDs), len(actions))
	}
	for i, want := range wantIDs {
		if actions[i].ID != want {
			t.Errorf("actions[%d].ID = %q, want %q", i, actions[i].ID, want)
		}
	}

	cases := map[string][]string{
		"update":        {"apk", "update"},
		"upgrade":       {"apk", "upgrade", "--no-progress"},
		"cache_clean":   {"apk", "cache", "clean"},
		"update_system": {"sh", "-c", "apk update && apk upgrade --no-progress"},
		"bogus":         nil,
	}
	for id, want := range cases {
		got := b.GlobalActionArgv(id)
		if !equal(got, want) {
			t.Errorf("GlobalActionArgv(%q) = %v, want %v", id, got, want)
		}
	}
}
