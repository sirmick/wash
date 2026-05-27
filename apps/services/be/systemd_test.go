package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSystemdUnits(t *testing.T) {
	in := []byte(`[
		{"Unit":"ssh.service","Load":"loaded","Active":"active","Sub":"running","Description":"OpenBSD Secure Shell server"},
		{"Unit":"cron.service","Load":"loaded","Active":"inactive","Sub":"dead","Description":"Regular background program"},
		{"Unit":"masked.service","Load":"masked","Active":"inactive","Sub":"dead","Description":""}
	]`)
	rows, err := parseSystemdUnits(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].Unit != "ssh.service" || rows[0].Active != "active" || rows[0].Sub != "running" {
		t.Errorf("row 0 unexpected: %+v", rows[0])
	}
	if rows[2].Load != "masked" {
		t.Errorf("row 2 load: want masked, got %q", rows[2].Load)
	}
}

func TestParseSystemdUnitFiles(t *testing.T) {
	in := []byte(`ssh.service              enabled
cron.service             enabled
exim4.service            disabled
foo.service              static  enabled
weird-line-no-fields
`)
	got := parseSystemdUnitFiles(in)
	want := map[string]string{
		"ssh.service":   "enabled",
		"cron.service":  "enabled",
		"exim4.service": "disabled",
		"foo.service":   "static",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, got[k])
		}
	}
	if _, hadWeird := got["weird-line-no-fields"]; hadWeird {
		t.Errorf("single-field line should not produce a map entry")
	}
}

// writeStub creates a 0755 shell stub that emits the given body and
// returns its path. Used by integration-ish unit tests that exercise
// the backend's exec path without needing a real systemctl install.
func writeStub(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return path
}

func TestSystemdBackend_ListViaStub(t *testing.T) {
	// systemctl list-units json + list-unit-files plain. The stub
	// dispatches on the first argv to return the right canned body.
	stub := writeStub(t, "systemctl", `
case "$1" in
  list-units)
    cat <<'EOF'
[{"Unit":"ssh.service","Load":"loaded","Active":"active","Sub":"running","Description":"OpenBSD Secure Shell server"}]
EOF
    ;;
  list-unit-files)
    cat <<'EOF'
ssh.service              enabled
ufw.service              disabled
EOF
    ;;
esac
`)
	t.Setenv("WASH_SERVICES_SYSTEMCTL_BIN", stub)

	b := &systemdBackend{}
	services, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// ssh.service appears in both calls → merged; ufw.service only
	// in list-unit-files → synthesized as not-loaded.
	if len(services) != 2 {
		t.Fatalf("want 2 services, got %d: %+v", len(services), services)
	}
	var ssh, ufw *Service
	for i := range services {
		switch services[i].Name {
		case "ssh.service":
			ssh = &services[i]
		case "ufw.service":
			ufw = &services[i]
		}
	}
	if ssh == nil || ssh.Active != "active" || ssh.Enabled != "enabled" {
		t.Errorf("ssh row unexpected: %+v", ssh)
	}
	if ufw == nil || ufw.Active != "inactive" || ufw.Load != "not-loaded" || ufw.Enabled != "disabled" {
		t.Errorf("ufw row unexpected: %+v", ufw)
	}
}

func TestSystemdBackend_ActionArgv(t *testing.T) {
	t.Setenv("WASH_SERVICES_SYSTEMCTL_BIN", "systemctl")
	b := &systemdBackend{}
	cases := []struct {
		op, name string
		want     []string
	}{
		{"start", "ssh", []string{"systemctl", "start", "ssh"}},
		{"reload", "nginx", []string{"systemctl", "reload", "nginx"}},
		{"enable", "ufw", []string{"systemctl", "enable", "ufw"}},
		{"bogus", "x", nil},
	}
	for _, c := range cases {
		got := b.ActionArgv(c.op, c.name)
		if !slicesEqual(got, c.want) {
			t.Errorf("ActionArgv(%q,%q) = %v, want %v", c.op, c.name, got, c.want)
		}
	}
}

func slicesEqual(a, b []string) bool {
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
