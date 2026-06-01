// Command washvm-run boots a wash microvm and fronts it with the HTTP/WS proxy
// (docs/NET.md §8.2): the dev-server + e2e entrypoint and the VM-target product
// connector. It Launches the VM, waits for the guest agent, starts the proxy
// (serving the host chrome at / and tunnelling the guest wash-router at /ws),
// prints "wash-vm ready at <URL>", and blocks until interrupted.
//
//	washvm-run --kernel out/vm/vmlinuz --initramfs out/vm/initramfs.gz \
//	           --chrome web/shell/dist --addr 127.0.0.1:0
//
// The browser loads the chrome, the shell connects ws://<addr>/ws (its default,
// location-relative transport), and the proxy bridges that to the in-guest
// router over the serial data plane — the VM serves the real catalog, desktop,
// and app bundles (§8.3).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirmick/wash/wash-vm/vm"
)

func main() {
	kernel := flag.String("kernel", "out/vm/vmlinuz", "guest kernel image")
	initramfs := flag.String("initramfs", "out/vm/initramfs.gz", "guest initramfs")
	chrome := flag.String("chrome", "", "host chrome dir served at / (e.g. web/shell/dist); empty serves only /ws")
	addr := flag.String("addr", "127.0.0.1:0", "proxy listen address")
	mem := flag.String("mem", "1024M", "guest memory (NM + modules in the RAM initramfs)")
	bootTimeout := flag.Duration("boot-timeout", 30*time.Second, "guest readiness timeout")
	flag.Parse()

	if err := run(*kernel, *initramfs, *chrome, *addr, *mem, *bootTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "washvm-run:", err)
		os.Exit(1)
	}
}

func run(kernel, initramfs, chrome, addr, mem string, bootTimeout time.Duration) error {
	// Boot + readiness get their own bounded context; the proxy then runs until
	// a signal so a deadline here wouldn't tear the VM down mid-session.
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), bootTimeout)
	defer cancelBoot()

	machine, err := vm.Launch(bootCtx, vm.Opts{Kernel: kernel, Initramfs: initramfs, Mem: mem})
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	defer machine.Close()

	if err := machine.WaitReady(bootCtx); err != nil {
		return fmt.Errorf("guest not ready: %w", err)
	}

	p, err := machine.Proxy(vm.ProxyOpts{Static: chrome, Addr: addr})
	if err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	defer p.Close()

	// The line tests/tools grep for. Flush explicitly — callers read it to
	// learn the bound port (addr may be :0).
	fmt.Printf("wash-vm ready at %s\n", p.URL)
	os.Stdout.Sync()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Fprintln(os.Stderr, "washvm-run: shutting down")
	return nil
}
