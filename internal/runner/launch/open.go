// `wash open <path>` — the desktop's open verb, from a terminal.
//
// docs/Review-findings.md, cross-app: "`wash open <path>`, `xdg-open`,
// `$EDITOR`, `$BROWSER` from a wash terminal (only `wash-edit --open <abs>`
// works, undocumented)". Every desktop has this verb; wash had the routing
// (manifest `Opens`, IMAGES.md) and no way to reach it from a shell.
//
// It does NOT start the app itself. The router refuses an attach whose
// binary does not match the registered app_id, and only the router knows
// which binary that is — so `wash open` hands the path to the wash-term
// window it was typed in ($WASH_TERM_INSTANCE, over $WASH_DISPLAY's
// control socket) and lets that BE do the opening. That half already
// holds CapOpen and CapSpawn, and already does exactly this for a path
// clicked in the terminal, so a click, `wash open` and `xdg-open` are one
// code path and one answer.
//
// The handler NAME is still resolved here, from the same registry the
// router consults, purely so the command can say what it chose.
//
// `xdg-open` is this same code: the shim wash-term puts on PATH is a
// symlink to the multicall binary named `xdg-open`, so argv[0] dispatch
// lands here (cmd/wash/main.go). A script would have to re-quote its
// arguments; a symlink cannot get that wrong.
package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/sirmick/wash/pkg/apps/registry"
)

// dirHandler is the app a DIRECTORY opens in. Directories have no
// extension for the manifest `Opens` tables to match, so the choice is
// named here rather than resolved — the same call wash-term's own path
// links make.
const dirHandler = "wash-fm"

// RunOpen implements `wash open <path|url>` (and the xdg-open shim).
// Returns a process exit code.
func RunOpen(args []string) int {
	var target string
	for _, a := range args {
		switch {
		case a == "--help" || a == "-h":
			writeOpenUsage(os.Stdout)
			return 0
		case a == "--version":
			fmt.Println("wash open")
			return 0
		case strings.HasPrefix(a, "-") && len(a) > 1 && target == "":
			// xdg-open is called with flags this doesn't implement
			// (--manual, -t). Ignoring them beats refusing to open the
			// file, which is what the caller actually wanted.
			continue
		default:
			if target == "" {
				target = a
			}
		}
	}
	if target == "" {
		writeOpenUsage(os.Stderr)
		return 2
	}
	if isURL(target) {
		return openURL(target)
	}
	return openPath(target)
}

func writeOpenUsage(w *os.File) {
	fmt.Fprintln(w, "usage: wash open <path|url>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Opens a file in whichever wash app registered its extension, a")
	fmt.Fprintln(w, "directory in the file manager, and a URL in $BROWSER.")
	fmt.Fprintln(w, "Run from a wash terminal (needs $WASH_DISPLAY).")
}

