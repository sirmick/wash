// Destination-name arithmetic: the "(copy)" suffix rule shared by fm's
// Duplicate verb and the "Keep both" answer to a copy/move conflict.
//
// Pure: nothing here touches the filesystem. The caller supplies a `taken`
// predicate (a readdir set, or an os.Lstat probe), which is what lets the
// same rule run inside the bulk worker and inside fm's BE before a job is
// even enqueued.
//
// The rule:
//
//	notes.txt        → notes (copy).txt → notes (copy 2).txt → …
//	notes (copy).txt → notes (copy 2).txt          (the suffix is not stacked)
//	archive.tar.gz   → archive (copy).tar.gz       (double extension kept whole)
//	.bashrc          → .bashrc (copy)              (a dotfile is all stem)
//	photos/          → photos (copy)               (a dir never has an extension)
package bulkops

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// copySuffixRe matches the suffix this package appends, so re-duplicating
// a copy counts up instead of stacking: " (copy)" or " (copy 7)" at the
// very end of the stem.
var copySuffixRe = regexp.MustCompile(`^(.*) \(copy(?: (\d+))?\)$`)

// splitName divides a name into the stem the suffix attaches to and the
// extension that stays on the end. Directories have no extension; a
// leading-dot name with no other dot is all stem; a ".tar.*" pair is kept
// together so "x.tar.gz" doesn't become "x.tar (copy).gz".
func splitName(name string, isDir bool) (stem, ext string) {
	if isDir {
		return name, ""
	}
	ext = filepath.Ext(name)
	if ext == "" || ext == name {
		return name, "" // no dot at all, or a bare dotfile like ".bashrc"
	}
	stem = strings.TrimSuffix(name, ext)
	if inner := filepath.Ext(stem); strings.EqualFold(inner, ".tar") && inner != stem {
		stem, ext = strings.TrimSuffix(stem, inner), inner+ext
	}
	return stem, ext
}

// CopyName is the nth copy-name for `name`: n=1 gives "x (copy).txt",
// n=2 "x (copy 2).txt", and so on. An existing "(copy)" / "(copy N)"
// suffix on the input is replaced rather than appended to. n < 1 is
// treated as 1.
func CopyName(name string, isDir bool, n int) string {
	if n < 1 {
		n = 1
	}
	stem, ext := splitName(name, isDir)
	if m := copySuffixRe.FindStringSubmatch(stem); m != nil {
		stem = m[1]
	}
	if n == 1 {
		return stem + " (copy)" + ext
	}
	return stem + " (copy " + strconv.Itoa(n) + ")" + ext
}

// UniqueCopyName is CopyName counted up until `taken` says the name is
// free. `taken` is asked about bare names (not paths), so the caller
// decides which directory it is probing.
func UniqueCopyName(name string, isDir bool, taken func(string) bool) string {
	for n := 1; ; n++ {
		candidate := CopyName(name, isDir, n)
		if taken == nil || !taken(candidate) {
			return candidate
		}
	}
}
