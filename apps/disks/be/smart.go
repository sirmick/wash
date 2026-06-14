package disks

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"

	"github.com/sirmick/wash/internal/sdk"
)

func itoa(n int) string { return strconv.Itoa(n) }

// SMART is per-disk and privileged: smartctl needs raw device access. It is
// therefore an on-demand action (the FE "Check health" button), NOT part of
// the poll loop — so a background Disks window never triggers a sudo prompt.
//
// Flow: FE → {kind:"smart", name} → BE spawns a goroutine (PrivRunInlineSync
// must not run on the callback goroutine — it would deadlock on its own reply
// stream) → smartctl -aj via wash-priv → parse → BE → {kind:"smart_ok"|
// "smart_err"}. The FE renders the badge + attribute table from the push.

type smartReq struct {
	Name string `json:"name"`
}

// SmartReport is the parsed result for one disk. Strings/ints kept flat so
// the FE renders without a smartctl-shaped parser of its own.
type SmartReport struct {
	Name        string      `json:"name"`
	Passed      bool        `json:"passed"` // overall health self-assessment
	HaveStatus  bool        `json:"have_status"`
	Model       string      `json:"model"`
	Serial      string      `json:"serial"`
	Firmware    string      `json:"firmware"`
	TempC       int         `json:"temp_c"`
	PowerOnHrs  int         `json:"power_on_hours"`
	PowerCycles int         `json:"power_cycles"`
	Attrs       []SmartAttr `json:"attrs"`
}

// SmartAttr is one normalized attribute row (ATA) or NVMe health field.
type SmartAttr struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Thresh     int    `json:"thresh"`
	Raw        string `json:"raw"`
	WhenFailed string `json:"when_failed"`
}

// smartMsg is the BE→FE envelope.
type smartMsg struct {
	Kind   string      `json:"kind"` // "smart_ok" | "smart_err"
	Name   string      `json:"name"`
	Report SmartReport `json:"report"`
	Error  string      `json:"error,omitempty"`
}

// registerSmart wires the on-demand SMART handler onto the bus.
func registerSmart(b *sdk.Bus) {
	sdk.HandleVoid(b, "smart", func(c *sdk.Conn, _ string, req smartReq) error {
		// Off the callback goroutine — see PrivRunInlineSync's contract.
		go runSmart(c, req.Name)
		return nil
	})
}

func runSmart(c *sdk.Conn, name string) {
	if os.Getenv("WASH_DISKS_SOURCE") == "fake" {
		_ = c.SendAppMsg(smartMsg{Kind: "smart_ok", Name: name, Report: fakeSmart(name)})
		return
	}
	r, err := c.PrivRunInlineSync(context.Background(),
		[]string{"smartctl", "-aj", "/dev/" + name},
		"read SMART health for "+name)
	if err != nil {
		log.Printf("wash-disks: smartctl dev=%s priv run: %v", name, err)
		_ = c.SendAppMsg(smartMsg{Kind: "smart_err", Name: name, Error: err.Error()})
		c.Fail("SMART check failed: "+name, err)
		return
	}
	// smartctl returns a non-zero bitmask exit even on benign warnings, but
	// still prints the JSON document; parse stdout regardless of Exit.
	if len(r.Stdout) == 0 {
		log.Printf("wash-disks: smartctl dev=%s no output exit=%d stderr=%q", name, r.Exit, r.Stderr)
		msg := "smartctl produced no output"
		if len(r.Stderr) > 0 {
			msg = string(r.Stderr)
		}
		_ = c.SendAppMsg(smartMsg{Kind: "smart_err", Name: name, Error: msg})
		c.Fail("SMART check failed: "+name, errors.New(msg))
		return
	}
	rep, perr := parseSmartJSON(r.Stdout)
	if perr != nil {
		log.Printf("wash-disks smart parse %s: %v", name, perr)
		_ = c.SendAppMsg(smartMsg{Kind: "smart_err", Name: name, Error: "could not parse smartctl output"})
		c.Fail("SMART check failed: "+name, errors.New("could not parse smartctl output"))
		return
	}
	rep.Name = name
	_ = c.SendAppMsg(smartMsg{Kind: "smart_ok", Name: name, Report: rep})
	// A failing self-assessment is a needs-attention event worth a toast
	// even though the panel shows the full report. A healthy result gets a
	// brief confirmation; no toast when the drive doesn't report status.
	switch {
	case rep.HaveStatus && !rep.Passed:
		c.Warn("SMART warning: "+name, "Drive reports failing health")
	case rep.HaveStatus:
		c.Info("SMART check passed: "+name, "")
	}
}

// smartctlJSON is the subset of `smartctl -aj` we read. Covers both ATA
// (ata_smart_attributes) and NVMe (nvme_smart_health_information_log).
type smartctlJSON struct {
	ModelName       string `json:"model_name"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	SmartStatus     *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount int `json:"power_cycle_count"`
	AtaSmartAttrs   *struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NvmeLog *struct {
		AvailableSpare   int `json:"available_spare"`
		PercentageUsed   int `json:"percentage_used"`
		MediaErrors      int `json:"media_errors"`
		UnsafeShutdowns  int `json:"unsafe_shutdowns"`
		CriticalWarning  int `json:"critical_warning"`
		DataUnitsWritten int `json:"data_units_written"`
	} `json:"nvme_smart_health_information_log"`
}

func parseSmartJSON(b []byte) (SmartReport, error) {
	var j smartctlJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return SmartReport{}, err
	}
	rep := SmartReport{
		Model:       j.ModelName,
		Serial:      j.SerialNumber,
		Firmware:    j.FirmwareVersion,
		PowerCycles: j.PowerCycleCount,
	}
	if j.SmartStatus != nil {
		rep.HaveStatus = true
		rep.Passed = j.SmartStatus.Passed
	}
	if j.Temperature != nil {
		rep.TempC = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		rep.PowerOnHrs = j.PowerOnTime.Hours
	}
	if j.AtaSmartAttrs != nil {
		for _, a := range j.AtaSmartAttrs.Table {
			rep.Attrs = append(rep.Attrs, SmartAttr{
				ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst,
				Thresh: a.Thresh, Raw: a.Raw.String, WhenFailed: a.WhenFailed,
			})
		}
	} else if j.NvmeLog != nil {
		// Synthesize attribute rows from the NVMe health log so the FE's
		// generic table renders both device classes the same way.
		n := j.NvmeLog
		rep.Attrs = []SmartAttr{
			{Name: "Available Spare %", Raw: itoa(n.AvailableSpare)},
			{Name: "Percentage Used %", Raw: itoa(n.PercentageUsed)},
			{Name: "Media Errors", Raw: itoa(n.MediaErrors)},
			{Name: "Unsafe Shutdowns", Raw: itoa(n.UnsafeShutdowns)},
			{Name: "Critical Warning", Raw: itoa(n.CriticalWarning)},
		}
	}
	return rep, nil
}
