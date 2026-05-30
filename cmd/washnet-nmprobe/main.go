// Command washnet-nmprobe connects to NetworkManager over D-Bus and prints its
// status — the B4b smoke check that wash-net's pure-Go nm backend can actually
// talk to NM in the guest. Baked into the test image; run over the ctl plane.
package main

import (
	"fmt"
	"os"

	"github.com/sirmick/wash/apps/netd/be/nm"
)

func main() {
	c, err := nm.Connect()
	if err != nil {
		fmt.Println("CONNECT ERROR:", err)
		os.Exit(1)
	}
	defer c.Close()

	s, err := c.Status()
	if err != nil {
		fmt.Println("STATUS ERROR:", err)
		os.Exit(1)
	}

	fmt.Printf("OK NetworkManager %s\n", s.Version)
	fmt.Printf("  state=%s connectivity=%s\n", nm.StateName(s.State), nm.ConnectivityName(s.Connectivity))
	fmt.Printf("  devices=%d\n", len(s.Devices))
	for _, d := range s.Devices {
		fmt.Printf("    %-10s %-10s state=%d\n", d.Interface, nm.DeviceTypeName(d.Type), d.State)
	}
}
