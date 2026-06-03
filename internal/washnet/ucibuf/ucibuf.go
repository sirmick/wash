// Package ucibuf marshals the multi-package UCI a config renders to into one
// editable text buffer (and back) — the interchange the read → edit → apply CLI
// loop passes around. Each package is a section headed by `# ==== <pkg> ====`.
package ucibuf

import (
	"fmt"
	"sort"
	"strings"
)

const prefix = "# ==== "

// Marshal renders files (package → UCI text) into one buffer, packages sorted.
func Marshal(files map[string]string) string {
	pkgs := make([]string, 0, len(files))
	for p := range files {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	var b strings.Builder
	for _, p := range pkgs {
		fmt.Fprintf(&b, "%s%s ====\n%s", prefix, p, files[p])
		if !strings.HasSuffix(files[p], "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// HasMarkers reports whether s is a marshalled multi-package buffer (vs a raw
// single-package .uci file).
func HasMarkers(s string) bool { return strings.Contains(s, prefix) }

// Unmarshal splits a buffer back into package → UCI text. Lines before the first
// header are ignored.
func Unmarshal(s string) map[string]string {
	out := map[string]string{}
	var cur string
	var body strings.Builder
	flush := func() {
		if cur != "" {
			// Drop the blank separator line(s) Marshal puts between sections;
			// keep exactly one trailing newline.
			if t := strings.TrimRight(body.String(), "\n"); t != "" {
				out[cur] = t + "\n"
			} else {
				out[cur] = ""
			}
		}
		body.Reset()
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(strings.TrimSpace(line), "====") {
			flush()
			cur = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, prefix), "===="))
			continue
		}
		if cur != "" {
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	flush()
	return out
}
