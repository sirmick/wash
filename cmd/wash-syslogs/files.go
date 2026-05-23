package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/sdk"
)

// LogFile is one row in the sidebar — a regular file directly under
// /var/log/ that smells like text and isn't an archived rotation.
type LogFile struct {
	Path  string `cbor:"path" json:"path"`
	Name  string `cbor:"name" json:"name"`
	Size  int64  `cbor:"size" json:"size"`
	MTime int64  `cbor:"mtime" json:"mtime"` // unix seconds
}

// pushFiles enumerates /var/log/* once and ships the list to the FE.
// Best-effort: an unreadable /var/log returns an empty list (the FE
// will then show its empty state and the user can click "Refresh" or
// relaunch via Root System Logs).
func pushFiles(c *sdk.Conn) {
	files := listLogFiles("/var/log")
	if err := c.SendAppMsg(map[string]any{
		"kind":  "files",
		"files": files,
	}); err != nil {
		log.Printf("wash-syslogs files send: %v", err)
	}
}

// listLogFiles walks the given directory non-recursively, plus one
// level into subdirs (so /var/log/nginx/access.log shows up). Rotated
// archives (*.gz, *.xz, *.bz2, *.[0-9]) are skipped — they don't
// support `tail -F` correctly and would clutter the sidebar with the
// same name × 10 every distro keeps around.
func listLogFiles(root string) []LogFile {
	out := walkLogDir(root, true)
	// Newest first — sysadmins typically want the file that's been
	// updated most recently. Ties broken by name for determinism.
	sort.Slice(out, func(i, j int) bool {
		if out[i].MTime != out[j].MTime {
			return out[i].MTime > out[j].MTime
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// walkLogDir lists log files in dir. If descend, also enters one
// level of subdirectories (e.g. /var/log/nginx/, /var/log/apache2/).
// Symlinks aren't followed — most distros symlink current → rotated
// and we don't want loops.
func walkLogDir(dir string, descend bool) []LogFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("wash-syslogs read %s: %v", dir, err)
		return nil
	}
	var out []LogFile
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			if descend {
				out = append(out, walkLogDir(full, false)...)
			}
			continue
		}
		if !looksLikeLogFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// /var/log/lastlog and /var/log/wtmp are sparse binary
		// utmp files — name-based filter already drops them, but
		// belt-and-braces on size==0 too (an empty file is a
		// useless row for tailing).
		out = append(out, LogFile{
			Path:  full,
			Name:  displayName(full),
			Size:  info.Size(),
			MTime: info.ModTime().Unix(),
		})
	}
	return out
}

// displayName renders a path like /var/log/nginx/access.log as
// "nginx/access.log" — keeps the subdir context without the long
// /var/log/ prefix every row would otherwise carry.
func displayName(full string) string {
	rel := strings.TrimPrefix(full, "/var/log/")
	if rel == full { // wasn't under /var/log; fall back to basename
		return filepath.Base(full)
	}
	return rel
}

// looksLikeLogFile is a name-only filter. We can't open every file
// to sniff for binary content cheaply — and most syslog daemons name
// their outputs predictably. False negatives (skipping an oddly-
// named log) are recoverable via the user typing a path; false
// positives waste an inotify watcher at worst.
func looksLikeLogFile(name string) bool {
	// Known binary / structured non-text files we don't want.
	switch name {
	case "lastlog", "wtmp", "btmp", "journal", "tallylog", "faillog":
		return false
	}
	// sysstat's binary reports under /var/log/sysstat/ are named
	// saDD (raw activity) / sarDD (text report compiled by sar -A).
	// Both look log-ish by name; both will produce non-UTF-8 bytes
	// when tailed — fxamacker/cbor on the router side rejects them
	// and tears the app's read loop. Skip them by name.
	if isSysstatBin(name) {
		return false
	}
	// Rotated archives: skip extensions that imply compression or
	// numeric rotation suffixes (".1", ".2.gz", ...).
	lower := strings.ToLower(name)
	for _, suf := range []string{".gz", ".xz", ".bz2", ".zst", ".zip"} {
		if strings.HasSuffix(lower, suf) {
			return false
		}
	}
	// Tail rotation suffix: `.1`, `.2`, ..., `.10`, ...
	if idx := strings.LastIndex(lower, "."); idx >= 0 {
		suf := lower[idx+1:]
		if len(suf) > 0 && allDigits(suf) {
			return false
		}
	}
	// Otherwise accept. We intentionally don't gate on `.log` — many
	// classic logs (auth.log, messages, syslog, dmesg) match no
	// extension at all.
	return true
}

// isSysstatBin matches "saDD" / "sarDD" (DD = two-digit day-of-month)
// — sysstat's daily binary activity files. /var/log/sysstat/sa05,
// /var/log/sysstat/sar22, etc.
func isSysstatBin(name string) bool {
	if (strings.HasPrefix(name, "sa") && len(name) == 4) ||
		(strings.HasPrefix(name, "sar") && len(name) == 5) {
		tail := name[len(name)-2:]
		return allDigits(tail)
	}
	return false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// currentUID returns the effective uid as an int. Used only in the
// startup log so the user can confirm a Root System Logs window
// actually launched as root.
func currentUID() int {
	return os.Geteuid()
}
