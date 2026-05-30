// Command washvm-agent runs inside the microvm as the control-plane agent. It
// reads exec Requests off a raw serial port (default /dev/ttyS1) and writes back
// Responses — the out-of-band channel the host harness drives (docs/NET.md §8).
// Built static (CGO_ENABLED=0) and packed into the initramfs by
// scripts/build-vm-image-alpine.sh.
package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/sirmick/wash/internal/washvm/proto"
	"github.com/sirmick/wash/internal/wire"
)

func main() {
	ctlPort, dataPort := "/dev/ttyS1", "/dev/ttyS2"
	if len(os.Args) > 1 {
		ctlPort = os.Args[1]
	}
	if len(os.Args) > 2 {
		dataPort = os.Args[2]
	}
	f, err := os.OpenFile(ctlPort, os.O_RDWR, 0)
	if err != nil {
		// Nothing to talk to; nothing to do.
		os.Exit(1)
	}
	defer f.Close()

	// Both serial planes carry binary frames, so they must be raw — canonical
	// mode would line-buffer and mangle CR/LF and the frame would never arrive.
	// Don't rely on busybox stty; do it here.
	if err := makeRaw(int(f.Fd())); err != nil {
		os.Exit(1)
	}

	// Data plane: echo wash wire frames. This stands in for the in-guest
	// wash-router until B1 bakes it into the image; it proves the inside-out
	// transport (browser WS ⟷ proxy ⟷ serial ⟷ guest) end to end.
	if df, err := os.OpenFile(dataPort, os.O_RDWR, 0); err == nil {
		_ = makeRaw(int(df.Fd()))
		go echoFrames(df)
	}

	// Announce readiness guest→host. This direction is buffered by qemu, so it
	// avoids the host→guest UART-FIFO overflow race at startup (the host must
	// not send a request until it has seen this hello).
	if err := proto.WriteFrame(f, proto.Response{Out: "hello"}); err != nil {
		os.Exit(1)
	}

	for {
		var req proto.Request
		if err := proto.ReadFrame(f, &req); err != nil {
			// EOF / closed control plane: the VM is going away.
			return
		}
		resp := run(req)
		if err := proto.WriteFrame(f, &resp); err != nil {
			return
		}
	}
}

// echoFrames reads wash wire frames off the data plane and echoes them back —
// a minimal router stand-in.
func echoFrames(rwc *os.File) {
	t := wire.NewStreamTransport(rwc)
	for {
		fr, err := t.ReadFrame()
		if err != nil {
			return
		}
		if err := t.WriteFrame(fr); err != nil {
			return
		}
	}
}

// makeRaw puts the serial fd into raw mode (cfmakeraw equivalent).
func makeRaw(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

func run(req proto.Request) proto.Response {
	cmd := exec.Command("/bin/sh", "-c", req.Cmd)
	out, err := cmd.CombinedOutput()
	resp := proto.Response{ID: req.ID, Out: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			resp.Exit = ee.ExitCode()
		} else {
			resp.Exit = -1
			resp.Err = err.Error()
		}
	}
	return resp
}
