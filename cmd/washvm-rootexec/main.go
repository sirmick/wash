// Command washvm-rootexec is a setuid-root trampoline for the wash-vm guest.
//
// The guest desktop runs as the unprivileged 'wash' user, but com.wash.netd
// must write NetworkManager system-connection keyfiles — and NM's keyfile plugin
// silently ignores any connection file not owned by root. So netd can't be fully
// unprivileged. This trampoline is installed as /usr/lib/wash/wash-netd with the
// setuid bit: when the wash router exec's it to spawn the netd singleton, the
// kernel grants euid 0; we promote to a full root id and re-exec the multicall
// as netd (argv[0] basename drives multicall dispatch), forwarding args + env.
//
// It is intentionally minimal and only ever becomes the netd binary — it is NOT
// a general "run anything as root" helper.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func main() {
	if err := syscall.Setgid(0); err != nil {
		fmt.Fprintf(os.Stderr, "washvm-rootexec: setgid(0): %v\n", err)
	}
	if err := syscall.Setuid(0); err != nil {
		fmt.Fprintf(os.Stderr, "washvm-rootexec: setuid(0): %v\n", err)
	}
	argv := append([]string{"wash-netd"}, os.Args[1:]...)
	// Pre-exec trace: this is a setuid-root privilege boundary, so the
	// "about to exec as root" line must exist before control transfers.
	fmt.Fprintf(os.Stderr, "washvm-rootexec: uid=%d exec /usr/lib/wash/wash argv=%q\n",
		os.Getuid(), strings.Join(argv, " "))
	if err := syscall.Exec("/usr/lib/wash/wash", argv, os.Environ()); err != nil {
		os.Stderr.WriteString("washvm-rootexec: exec wash-netd: " + err.Error() + "\n")
		os.Exit(127)
	}
}
