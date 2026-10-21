package sshtarget

import "testing"

func TestValidate(t *testing.T) {
	for _, ok := range []string{
		"host", "host.example.com", "user@host", "me@10.0.0.7", "::1", "fe80::1%eth0",
		"[fe80::1]", "box-2.local", "alias_from_ssh_config", "under_score@host",
	} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v; want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "-host", "-ohostname=x", "user@-host", "-user@host", "@host", "user@",
		"host name", "host\tname", "host\n", "host;id", "host|x", "host$x", "host`x`",
		"host'x", `host"x`, "host/x", "host\\x", "host=x", "host,x",
	} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) = nil; want error", bad)
		}
	}
}
