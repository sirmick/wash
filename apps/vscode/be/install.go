package vscode

// install.go — detect/install/upgrade code-server, run as a real
// command under a PTY so the window can render live, package-manager-
// style output in an xterm. We drive our own shell script (not the
// official install.sh) to keep control of the cache layout, version
// pinning, and the atomic `current` symlink — while curl/tar still
// produce genuine terminal progress.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	creackpty "github.com/creack/pty"
)

const (
	// pinnedVersion is the known-good code-server wash installs by
	// default. Bumped deliberately; "update available" is offered
	// when GitHub reports something newer, but the user opts in.
	pinnedVersion = "4.107.0"

	githubLatestAPI = "https://api.github.com/repos/coder/code-server/releases/latest"
	releaseBaseURL  = "https://github.com/coder/code-server/releases/download"
)

func cacheRoot() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "wash", "code-server")
}

// codeServerArch maps GOARCH to coder's standalone-release suffix.
// ok=false on arches with no standalone build (incl. riscv64).
func codeServerArch() (string, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64", true
	case "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

type installState struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Binary    string `json:"binary"`
	Managed   bool   `json:"managed"`
	ArchOK    bool   `json:"arch_ok"`
}

// detect resolves which code-server wash will run: the managed cache
// `current` symlink first, then anything on PATH.
func detect() installState {
	st := installState{}
	_, st.ArchOK = codeServerArch()

	managed := filepath.Join(cacheRoot(), "current", "bin", "code-server")
	if fi, err := os.Stat(managed); err == nil && !fi.IsDir() {
		return installState{Installed: true, Binary: managed, Managed: true, ArchOK: st.ArchOK, Version: readVersion(managed)}
	}
	if p, err := exec.LookPath("code-server"); err == nil {
		st.Installed = true
		st.Binary = p
		st.Version = readVersion(p)
	}
	return st
}

func readVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[0], "v")
}

// latestVersion queries GitHub for the newest release tag. Best-effort;
// callers cache it (the daemon does) so we don't hit the 60/hr
// unauthenticated rate limit on every status request.
func latestVersion(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github releases: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	return strings.TrimPrefix(body.TagName, "v"), nil
}

// buildInstallScript renders the shell script that fetches and
// activates code-server version into the cache. Run under a PTY so
// curl's progress bar and tar's output stream live to the xterm. The
// final symlink flip is atomic (mv -T over the rename target), so a
// failed run never leaves `current` pointing at a half-extracted dir.
func buildInstallScript(version, arch, cache string) string {
	url := fmt.Sprintf("%s/v%s/code-server-%s-linux-%s.tar.gz", releaseBaseURL, version, version, arch)
	// Single-quoted heredoc-free script; values are safe (version/arch
	// validated, cache is our own path). %q-quote the interpolated
	// values defensively.
	return fmt.Sprintf(`set -e
VER=%q
ARCH=%q
CACHE=%q
URL=%q
echo "==> Installing code-server $VER ($ARCH) into $CACHE"
mkdir -p "$CACHE"
TMP="$CACHE/$VER.tar.gz"
echo "==> Downloading $URL"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar "$URL" -o "$TMP"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress "$URL" -O "$TMP"
else
  echo "neither curl nor wget found" >&2; exit 1
fi
echo "==> Extracting"
rm -rf "$CACHE/$VER.incoming"
mkdir -p "$CACHE/$VER.incoming"
tar -xzf "$TMP" --strip-components=1 -C "$CACHE/$VER.incoming"
rm -f "$TMP"
rm -rf "$CACHE/$VER"
mv "$CACHE/$VER.incoming" "$CACHE/$VER"
echo "==> Activating"
ln -sfn "$VER" "$CACHE/current.tmp"
mv -Tf "$CACHE/current.tmp" "$CACHE/current"
echo "==> Installed code-server $VER"
`, version, arch, cache, url)
}

// runInstallPTY runs the install script under a PTY and calls onChunk
// with each raw output slice as it arrives. Returns the script's exit
// error (nil on success). onChunk must not retain the slice.
func runInstallPTY(ctx context.Context, version string, onChunk func([]byte)) error {
	arch, ok := codeServerArch()
	if !ok {
		return fmt.Errorf("no standalone code-server build for %s; install code-server manually and it will be picked up from PATH", runtime.GOARCH)
	}
	if version == "" {
		version = pinnedVersion
	}
	script := buildInstallScript(version, arch, cacheRoot())
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = os.Environ()

	f, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		return fmt.Errorf("start install pty: %w", err)
	}
	buf := make([]byte, 8192)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			onChunk(buf[:n])
		}
		if rerr != nil {
			break // EOF or PTY closed when the child exits
		}
	}
	_ = f.Close()
	return cmd.Wait()
}
