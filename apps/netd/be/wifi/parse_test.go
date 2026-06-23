package wifi

import "testing"

func TestSplitTerse(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`*:home:75:WPA2`, []string{"*", "home", "75", "WPA2"}},
		{`:My\:Net:60:WPA3`, []string{"", "My:Net", "60", "WPA3"}},   // escaped colon in SSID
		{`*:back\\slash:42:`, []string{"*", `back\slash`, "42", ""}}, // escaped backslash
		{`:open:88:--`, []string{"", "open", "88", "--"}},
	}
	for _, c := range cases {
		got := splitTerse(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitTerse(%q) = %v (len %d), want %v", c.in, got, len(got), c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitTerse(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseScan(t *testing.T) {
	// home:net appears twice (two BSSIDs); the in-use/strongest row wins. The
	// empty-SSID row (hidden) is dropped. "--" security is open.
	out := "*:home\\:net:80:WPA2 WPA3\n" +
		":home\\:net:55:WPA2 WPA3\n" +
		":OpenCafe:60:--\n" +
		"::42:WPA2\n" +
		":Pixel:73:WPA2\n"
	aps := parseScan(out)
	if len(aps) != 3 {
		t.Fatalf("want 3 APs (hidden dropped, dup collapsed), got %d: %+v", len(aps), aps)
	}
	if aps[0].SSID != "home:net" || aps[0].Signal != 80 || !aps[0].InUse {
		t.Errorf("first AP = %+v, want SSID home:net signal 80 in-use", aps[0])
	}
	if aps[1].SSID != "OpenCafe" || aps[1].Security != "" {
		t.Errorf("OpenCafe should be open, got %+v", aps[1])
	}
	if aps[2].SSID != "Pixel" || aps[2].Security != "WPA2" {
		t.Errorf("Pixel = %+v, want SSID Pixel WPA2", aps[2])
	}
}

func TestParseActiveWifi(t *testing.T) {
	// NAME "home:net" arrives escaped as home\:net; the ethernet row is filtered.
	out := "Wired connection 1:802-3-ethernet:eth0\n" +
		"home\\:net:802-11-wireless:wlan0\n"
	conns := parseActiveWifi(out)
	if len(conns) != 1 {
		t.Fatalf("want 1 wifi conn, got %d: %+v", len(conns), conns)
	}
	if conns[0].Name != "home:net" || conns[0].Device != "wlan0" {
		t.Errorf("conn = %+v, want home:net/wlan0", conns[0])
	}
}

func TestNormalizeSecurity(t *testing.T) {
	for in, want := range map[string]string{"--": "", "": "", "WPA2": "WPA2", " WPA3 ": "WPA3", "WPA1 WPA2": "WPA1 WPA2"} {
		if got := normalizeSecurity(in); got != want {
			t.Errorf("normalizeSecurity(%q) = %q, want %q", in, got, want)
		}
	}
}
