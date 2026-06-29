package radio

import (
	"os"
	"strings"
	"testing"
)

func TestStationConfigSeedsAndLoadsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	stations, err := loadStationConfig()
	if err != nil {
		t.Fatalf("load station config: %v", err)
	}
	if len(stations) == 0 {
		t.Fatal("expected seeded stations")
	}
	if !hasStation(stations, "SomaFM — DEF CON Radio") {
		t.Fatalf("seeded stations missing DEF CON Radio: %#v", stations[:min(len(stations), 5)])
	}
	if !hasStation(stations, "Nightride — DATAWAVE FM") {
		t.Fatal("seeded stations missing DATAWAVE FM")
	}

	data, err := os.ReadFile(stationConfigPath())
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	if !strings.Contains(string(data), `"stations"`) {
		t.Fatalf("seeded config does not use object schema: %s", data)
	}
}

func TestDecodeStationConfigAcceptsBareArrayAndSanitizes(t *testing.T) {
	stations, err := decodeStationConfig([]byte(`[
		{"name":"  Custom  ","url":"  http://example.test/stream  ","codec":" stream "},
		{"name":"No URL"}
	]`))
	if err != nil {
		t.Fatalf("decode bare array: %v", err)
	}
	if len(stations) != 1 {
		t.Fatalf("len = %d, want 1", len(stations))
	}
	if stations[0].Name != "Custom" || stations[0].URL != "http://example.test/stream" || stations[0].Codec != "stream" {
		t.Fatalf("station not sanitized: %+v", stations[0])
	}
}

func hasStation(stations []station, name string) bool {
	for _, st := range stations {
		if st.Name == name {
			return true
		}
	}
	return false
}
