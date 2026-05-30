package vscode

// server.go — the single code-server process and its single ingress
// route, owned by the manager window for as long as it's open. Closing
// the manager kills code-server (it's our child; Pdeathsig + group-kill
// guarantee it). Workbench windows iframe the published path.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// dataHome returns code-server's user-data/extensions parent. Under the
// session sandbox when confined; HOME-relative otherwise.
func (m *manager) dataHome() string {
	if m.root != "" {
		return filepath.Join(m.root, ".wash", "vscode")
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "wash", "vscode")
}

// socketPath is the host-visible unix socket the router dials. Outside
// the session sandbox (the router isn't confined), per-instance so two
// sessions on one host don't collide.
func (m *manager) socketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "wash-vscode-"+sanitize(m.instanceID)+".sock")
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// lastFolderPath stores the most recently opened workspace folder so
// the manager can reopen it on next launch.
func (m *manager) lastFolderPath() string { return filepath.Join(m.dataHome(), "last-folder") }

func (m *manager) readLastFolder() string {
	b, err := os.ReadFile(m.lastFolderPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (m *manager) writeLastFolder(folder string) {
	if folder == "" {
		return
	}
	_ = os.MkdirAll(m.dataHome(), 0o755)
	_ = os.WriteFile(m.lastFolderPath(), []byte(folder), 0o644)
}

// defaultFolder is the workspace code-server opens by default.
func (m *manager) defaultFolder() string {
	if m.root != "" {
		return m.root
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "/"
}

// launch ensures code-server is running and its ingress published,
// returning the public path. Idempotent; serialized so concurrent
// ensures from multiple windows don't double-spawn.
func (m *manager) launch(ctx context.Context) (string, error) {
	m.launchMu.Lock()
	defer m.launchMu.Unlock()

	st := detect()
	if !st.Installed {
		return "", fmt.Errorf("code-server is not installed")
	}

	m.mu.Lock()
	if m.proc != nil && m.path != "" {
		path := m.path
		m.mu.Unlock()
		return path, nil
	}
	m.mu.Unlock()

	sock := m.socketPath()
	_ = os.Remove(sock)
	data := m.dataHome()
	if err := os.MkdirAll(filepath.Join(data, "extensions"), 0o755); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}

	args := []string{
		"--auth", "none",
		"--disable-telemetry",
		"--disable-update-check",
		"--socket", sock,
		"--user-data-dir", filepath.Join(data, "user-data"),
		"--extensions-dir", filepath.Join(data, "extensions"),
		m.defaultFolder(),
	}
	cmd := exec.Command(st.Binary, args...)
	// Scrub VS Code's injected env (VSCODE_IPC_HOOK_CLI et al). If wash
	// runs from a VS Code integrated terminal, inheriting them makes
	// code-server defer "open" to the outer editor and exit.
	cmd.Env = append(scrubVSCodeEnv(os.Environ()), "DISABLE_TELEMETRY=true")
	cmd.Dir = m.defaultFolder()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Own process group so we can kill the whole tree (node children),
	// and Pdeathsig so the kernel reaps code-server if we die abruptly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start code-server: %w", err)
	}

	m.mu.Lock()
	m.proc = cmd
	m.sock = sock
	m.mu.Unlock()
	go m.watch(cmd, sock)

	if err := waitHealthy(ctx, sock, 30*time.Second); err != nil {
		killTree(cmd)
		return "", fmt.Errorf("code-server did not become healthy: %w", err)
	}

	path, err := m.bus.Conn().PublishIngress(ctx, "unix", sock)
	if err != nil {
		killTree(cmd)
		return "", fmt.Errorf("publish ingress: %w", err)
	}
	m.mu.Lock()
	m.path = path
	m.mu.Unlock()
	return path, nil
}

// stop tears the running child down and drops its ingress route.
func (m *manager) stop() {
	m.mu.Lock()
	proc, path := m.proc, m.path
	m.proc, m.path = nil, ""
	m.mu.Unlock()
	if path != "" {
		_ = m.bus.Conn().UnpublishIngress(path)
	}
	killTree(proc)
}

// killTree SIGKILLs the child's whole process group, falling back to
// the bare process.
func killTree(proc *exec.Cmd) {
	if proc == nil || proc.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(proc.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = proc.Process.Kill()
}

// watch reaps the child and, if it was the current one, clears state
// and notifies subscribers so windows can offer a relaunch.
func (m *manager) watch(cmd *exec.Cmd, sock string) {
	err := cmd.Wait()
	killTree(cmd) // sweep any lingering node children
	_ = os.Remove(sock)
	m.mu.Lock()
	current := m.proc == cmd
	path := m.path
	if current {
		m.proc, m.path = nil, ""
	}
	m.mu.Unlock()
	if !current {
		return
	}
	if path != "" {
		_ = m.bus.Conn().UnpublishIngress(path)
	}
	reason := "code-server exited"
	if err != nil {
		reason = err.Error()
	}
	m.broadcast(map[string]any{"kind": "exited", "reason": reason})
	m.broadcastStatus()
}

// scrubVSCodeEnv returns env with every VSCODE_* entry removed.
func scrubVSCodeEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "VSCODE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// waitHealthy polls /healthz over the unix socket until 200 or timeout.
func waitHealthy(ctx context.Context, sock string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}