// isURL is deliberately narrow: a scheme wash might hand to a browser.
// A Windows-style drive letter or a relative path with a colon in it must
// not be mistaken for one.
func isURL(s string) bool {
	for _, p := range []string{"http://", "https://", "ftp://", "mailto:"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// openURL says what it chose, because there is nothing obvious to choose:
// no wash app registers a URL scheme (there is no wash browser), so the
// only answer is the user's own $BROWSER. Saying so is the point — a
// silent no-op here is the worst outcome.
func openURL(url string) int {
	browser := strings.TrimSpace(os.Getenv("BROWSER"))
	if browser == "" {
		fmt.Fprintf(os.Stderr, "wash open: no wash app handles URLs, and $BROWSER is unset — not opening %s\n", url)
		fmt.Fprintln(os.Stderr, "  (set BROWSER=<command>; wash windows opened from a page use the page's own browser)")
		return 1
	}
	argv := browserArgv(browser, url)
	if len(argv) == 0 {
		fmt.Fprintf(os.Stderr, "wash open: $BROWSER is empty — not opening %s\n", url)
		return 1
	}
	fmt.Printf("wash open: %s → %s\n", url, strings.Join(argv, " "))
	return spawnDetached(argv[0], argv)
}

// browserArgv turns $BROWSER into a command line. $BROWSER is a command
// in the XDG sense; the two forms in the wild are a bare command (append
// the URL) and one carrying a %s placeholder (substitute it), and getting
// the second wrong opens the browser's home page instead of the link.
func browserArgv(browser, url string) []string {
	fields := strings.Fields(browser)
	if len(fields) == 0 {
		return nil
	}
	argv := make([]string, 0, len(fields)+1)
	sawPlaceholder := false
	for _, f := range fields {
		if strings.Contains(f, "%s") {
			f = strings.ReplaceAll(f, "%s", url)
			sawPlaceholder = true
		}
		argv = append(argv, f)
	}
	if !sawPlaceholder {
		argv = append(argv, url)
	}
	return argv
}

// openPath resolves the file, picks its handler, and starts it.
func openPath(path string) int {
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash open: %s: %v\n", path, err)
		return 1
	}
	fi, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash open: %s: %v\n", path, err)
		return 1
	}
	name := dirHandler
	if !fi.IsDir() {
		name = handlerFor(abs)
		if name == "" {
			fmt.Fprintf(os.Stderr, "wash open: no wash app handles %q\n", filepath.Ext(abs))
			if exts := knownExts(); exts != "" {
				fmt.Fprintf(os.Stderr, "  handled: %s\n", exts)
			}
			return 1
		}
	}
	sock := os.Getenv("WASH_DISPLAY")
	if sock == "" {
		sock = os.Getenv("WASH_CONTROL_SOCKET")
	}
	inst := os.Getenv("WASH_TERM_INSTANCE")
	if sock == "" || inst == "" {
		fmt.Fprintln(os.Stderr, "wash open: not inside a wash terminal ($WASH_DISPLAY / $WASH_TERM_INSTANCE unset)")
		return 1
	}
	req := map[string]any{
		"t":           "msg",
		"instance_id": inst,
		"data":        map[string]any{"kind": "path_open", "path": abs},
	}
	resp, code := roundtrip(sock, 5, req)
	if code != 0 {
		return code
	}
	if t, _ := resp["t"].(string); t == "error" {
		return printErrAndExit(resp)
	}
	fmt.Printf("wash open: %s → %s\n", abs, name)
	return 0
}

// handlerFor is the router's resolveOpen, over the same registry: the
// first enabled app whose manifest claims this extension. Iteration order
// of a map is not stable, so ties are broken by app name rather than by
// whatever the runtime felt like — an unstable answer would be worse than
// an arbitrary one.
func handlerFor(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}
	var matches []string
	for name, a := range registry.All() {
		for _, o := range a.Manifest.Opens {
			if strings.ToLower(o) == ext {
				matches = append(matches, name)
				break
			}
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

// knownExts is the "handled:" line on a miss — a list beats a bare no.
func knownExts() string {
	seen := map[string]bool{}
	var out []string
	for _, a := range registry.All() {
		for _, o := range a.Manifest.Opens {
			e := strings.ToLower(o)
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// inheritedIdentity are the env vars that say WHICH app and instance the
// process is. A shell inside a wash terminal inherits wash-term's copies
// (the router sets WASH_APP_ID on every app it spawns, and the pty passes
// the environment straight through), so a child started from that shell
// would introduce itself to the router as wash-term — with wash-term's
// attach token — and be refused or, worse, believed. The path stays
// (WASH_DISPLAY, WASH_PROTO); the identity does not.
var inheritedIdentity = []string{"WASH_APP_ID", "WASH_INSTANCE_ID", "WASH_ATTACH_TOKEN"}

func childEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range inheritedIdentity {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// spawnDetached starts bin in its own session with argv (argv[0] included,
// which is how the app name is carried), detached from this terminal: a
// window opened from a shell must outlive the command that opened it, and
// its logs must not scribble over the shell's output.
func spawnDetached(bin string, argv []string) int {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash open: %v\n", err)
		return 1
	}
	defer devnull.Close()
	cmd := &exec.Cmd{
		Path:        bin,
		Args:        argv,
		Env:         childEnv(),
		Stdin:       devnull,
		Stdout:      devnull,
		Stderr:      devnull,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if !filepath.IsAbs(bin) {
		p, err := exec.LookPath(bin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wash open: %s: %v\n", bin, err)
			return 1
		}
		cmd.Path = p
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wash open: start %s: %v\n", bin, err)
		return 1
	}
	// Release rather than Wait: the point is to hand the window over and
	// give the shell its prompt back.
	_ = cmd.Process.Release()
	return 0
}
