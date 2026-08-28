package packages

import (
	"strings"
	"testing"
)

// canned `dnf search --quiet` output. Two matches; one shared between
// arches (filtered to a single row by the parser's dedup). The
// "=== ... ===" header is intentionally interleaved — newer dnf
// suppresses these with --quiet but older versions don't.
const dnfSearchSample = "=== Name & Summary Matched: bash ===\n" +
	"bash.x86_64 : The GNU Bourne Again shell\n" +
	"bash-completion.noarch : Programmable completion for Bash\n" +
	"bash.i686 : The GNU Bourne Again shell\n" +
	"cowsay.noarch : Configurable talking cow\n"

func TestParseDNFSearch(t *testing.T) {
	installed := map[string]string{
		"bash": "5.2.15-3.fc40",
	}
	got := parseDNFSearch([]byte(dnfSearchSample), installed)

	wantNames := []string{"bash", "bash-completion", "cowsay"}
	if len(got) != len(wantNames) {
		t.Fatalf("want %d packages, got %d: %+v", len(wantNames), len(got), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	// Installed bash has its Version filled in from the rpm map.
	if !got[0].Installed {
		t.Errorf("bash should be Installed=true")
	}
	if got[0].Version != "5.2.15-3.fc40" {
		t.Errorf("bash version = %q, want 5.2.15-3.fc40", got[0].Version)
	}
	// bash-completion is not in `installed`; Installed should be false.
	if got[1].Installed {
		t.Errorf("bash-completion should be Installed=false")
	}
	// Summaries survive.
	if !strings.Contains(got[0].Summary, "Bourne Again") {
		t.Errorf("bash summary lost: %q", got[0].Summary)
	}
}

func TestParseDNFSearch_StripsArchSuffix(t *testing.T) {
	// Verify the package name doesn't carry ".x86_64" etc.
	got := parseDNFSearch([]byte("foo.x86_64 : bar\n"), nil)
	if len(got) != 1 || got[0].Name != "foo" {
		t.Errorf("want foo, got %+v", got)
	}
	// A non-arch trailing dot stays in the name (e.g. python3.11).
	got = parseDNFSearch([]byte("python3.11.noarch : Python 3.11\n"), nil)
	if len(got) != 1 || got[0].Name != "python3.11" {
		t.Errorf("want python3.11, got %+v", got)
	}
}

func TestParseDNFSearch_SkipsHeadersAndBlankLines(t *testing.T) {
	in := "\n=== Name Exactly Matched: x ===\n\n" +
		"x.x86_64 : an x\n" +
		"   \n" +
		"=== Summary Matched: x ===\n" +
		"y.noarch : a y referencing x\n"
	got := parseDNFSearch([]byte(in), nil)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
}

func TestDNFBackend_InstallArgv(t *testing.T) {
	b := &dnfBackend{}
	t.Setenv("WASH_PACKAGES_DNF_BIN", "dnf")

	if got := b.InstallArgv("bash", ""); !equal(got, []string{"dnf", "-y", "install", "bash"}) {
		t.Errorf("install argv wrong: %v", got)
	}
	if got := b.RemoveArgv("bash", ""); !equal(got, []string{"dnf", "-y", "remove", "bash"}) {
		t.Errorf("remove argv wrong: %v", got)
	}
	if got := b.UpgradePkgArgv("bash", ""); !equal(got, []string{"dnf", "-y", "upgrade", "bash"}) {
		t.Errorf("upgrade argv wrong: %v", got)
	}
	// Non-empty kind isn't supported (no group surfacing yet).
	if got := b.InstallArgv("@base", "group"); got != nil {
		t.Errorf("group kind should return nil for now, got %v", got)
	}
}

func TestDNFBackend_GlobalActions(t *testing.T) {
	b := &dnfBackend{}
	t.Setenv("WASH_PACKAGES_DNF_BIN", "dnf")

	actions := b.GlobalActions()
	wantIDs := []string{"update_system", "refresh", "upgrade", "autoremove", "clean"}
	if len(actions) != len(wantIDs) {
		t.Fatalf("want %d actions, got %d", len(wantIDs), len(actions))
	}
	for i, want := range wantIDs {
		if actions[i].ID != want {
			t.Errorf("actions[%d].ID = %q, want %q", i, actions[i].ID, want)
		}
	}
	for _, a := range actions {
		switch a.ID {
		case "update_system":
			if a.Surface != "toolbar" {
				t.Errorf("update_system should be toolbar, got %q", a.Surface)
			}
		case "autoremove":
			if !a.Danger {
				t.Errorf("autoremove should be Danger=true")
			}
		default:
			if a.Surface == "toolbar" {
				t.Errorf("%s should not be on toolbar", a.ID)
			}
		}
	}

	cases := map[string][]string{
		"refresh":       {"dnf", "check-update"},
		"upgrade":       {"dnf", "-y", "upgrade"},
		"update_system": {"dnf", "-y", "upgrade"},
		"autoremove":    {"dnf", "-y", "autoremove"},
		"clean":         {"dnf", "clean", "all"},
		"bogus":         nil,
	}
	for id, want := range cases {
		got := b.GlobalActionArgv(id)
		if !equal(got, want) {
			t.Errorf("GlobalActionArgv(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestIsRPMArch(t *testing.T) {
	for _, a := range []string{"x86_64", "aarch64", "noarch", "src", "riscv64"} {
		if !isRPMArch(a) {
			t.Errorf("%s should be a recognised arch", a)
		}
	}
	for _, a := range []string{"11", "fc40", "el9", ""} {
		if isRPMArch(a) {
			t.Errorf("%s should NOT be a recognised arch", a)
		}
	}
}

// canned `dnf search --quiet` output from dnf5 (Fedora 41+), which
// ships as /usr/bin/dnf and replaced dnf4's " : " separator with a
// TAB and its "=== ... ===" headers with "Matched fields: ...".
// Captured verbatim from a fc44 host running dnf5 5.4.3.0.
const dnf5SearchSample = "Matched fields: name (exact)\n" +
	" bash.x86_64\tThe GNU Bourne Again shell\n" +
	"Matched fields: name\n" +
	" bash-completion.noarch\tProgrammable completion for Bash\n" +
	" bash.i686\tThe GNU Bourne Again shell\n" +
	"Matched fields: summary\n" +
	" cowsay.noarch\tConfigurable talking cow\n"

func TestParseDNFSearch_DNF5Format(t *testing.T) {
	installed := map[string]string{"bash": "5.3.0-2.fc44"}
	got := parseDNFSearch([]byte(dnf5SearchSample), installed)

	wantNames := []string{"bash", "bash-completion", "cowsay"}
	if len(got) != len(wantNames) {
		t.Fatalf("want %d packages, got %d: %+v", len(wantNames), len(got), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	// Leading space before the name and the TAB separator are both
	// stripped, so installed-state still keys off the bare name.
	if !got[0].Installed || got[0].Version != "5.3.0-2.fc44" {
		t.Errorf("bash installed-state lost: %+v", got[0])
	}
	if got[0].Summary != "The GNU Bourne Again shell" {
		t.Errorf("bash summary = %q", got[0].Summary)
	}
	if got[2].Summary != "Configurable talking cow" {
		t.Errorf("cowsay summary = %q", got[2].Summary)
	}
}

func TestParseDNFSearch_DNF5NoMatches(t *testing.T) {
	// dnf5 exits 0 and prints this on stdout where dnf4 exited 1 with
	// nothing; it must not parse as a package.
	if got := parseDNFSearch([]byte("No matches found.\n"), nil); len(got) != 0 {
		t.Errorf("want no packages, got %+v", got)
	}
}

func TestSplitDNFSearchLine(t *testing.T) {
	cases := []struct {
		in            string
		name, summary string
		ok            bool
	}{
		{"bash.x86_64 : The GNU Bourne Again shell", "bash.x86_64", "The GNU Bourne Again shell", true},
		{"bash.x86_64\tThe GNU Bourne Again shell", "bash.x86_64", "The GNU Bourne Again shell", true},
		{" bash.x86_64\tThe GNU Bourne Again shell", "bash.x86_64", "The GNU Bourne Again shell", true},
		// TAB wins: a dnf5 summary may itself contain " : ".
		{"foo.noarch\tPlugin : legacy shim", "foo.noarch", "Plugin : legacy shim", true},
		{"Matched fields: name (exact)", "", "", false},
		{"No matches found.", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		name, summary, ok := splitDNFSearchLine(c.in)
		if ok != c.ok || name != c.name || summary != c.summary {
			t.Errorf("splitDNFSearchLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, name, summary, ok, c.name, c.summary, c.ok)
		}
	}
}
