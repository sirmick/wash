package packages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTasksel writes a small shell script that mimics
// `tasksel --list-tasks` output. Used by the tests below + the e2e
// suite via WASH_PACKAGES_TASKSEL_BIN.
func fakeTasksel(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-tasksel")
	script := "#!/bin/sh\ncat <<'EOF'\n" + body + "EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tasksel: %v", err)
	}
	return path
}

func TestTaskselTasks_FilterAndStatus(t *testing.T) {
	body := "u\tlamp-server\tLAMP server\n" +
		"i\topenssh-server\tOpenSSH server\n" +
		"u\tubuntu-mate-desktop\tUbuntu MATE desktop\n"
	t.Setenv("WASH_PACKAGES_TASKSEL_BIN", fakeTasksel(t, body))

	got, err := taskselTasks(context.Background(), "server")
	if err != nil {
		t.Fatalf("taskselTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tasks matching \"server\", got %d: %+v", len(got), got)
	}
	names := []string{got[0].Name, got[1].Name}
	if !contains(names, "lamp-server") || !contains(names, "openssh-server") {
		t.Errorf("want lamp-server + openssh-server, got %v", names)
	}
	// openssh-server is marked `i` → Installed=true.
	for _, p := range got {
		if p.Name == "openssh-server" && !p.Installed {
			t.Errorf("openssh-server should be Installed=true")
		}
		if p.Name == "lamp-server" && p.Installed {
			t.Errorf("lamp-server should be Installed=false")
		}
		if p.Kind != "task" {
			t.Errorf("%s: want Kind=task, got %q", p.Name, p.Kind)
		}
	}
}

func TestTaskselTasks_EmptyQueryReturnsAll(t *testing.T) {
	body := "u\ta\tA\nu\tb\tB\n"
	t.Setenv("WASH_PACKAGES_TASKSEL_BIN", fakeTasksel(t, body))

	got, err := taskselTasks(context.Background(), "")
	if err != nil {
		t.Fatalf("taskselTasks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("empty query should return all tasks, got %d", len(got))
	}
}

func TestAptBackend_InstallArgvByKind(t *testing.T) {
	b := &aptBackend{}
	t.Setenv("WASH_PACKAGES_APT_BIN", "apt-get")
	t.Setenv("WASH_PACKAGES_TASKSEL_BIN", "tasksel")

	regular := b.InstallArgv("bash", "")
	if !equal(regular, []string{"apt-get", "-y", "install", "bash"}) {
		t.Errorf("regular install argv wrong: %v", regular)
	}
	task := b.InstallArgv("lamp-server", "task")
	if !equal(task, []string{"tasksel", "install", "lamp-server"}) {
		t.Errorf("task install argv wrong: %v", task)
	}

	// Per-task upgrade is intentionally unsupported.
	if got := b.UpgradePkgArgv("lamp-server", "task"); got != nil {
		t.Errorf("UpgradePkgArgv for task should be nil, got %v", got)
	}
	// Regular upgrade still works.
	if got := b.UpgradePkgArgv("bash", ""); len(got) == 0 || !strings.Contains(strings.Join(got, " "), "--only-upgrade") {
		t.Errorf("regular upgrade argv missing --only-upgrade: %v", got)
	}

}

func TestAptBackend_GlobalActions(t *testing.T) {
	b := &aptBackend{}
	t.Setenv("WASH_PACKAGES_APT_BIN", "apt-get")

	actions := b.GlobalActions()
	wantIDs := []string{"update_system", "update", "upgrade", "dist_upgrade", "autoremove"}
	if len(actions) != len(wantIDs) {
		t.Fatalf("want %d actions, got %d", len(wantIDs), len(actions))
	}
	for i, want := range wantIDs {
		if actions[i].ID != want {
			t.Errorf("actions[%d].ID = %q, want %q", i, actions[i].ID, want)
		}
	}
	// Toolbar vs menu split: only update_system surfaces to toolbar.
	for _, a := range actions {
		switch a.ID {
		case "update_system":
			if a.Surface != "toolbar" {
				t.Errorf("update_system should be Surface=toolbar, got %q", a.Surface)
			}
		case "autoremove":
			if !a.Danger {
				t.Errorf("autoremove should be Danger=true")
			}
			if a.Surface != "" && a.Surface != "menu" {
				t.Errorf("autoremove should not be on toolbar, got Surface=%q", a.Surface)
			}
		default:
			if a.Surface == "toolbar" {
				t.Errorf("%s should not be on toolbar (only update_system)", a.ID)
			}
		}
	}

	cases := map[string][]string{
		"update":       {"apt-get", "update"},
		"upgrade":      {"apt-get", "-y", "upgrade"},
		"dist_upgrade": {"apt-get", "-y", "dist-upgrade"},
		"autoremove":   {"apt-get", "-y", "autoremove"},
		// Combo runs through sh -c so both halves land in one PTY.
		"update_system": {"sh", "-c", "apt-get update && apt-get -y dist-upgrade"},
		"bogus":         nil,
	}
	for id, want := range cases {
		got := b.GlobalActionArgv(id)
		if !equal(got, want) {
			t.Errorf("GlobalActionArgv(%q) = %v, want %v", id, got, want)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
