// Package observe turns what the router already holds about a window into
// something a model can read (docs/COMMANDER.md §4): the tail of a pty's
// scrollback ring stripped of terminal control sequences and scrubbed of
// credential shapes, and the redaction every observation goes through
// before it leaves the router. Deterministic and testable on fixtures; it
// knows nothing about models.
package observe

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultTailBytes is how much of the ring an automatic observation reads;
// MaxTailBytes is what a caller may ask for. A pane's last screen or two
// says what it is doing; a whole 256 KiB ring says what it did an hour ago.
const (
	DefaultTailBytes = 16 << 10
	MaxTailBytes     = 64 << 10
)

// Terminal renders the last max bytes of raw pty output as text: escape
// sequences gone, carriage-return overwrites and backspaces resolved
// within a line, blank runs collapsed. truncated says the tail was cut at
// the front (the ring holds more).
//
// A full-screen program redraws cells rather than appending lines, so its
// tail is poor — that is what an app's own export is for (§4.5).
func Terminal(raw []byte, max int) (text string, truncated bool) {
	if max <= 0 || max > MaxTailBytes {
		max = DefaultTailBytes
	}
	if len(raw) > max {
		raw = raw[len(raw)-max:]
		truncated = true
		// Never start mid-rune or mid-escape: drop up to the first newline
		// when the cut landed inside a line.
		if i := bytes.IndexByte(raw, '\n'); i >= 0 && i < 512 {
			raw = raw[i+1:]
		}
	}
	lines := splitLines(strip(raw))
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n"), truncated
}

// strip removes ESC-introduced sequences (CSI, OSC, DCS, APC, PM, SOS and
// two-byte ESC x forms) and C0 controls other than \n, \r, \t, \b. The
// output is valid UTF-8: an invalid byte becomes U+FFFD rather than
// leaking a half rune to a model.
func strip(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		b := raw[i]
		switch {
		case b == 0x1b:
			i += skipEscape(raw[i:])
		case b == 0x9b: // 8-bit CSI
			i += skipCSI(raw[i+1:]) + 1
		case b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\b':
			i++
		case b == 0x7f:
			i++
		case b >= 0x80:
			r, n := utf8.DecodeRune(raw[i:])
			if r == utf8.RuneError && n == 1 {
				out = append(out, "�"...)
			} else {
				out = append(out, raw[i:i+n]...)
			}
			i += n
		default:
			out = append(out, b)
			i++
		}
	}
	return out
}

// skipEscape returns how many bytes an ESC-introduced sequence occupies,
// starting at the ESC.
func skipEscape(b []byte) int {
	if len(b) < 2 {
		return len(b)
	}
	switch b[1] {
	case '[':
		return 2 + skipCSI(b[2:])
	case ']', 'P', '_', '^', 'X':
		// OSC/DCS/APC/PM/SOS: to ST (ESC \) or BEL.
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return len(b)
	case '(', ')', '*', '+', '#', '%':
		// Charset / line-attr designators take one more byte.
		if len(b) >= 3 {
			return 3
		}
		return len(b)
	default:
		return 2
	}
}

// skipCSI returns the length of a CSI body: parameter and intermediate
// bytes up to and including the final byte 0x40–0x7E.
func skipCSI(b []byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return i + 1
		}
		if b[i] < 0x20 || b[i] > 0x3f {
			return i // malformed: stop before it
		}
	}
	return len(b)
}

// splitLines applies \r (overwrite from column 0) and \b (erase one rune)
// within a line, the way a terminal would, so a progress bar leaves one
// line and a corrected command reads corrected.
func splitLines(b []byte) []string {
	var lines []string
	var cur []rune
	col := 0
	flush := func() {
		lines = append(lines, string(cur))
		cur = cur[:0]
		col = 0
	}
	for _, r := range string(b) {
		switch r {
		case '\n':
			flush()
		case '\r':
			col = 0
		case '\b':
			if col > 0 {
				col--
			}
		case '\t':
			for {
				if col < len(cur) {
					cur[col] = ' '
				} else {
					cur = append(cur, ' ')
				}
				col++
				if col%8 == 0 {
					break
				}
			}
		default:
			if col < len(cur) {
				cur[col] = r
			} else {
				cur = append(cur, r)
			}
			col++
		}
	}
	if len(cur) > 0 || col > 0 {
		flush()
	}
	return lines
}

// secretish matches the shapes a credential takes in text that crosses a
// screen or a pipe: a bearer token, an assignment to something key/token/
// secret/password-shaped, vendor key prefixes, and AWS access keys. The
// same shapes wash-inference scrubs from CLI diagnostics.
var (
	// labelled: keep the label (and a "Bearer" scheme word), drop the value.
	// The label may be quoted (a JSON state blob: "password":"…") and the
	// value stops at a quote, so what surrounds it survives.
	labelled = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|passwd|authorization)["']?\s*[:=]\s*["']?(?:bearer\s+)?|bearer\s+)([^\s"']+)`)
	// bare: vendor key prefixes and AWS access keys, wherever they sit.
	bare = regexp.MustCompile(`\b(?:sk|sk-ant|sk-proj|ghp|gho|xox[abp])[-_][A-Za-z0-9_\-]{8,}|\bAKIA[0-9A-Z]{16}\b`)
)

// Redact replaces credential shapes so an observation can leave the router
// — even for an on-box model — without carrying a pasted token.
func Redact(s string) string {
	s = labelled.ReplaceAllString(s, "${1}[redacted]")
	return bare.ReplaceAllString(s, "[redacted]")
}
