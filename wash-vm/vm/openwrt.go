package vm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OpenWRT is a stock OpenWRT x86-64 VM driven over its serial console — the
// "Approach B" router harness (docs/NET-ROUTER-UI.md). Unlike the agent-initramfs
// distros, OpenWRT boots its OWN disk image (uci/fw4/odhcpd/dnsmasq already
// aboard), so we don't build an image: we boot it, drive the root console
// directly, and share the static washnet CLIs in over a small ext4 disk
// (mounted read-only at /mnt). This is the router half of the multi-VM truth test
// — wash configures a real, running OpenWRT gateway.
type OpenWRT struct {
	cmd  *exec.Cmd
	conn net.Conn
	dir  string
	seq  int
	buf  []byte
}

// OpenWRTOpts configures a launch. Disk is the Image-Builder OpenWRT image
// (build-vm-image-openwrt.sh builds it with the washnet CLIs baked into
// /usr/bin); a writable per-VM copy is booted so the base image is never mutated.
// Extra is appended to the qemu argv verbatim — this is where multi-VM L2 wiring
// (-netdev socket …) goes in M1.
type OpenWRTOpts struct {
	Disk  string
	Mem   string // default "256M"
	Extra []string
}

// LaunchOpenWRT boots the VM, waits for the root shell, and mounts the CLI disk.
func LaunchOpenWRT(ctx context.Context, o OpenWRTOpts) (*OpenWRT, error) {
	if o.Mem == "" {
		o.Mem = "256M"
	}
	dir, err := os.MkdirTemp("", "wash-owrt-")
	if err != nil {
		return nil, err
	}
	w := &OpenWRT{dir: dir}
	sock := filepath.Join(dir, "console.sock")
	disk := filepath.Join(dir, "disk.img") // writable copy: never touch the base
	if err := copyFile(o.Disk, disk); err != nil {
		w.Close()
		return nil, err
	}
	args := []string{
		"-machine", "q35", "-accel", "kvm", "-cpu", "host", "-m", o.Mem, "-smp", "1",
		"-display", "none", "-monitor", "none", "-no-reboot",
		"-drive", "file=" + disk + ",format=raw,if=virtio",
		"-serial", "unix:" + sock + ",server=on,wait=off",
	}
	args = append(args, o.Extra...)
	w.cmd = exec.CommandContext(ctx, "qemu-system-x86_64", args...)
	if os.Getenv("OWRT_DEBUG") != "" {
		w.cmd.Stderr = os.Stderr
	}
	if err := w.cmd.Start(); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.connect(ctx, sock); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.waitShell(ctx); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *OpenWRT) connect(ctx context.Context, sock string) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			w.conn = c
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("openwrt: serial socket never appeared at %s", sock)
}

// pump reads whatever is available within d into the rolling buffer.
func (w *OpenWRT) pump(d time.Duration) {
	_ = w.conn.SetReadDeadline(time.Now().Add(d))
	tmp := make([]byte, 65536)
	n, _ := w.conn.Read(tmp)
	if n > 0 {
		w.buf = append(w.buf, tmp[:n]...)
		if os.Getenv("OWRT_DEBUG") != "" {
			os.Stderr.Write(tmp[:n])
		}
	}
}

