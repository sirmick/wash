package pty

import "testing"

// TestParseSSHTarget covers the argv shapes the tab status line relies
// on: plain host, user@host, and the option-skipping that keeps a flag
// value (e.g. "-p 2222") from being mistaken for the destination.
func TestParseSSHTarget(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"plain host", []string{"ssh", "files"}, "files"},
		{"user@host stripped", []string{"ssh", "mick@files"}, "files"},
		{"separate flag value skipped", []string{"ssh", "-p", "2222", "files"}, "files"},
		{"attached flag value", []string{"ssh", "-p2222", "files"}, "files"},
		{"identity flag then host", []string{"ssh", "-i", "/k/id", "root@harbor"}, "harbor"},
		{"long option before host", []string{"ssh", "-4", "files"}, "files"},
		{"no destination", []string{"ssh"}, ""},
		{"only options", []string{"ssh", "-p", "22"}, ""},
		{"empty argv", []string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseSSHTarget(c.args); got != c.want {
				t.Errorf("parseSSHTarget(%q) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}
