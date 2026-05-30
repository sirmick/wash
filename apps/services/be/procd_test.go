package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProcdRunning(t *testing.T) {
	in := []byte(`{
		"network": {"instances": {"instance1": {"running": true}}},
		"dnsmasq": {"instances": {}},
		"firewall": {"instances": {"instance1": {"running": false}}},
		"odhcpd": {"instances": {"instance1": {"running": true}, "instance2": {"running": false}}}
	}`)
	got := parseProcdRunning(in)
	if !got["network"] {
		t.Errorf("network should be running")
	}
	if got["dnsmasq"] {
		t.Errorf("dnsmasq has no instances → not running")
	}
	if got["firewall"] {
		t.Errorf("firewall has one inactive instance → not running")
	}
	// Any-instance-running rule: odhcpd should count as running.
	if !got["odhcpd"] {
		t.Errorf("odhcpd has one running instance → should count")
	}
}

func TestParseProcdRunning_HandlesMalformedJSON(t *testing.T) {
	if got := parseProcdRunning([]byte("not json")); len(got) != 0 {
		t.Errorf("malformed JSON should return empty map, got %v", got)
	}
}

func TestProcdEnabled(t *testing.T) {
	dir := t.TempDir()
	// Synthetic /etc/rc.d/. Symlinks named S<N><svc> are enabled;
	// K-prefix (shutdown order) is ignored; bare files without an
	// S prefix are not enabled.
	for _, name := range []string{"S20network", "S99cron", "K90firewall", "Nogoprefix"} {
		path := filepath.Join(dir, name)
		if err := os.Symlink("/dev/null", path); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	t.Setenv("WASH_SERVICES_RCD_DIR", dir)

	got := procdEnabled()
	if !got["network"] {
		t.Errorf("S20network → network should be enabled, got %v", got)
	}
	if !got["cron"] {
		t.Errorf("S99cron → cron should be enabled")
	}
	if got["firewall"] {
		t.Errorf("K-prefix should not count as enabled")
	}
	if got["ogoprefix"] {
		t.Errorf("non-S prefix should not be parsed")
	}
}

func TestProcdBackend_ActionArgv(t *testing.T) {
	t.Setenv("WASH_SERVICES_INITD_DIR", "/etc/init.d")
	b := &procdBackend{}
	cases := []struct {
		op, name string
		want     []string
	}{
		{"start", "network", []string{"/etc/init.d/network", "start"}},
		{"reload", "dnsmasq", []string{"/etc/init.d/dnsmasq", "reload"}},
		{"enable", "firewall", []string{"/etc/init.d/firewall", "enable"}},
		{"bogus", "x", nil},
	}
	for _, c := range cases {
		got := b.ActionArgv(c.op, c.name)
		if !slicesEqual(got, c.want) {
			t.Errorf("ActionArgv(%q,%q) = %v, want %v", c.op, c.name, got, c.want)
		}
	}
}
