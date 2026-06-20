package remote

import (
	"strings"
	"testing"
)

func TestMountPointFor(t *testing.T) {
	m := &mountManager{baseDir: "/base"}
	cases := []struct{ host, root, want string }{
		{"wash@10.77.0.2", "/home/user/project", "/base/wash_at_10.77.0.2/project"},
		{"host", "/", "/base/host/root"},
		{"user@h:2222", "/srv/data", "/base/user_at_h_2222/data"},
		{"host", "", "/base/host/root"}, // empty remote root → "root"
	}
	for _, c := range cases {
		root := c.root
		if root == "" {
			root = "/"
		}
		if got := m.mountPointFor(c.host, root); got != c.want {
			t.Errorf("mountPointFor(%q,%q) = %q; want %q", c.host, root, got, c.want)
		}
	}
}

func TestSanitizeHost(t *testing.T) {
	if got := sanitizeHost("wash@10.0.0.2:22"); got != "wash_at_10.0.0.2_22" {
		t.Errorf("sanitizeHost = %q; want wash_at_10.0.0.2_22", got)
	}
	// No path separator may survive — it would escape the mount base dir.
	if got := sanitizeHost("a/b@c"); strings.Contains(got, "/") {
		t.Errorf("sanitizeHost left a slash: %q", got)
	}
}
