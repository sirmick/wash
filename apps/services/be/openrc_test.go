package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRcStatus(t *testing.T) {
	in := []byte(`Runlevel: default
[default]
sshd = started
crond = stopped
nginx = crashed
[sysinit]
udev = started

`)
	got := parseRcStatus(in)
	want := map[string]string{
		"sshd":  "started",
		"crond": "stopped",
		"nginx": "crashed",
		"udev":  "started",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, got[k])
		}
	}
}

func TestParseRcUpdate(t *testing.T) {
	in := []byte(`               sshd | default
              crond | default sysinit
              nginx |
       not-a-service
`)
	got := parseRcUpdate(in)
	if !got["sshd"] || !got["crond"] {
		t.Errorf("sshd + crond should be enabled, got %v", got)
	}
	if got["nginx"] {
		t.Errorf("nginx has empty runlevel column → should be disabled, got enabled")
	}
}

func TestApplyOpenRCState(t *testing.T) {
	running := map[string]string{"sshd": "started", "nginx": "crashed"}
	enabled := map[string]bool{"sshd": true}

	cases := []struct {
		name             string
		wantActive, wantSub, wantEnabled string
	}{
		{"sshd", "active", "started", "enabled"},
		{"nginx", "failed", "crashed", "disabled"},
		{"missing", "inactive", "stopped", "disabled"},
	}
	for _, c := range cases {
		s := Service{Name: c.name}
		applyOpenRCState(&s, running, enabled)
		if s.Active != c.wantActive || s.Sub != c.wantSub || s.Enabled != c.wantEnabled {
			t.Errorf("%s: got active=%q sub=%q enabled=%q; want %q/%q/%q",
				c.name, s.Active, s.Sub, s.Enabled,
				c.wantActive, c.wantSub, c.wantEnabled)
		}
	}
}

func TestOpenRCBackend_ListViaStubs(t *testing.T) {
	// Fake /etc/init.d with two script files (+ one dir that should
	// be skipped). rc-status / rc-update stubs canned to match.
	initd := t.TempDir()
	for _, f := range []string{"sshd", "nginx"} {
		if err := os.WriteFile(filepath.Join(initd, f), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := os.Mkdir(filepath.Join(initd, "skipme"), 0o755); err != nil {
		t.Fatalf("mkdir skipme: %v", err)
	}
	t.Setenv("WASH_SERVICES_INITD_DIR", initd)
	t.Setenv("WASH_SERVICES_RC_STATUS_BIN", writeStub(t, "rc-status", `
cat <<'EOF'
[default]
sshd = started
nginx = stopped
EOF
`))
	t.Setenv("WASH_SERVICES_RC_UPDATE_BIN", writeStub(t, "rc-update", `
case "$1" in
  show)
    cat <<'EOF'
               sshd | default
              nginx |
EOF
    ;;
esac
`))
	// rc-service just needs to exist for Detect() / future calls.
	t.Setenv("WASH_SERVICES_RC_SERVICE_BIN", writeStub(t, "rc-service", `exit 0`))

	b := &openrcBackend{}
	services, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("want 2 services (skipme dir excluded), got %d: %+v", len(services), services)
	}
	for _, s := range services {
		switch s.Name {
		case "sshd":
			if s.Active != "active" || s.Enabled != "enabled" {
				t.Errorf("sshd: got %+v", s)
			}
		case "nginx":
			if s.Active != "inactive" || s.Enabled != "disabled" {
				t.Errorf("nginx: got %+v", s)
			}
		}
	}
}

func TestOpenRCBackend_ActionArgv(t *testing.T) {
	t.Setenv("WASH_SERVICES_RC_SERVICE_BIN", "rc-service")
	t.Setenv("WASH_SERVICES_RC_UPDATE_BIN", "rc-update")
	b := &openrcBackend{}
	cases := []struct {
		op, name string
		want     []string
	}{
		{"start", "sshd", []string{"rc-service", "sshd", "start"}},
		{"restart", "nginx", []string{"rc-service", "nginx", "restart"}},
		{"enable", "sshd", []string{"rc-update", "add", "sshd"}},
		{"disable", "nginx", []string{"rc-update", "del", "nginx"}},
		{"reload", "sshd", nil}, // openrc lacks reload
		{"bogus", "x", nil},
	}
	for _, c := range cases {
		got := b.ActionArgv(c.op, c.name)
		if !slicesEqual(got, c.want) {
			t.Errorf("ActionArgv(%q,%q) = %v, want %v", c.op, c.name, got, c.want)
		}
	}
}
