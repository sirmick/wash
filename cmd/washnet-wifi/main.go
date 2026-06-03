// Command washnet-wifi performs a privileged NetworkManager wifi action
// (connect or forget) via nmcli — the polkit-gated half of wash's wifi runtime.
// netd handles scan/status itself (unprivileged), but connect/forget need root,
// so netd escalates them through wash-priv, which runs this CLI as root (the FE
// shows wash-priv's approval dialog). It prints "OK <detail>" on success and
// "ERR <reason>" to stderr with a non-zero exit on failure, so netd can surface
// the reason (e.g. a bad password) instead of a silent no-op.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirmick/wash/apps/netd/be/wifi"
)

func main() {
	op := flag.String("op", "", "connect|forget|radio")
	ssid := flag.String("ssid", "", "network SSID (connect/forget)")
	security := flag.String("security", wifi.SecNone, "none|psk2|sae")
	psk := flag.String("psk", "", "pre-shared key (psk2/sae)")
	hidden := flag.Bool("hidden", false, "connect to a hidden (non-broadcast) SSID")
	on := flag.Bool("on", false, "radio: turn Wi-Fi on (default off)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rt := wifi.New()

	var out string
	var err error
	switch *op {
	case "connect":
		if *ssid == "" {
			fail("connect: missing -ssid")
		}
		out, err = rt.Connect(ctx, *ssid, *security, *psk, *hidden)
	case "forget":
		if *ssid == "" {
			fail("forget: missing -ssid")
		}
		out, err = rt.Forget(ctx, *ssid)
	case "radio":
		out, err = rt.SetEnabled(ctx, *on)
	default:
		fail("unknown -op %q (want connect|forget|radio)", *op)
	}
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("OK %s\n", firstLine(out))
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "ERR "+format+"\n", a...)
	os.Exit(1)
}

// firstLine trims nmcli's chatter to its first non-empty line for a tidy ack.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return "done"
}
