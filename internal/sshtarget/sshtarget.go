// Package sshtarget validates a user- or network-supplied ssh target before
// it is placed in an ssh argv. A target that begins with "-" would be parsed
// by ssh as an option rather than a host, and whitespace or control bytes
// never belong in one. Every wash component that execs ssh with a host it did
// not mint itself (the remote supervisor, the SFTP mount, the watch service,
// the connect app's ssh-copy-id) validates here, and additionally passes "--"
// before the host so ssh stops option parsing regardless.
package sshtarget

import (
	"errors"
	"fmt"
	"strings"
)

// Validate reports whether host is safe to hand to ssh as its positional
// target: "[user@]host" where host is a DNS name, an IPv4/IPv6 literal, or an
// ssh_config alias. It rejects an empty target, a leading "-" on either the
// user or the host part, and any byte outside a conservative set
// (letters, digits, ".", "-", "_", ":", "%", "[", "]", one "@").
func Validate(host string) error {
	if host == "" {
		return errors.New("empty ssh target")
	}
	user, name := "", host
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		user, name = host[:i], host[i+1:]
	}
	if err := part("user", user, user != "" || strings.Contains(host, "@")); err != nil {
		return err
	}
	return part("host", name, true)
}

func part(what, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("ssh target: empty %s", what)
		}
		return nil
	}
	if s[0] == '-' {
		return fmt.Errorf("ssh target: %s %q must not begin with '-'", what, s)
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == ':', c == '%', c == '[', c == ']':
		default:
			return fmt.Errorf("ssh target: %s %q contains %q", what, s, c)
		}
	}
	return nil
}
