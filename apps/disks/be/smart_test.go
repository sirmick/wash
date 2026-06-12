package disks

import "testing"

// Trimmed but real-shaped `smartctl -aj` output for a SATA SSD.
const ataSmartJSON = `{
  "model_name": "Samsung SSD 860",
  "serial_number": "S3Z9NB0K123456",
  "firmware_version": "RVT04B6Q",
  "smart_status": { "passed": true },
  "temperature": { "current": 31 },
  "power_on_time": { "hours": 14213 },
  "power_cycle_count": 1872,
  "ata_smart_attributes": {
    "table": [
      { "id": 5, "name": "Reallocated_Sector_Ct", "value": 100, "worst": 100, "thresh": 10, "when_failed": "", "raw": { "value": 0, "string": "0" } },
      { "id": 194, "name": "Temperature_Celsius", "value": 69, "worst": 49, "thresh": 0, "when_failed": "", "raw": { "value": 31, "string": "31" } }
    ]
  }
}`

// NVMe smartctl output uses a different health log.
const nvmeSmartJSON = `{
  "model_name": "WD_BLACK SN770 1TB",
  "serial_number": "23110X800123",
  "firmware_version": "731030WD",
  "smart_status": { "passed": true },
  "temperature": { "current": 40 },
  "power_on_time": { "hours": 920 },
  "power_cycle_count": 311,
  "nvme_smart_health_information_log": {
    "available_spare": 100,
    "percentage_used": 2,
    "media_errors": 0,
    "unsafe_shutdowns": 18,
    "critical_warning": 0
  }
}`

func TestParseSmartATA(t *testing.T) {
	rep, err := parseSmartJSON([]byte(ataSmartJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HaveStatus || !rep.Passed {
		t.Fatalf("health = %+v", rep)
	}
	if rep.Model != "Samsung SSD 860" || rep.Serial != "S3Z9NB0K123456" {
		t.Fatalf("identity = %+v", rep)
	}
	if rep.TempC != 31 || rep.PowerOnHrs != 14213 || rep.PowerCycles != 1872 {
		t.Fatalf("metrics = %+v", rep)
	}
	if len(rep.Attrs) != 2 || rep.Attrs[0].ID != 5 || rep.Attrs[0].Raw != "0" {
		t.Fatalf("attrs = %+v", rep.Attrs)
	}
}

func TestParseSmartNVMe(t *testing.T) {
	rep, err := parseSmartJSON([]byte(nvmeSmartJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.TempC != 40 || rep.PowerOnHrs != 920 {
		t.Fatalf("nvme metrics = %+v", rep)
	}
	// NVMe health log → synthesized attribute rows.
	names := map[string]string{}
	for _, a := range rep.Attrs {
		names[a.Name] = a.Raw
	}
	if names["Percentage Used %"] != "2" || names["Available Spare %"] != "100" {
		t.Fatalf("nvme attrs = %+v", rep.Attrs)
	}
}

func TestParseSmartGarbage(t *testing.T) {
	if _, err := parseSmartJSON([]byte("not json")); err == nil {
		t.Fatal("expected error on garbage input")
	}
}
