package httpsec

import (
	"net/http/httptest"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		bindHost string
		allow    []string
		want     bool
	}{
		// Permissive default: no allowlist => everything passes.
		{"empty allowlist passes anything", "evil.example.com", "0.0.0.0", nil, true},
		{"empty allowlist passes lan ip", "10.0.0.5:11000", "0.0.0.0", nil, true},

		// Enforcing: loopback names always pass.
		{"localhost passes", "localhost:11000", "0.0.0.0", []string{"wash.lan"}, true},
		{"127.0.0.1 passes", "127.0.0.1", "0.0.0.0", []string{"wash.lan"}, true},
		{"ipv6 loopback passes", "[::1]:11000", "0.0.0.0", []string{"wash.lan"}, true},
		{"missing host passes", "", "0.0.0.0", []string{"wash.lan"}, true},

		// Enforcing: allowlist membership.
		{"listed host passes", "wash.lan:11000", "0.0.0.0", []string{"wash.lan"}, true},
		{"listed host case-insensitive", "WASH.LAN", "0.0.0.0", []string{"wash.lan"}, true},
		{"listed host trailing dot (fqdn)", "wash.lan.", "0.0.0.0", []string{"wash.lan"}, true},
		{"bind host always passes", "myhost:11000", "myhost", []string{"other.lan"}, true},

		// Enforcing: rejection (the rebinding shape).
		{"unlisted host rejected", "attacker.com", "0.0.0.0", []string{"wash.lan"}, false},
		{"unlisted lan ip rejected", "10.0.0.5:11000", "0.0.0.0", []string{"wash.lan"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HostAllowed(tt.host, tt.bindHost, tt.allow); got != tt.want {
				t.Errorf("HostAllowed(%q, %q, %v) = %v, want %v",
					tt.host, tt.bindHost, tt.allow, got, tt.want)
			}
		})
	}
}

func TestSetSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSecurityHeaders(rec.Header())
	want := map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}
