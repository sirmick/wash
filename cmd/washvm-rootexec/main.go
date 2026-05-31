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
	"os"
	"syscall"
)

func main() {
	_ = syscall.Setgid(0)
	_ = syscall.Setuid(0)
	argv := append([]string{"wash-netd"}, os.Args[1:]...)
	if err := syscall.Exec("/usr/lib/wash/wash", argv, os.Environ()); err != nil {
		os.Stderr.WriteString("washvm-rootexec: exec wash-netd: " + err.Error() + "\n")
		os.Exit(127)
	}
}