// waitShell waits for OpenWRT's auto-login root prompt. It does NOT poke the
// console during the boot window: the image's GRUB menu is an interactive serial
// TUI, and stray newlines land in it and derail the boot. Only after a grace
// period (boot should be done) does it nudge with Enter, in case the shell needs
// one to draw its prompt.
func (w *OpenWRT) waitShell(ctx context.Context) error {
	start := time.Now()
	deadline := start.Add(90 * time.Second)
	for time.Now().Before(deadline) {
		w.pump(1 * time.Second)
		if bytes.Contains(w.buf, []byte("root@OpenWrt")) {
			return nil
		}
		if time.Since(start) > 25*time.Second {
			_, _ = w.conn.Write([]byte("\n"))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("openwrt: root shell never appeared (boot failed?)\n%s", tailStr(w.buf, 800))
}

// Run executes one shell command and returns its combined output. Completion is
// detected by a unique marker echoed after the command; the marker is split in
// the sent line ("WASH""MKn") so the console's echo of the command itself never
// matches the contiguous marker we search for in the output.
func (w *OpenWRT) Run(ctx context.Context, cmd string) (string, error) {
	w.seq++
	marker := "WASHMK" + strconv.Itoa(w.seq)
	w.buf = w.buf[:0]
	line := cmd + ` 2>&1; echo "WASH""MK` + strconv.Itoa(w.seq) + `=$?"` + "\n"
	if _, err := w.conn.Write([]byte(line)); err != nil {
		return "", err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		w.pump(500 * time.Millisecond)
		if i := bytes.Index(w.buf, []byte(marker+"=")); i >= 0 {
			rc := 0
			fmt.Sscanf(string(w.buf[i+len(marker)+1:]), "%d", &rc)
			out := scrubConsole(string(w.buf[:i]))
			if rc != 0 {
				return out, fmt.Errorf("openwrt: %q exited %d:\n%s", cmd, rc, out)
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("openwrt: ctx ended waiting on %q: %w\nconsole tail:\n%s", cmd, ctx.Err(), tailStr(w.buf, 1200))
		}
	}
	return scrubConsole(string(w.buf)), fmt.Errorf("openwrt: command timed out: %q\nconsole tail:\n%s", cmd, tailStr(w.buf, 1200))
}

// MCastLAN returns qemu args attaching a virtio NIC to a shared L2 segment — a
// multicast "virtual hub" pinned to loopback (localaddr=127.0.0.1), so no host
// bridge/tap/root is involved and nothing leaks onto the real network. Every VM
// given the same group:port shares one Ethernet segment (VLAN tags ride the
// frames); mac must be unique per VM, id unique per NIC within a VM. This is the
// multi-VM wiring for M1+ (pass via OpenWRTOpts.Extra / vm.Opts.Extra).
func MCastLAN(id, group string, port int, mac string) []string {
	return []string{
		"-netdev", fmt.Sprintf("socket,id=%s,mcast=%s:%d,localaddr=127.0.0.1", id, group, port),
		"-device", "virtio-net-pci,netdev=" + id + ",mac=" + mac,
	}
}

// WriteFile writes content to a path in-guest via printf (busybox-native;
// OpenWRT ships no base64/heredoc-friendly tools), escaping for a double-quoted
// printf format so newlines/tabs survive. Sent in short chunks because a long
// single line overruns the serial console's line discipline (a ~700B config
// silently never completes).
func (w *OpenWRT) WriteFile(ctx context.Context, path, content string) error {
	esc := func(s string) string {
		for _, r := range [][2]string{
			{"\\", "\\\\"}, {"\"", "\\\""}, {"$", "\\$"}, {"`", "\\`"}, {"\t", "\\t"},
		} {
			s = strings.ReplaceAll(s, r[0], r[1])
		}
		return s
	}
	if _, err := w.Run(ctx, ": > "+path); err != nil { // truncate
		return err
	}
	chunk := ""
	flush := func() error {
		if chunk == "" {
			return nil
		}
		_, err := w.Run(ctx, "printf \""+chunk+"\" >> "+path)
		chunk = ""
		return err
	}
	for _, ln := range strings.Split(content, "\n") {
		add := esc(ln) + "\\n"
		if len(chunk)+len(add) > 280 {
			if err := flush(); err != nil {
				return err
			}
		}
		chunk += add
	}
	return flush()
}

// Close kills the VM and removes its scratch dir.
func (w *OpenWRT) Close() {
	if w.conn != nil {
		_ = w.conn.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_, _ = w.cmd.Process.Wait()
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
	}
}

// scrubConsole strips carriage returns and NULs so callers can do clean
// Contains/line checks on console output.
func scrubConsole(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\x00", "")
}

func tailStr(b []byte, n int) string {
	s := scrubConsole(string(b))
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
