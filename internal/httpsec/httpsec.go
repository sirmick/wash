// Package httpsec holds the small, shared HTTP-hardening primitives used
// by both the standalone router (internal/router) and the multi-user
// login front (internal/login): baseline security headers and a
// permissive-by-default Host-header allowlist (a DNS-rebinding defense).
//
// It exists because there are two consumers; a single-consumer version
// would live in that consumer.
package httpsec

import (
	"net"
	"net/http"
	"strings"
)

// SetSecurityHeaders stamps the baseline hardening headers onto a
// response served by wash itself. Do NOT apply it to the /app/<token>/
// ingress proxy — those responses belong to the embedded backend
// (code-server, jupyter, …) and carry that backend's own framing/CSP.
//
//   - X-Frame-Options: SAMEORIGIN — the shell may be framed only by
//     same-origin pages (it frames its own /app/ iframes; nobody frames
//     it). Blocks clickjacking of the desktop from a hostile site.
//   - X-Content-Type-Options: nosniff — assets carry explicit
//     Content-Types; don't let a browser MIME-sniff them into script.
//   - Referrer-Policy: same-origin — never leak the desktop URL (which
//     may carry a ?token=) to off-origin navigations.
//
// A blocking Content-Security-Policy is deliberately NOT set yet: the
// shell loads xterm, CodeMirror and Webamp, each needing some inline
// style / worker / blob allowance, so a wrong CSP bricks the desktop.
// That needs in-browser verification first — see docs/CORE_AUDIT.md §1.4.
func SetSecurityHeaders(h http.Header) {
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

// HostAllowed reports whether the request's Host header is acceptable for
// a TCP listener — a DNS-rebinding defense.
//
// Permissive by default: when allow is empty (the out-of-the-box state)
// every Host passes, so existing LAN / mDNS deployments keep working with
// no configuration. Enforcement is opt-in: once allow is non-empty, only
// the listed hostnames — plus the always-safe loopback names and the
// listener's own bind host — are accepted, and an off-list Host (the
// shape of a rebinding attack, where the attacker's DNS name resolves to
// a LAN IP) is rejected.
//
// hostHeader is r.Host (may include a :port); bindHost is the host part
// of the listen address ("" if unknown); allow is the configured list.
func HostAllowed(hostHeader, bindHost string, allow []string) bool {
	if len(allow) == 0 {
		return true // permissive default — no allowlist configured
	}
	h := normalizeHost(hostHeader)
	switch h {
	case "", "localhost", "127.0.0.1", "::1":
		return true // always-safe loopback names + missing Host
	}
	if bh := normalizeHost(bindHost); bh != "" && h == bh {
		return true
	}
	for _, a := range allow {
		if h == normalizeHost(a) {
			return true
		}
	}
	return false
}

// normalizeHost strips an optional :port, []-brackets around an IPv6
// literal, and a trailing FQDN dot, then lowercases — so allowlist
// comparison is case-, port-, and trailing-dot-insensitive.
func normalizeHost(s string) string {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSuffix(s, ".")
	return strings.ToLower(s)
}
