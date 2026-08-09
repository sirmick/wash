package journal

import (
	"bufio"
	"bytes"
	"context"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
)

// Unit is one row in the sidebar. Description matches systemctl's
// short string so the user sees something close to what they'd see
// in a terminal.
type Unit struct {
	Name        string `json:"name"`
	Active      string `json:"active"` // active | inactive | failed | activating | ...
	Sub         string `json:"sub"`    // running | exited | dead | ...
	Description string `json:"description"`
}

// pushUnits runs `systemctl list-units` (without --user), parses the
// terse format, and ships the list to the FE. Best-effort — on a
// failed run we still push an empty list so the FE knows the call
// completed.
//
// We avoid `--output json` because old systemd's JSON support varies;
// the line-based format is identical going back to systemd 219.
func pushUnits(c *sdk.Conn) {
	units := listUnits()
	if err := c.SendAppMsg(map[string]any{
		"kind":  "units",
		"units": units,
	}); err != nil {
		log.Printf("wash-journal units send: %v", err)
	}
}

func listUnits() []Unit {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"systemctl", "list-units",
		"--type=service",
		"--all",
		"--no-pager",
		"--no-legend",
		"--plain")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("wash-journal list-units: %v", err)
		return nil
	}
	var units []Unit
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Five whitespace-separated columns:
		//   UNIT  LOAD  ACTIVE  SUB  DESCRIPTION
		// DESCRIPTION may contain spaces; the first four are single
		// tokens. Fields() collapses runs of whitespace so the
		// description tail rejoins easily.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		// `●` markers leak through when --plain isn't honoured by old
		// systemd; skip rows whose first column isn't a unit name.
		if !strings.HasSuffix(name, ".service") {
			continue
		}
		units = append(units, Unit{
			Name:        name,
			Active:      fields[2],
			Sub:         fields[3],
			Description: strings.Join(fields[4:], " "),
		})
	}
	return units
}
