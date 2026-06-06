package disks

import "testing"

// `lvm fullreport --reportformat json --units b --nosuffix` shape: the report
// array is grouped per VG, each element carrying that VG's vg/pv/lv arrays.
const lvmFullreportJSON = `{
  "report": [
    {
      "vg": [ {"vg_name":"vg0","vg_size":"2040109465600","vg_free":"429496729600"} ],
      "pv": [ {"pv_name":"/dev/md0","vg_name":"vg0","pv_size":"2040109465600","pv_free":"429496729600"} ],
      "lv": [
        {"lv_name":"home","vg_name":"vg0","lv_size":"1073741824000","lv_attr":"-wi-ao----","lv_path":"/dev/vg0/home"},
        {"lv_name":"data","vg_name":"vg0","lv_size":"536870912000","lv_attr":"-wi-a-----","lv_path":"/dev/vg0/data"}
      ]
    }
  ]
}`

func TestParseLvmFullreport(t *testing.T) {
	// Mount table: home is mounted via the device-mapper name.
	mounts := map[string]mountInfo{
		"/dev/mapper/vg0-home": {point: "/home", fstype: "ext4", opts: "rw"},
	}
	vgs, err := parseLvmFullreport([]byte(lvmFullreportJSON), mounts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vgs) != 1 {
		t.Fatalf("vgs = %d", len(vgs))
	}
	vg := vgs[0]
	if vg.Name != "vg0" || vg.Size != 2040109465600 || vg.Free != 429496729600 {
		t.Fatalf("vg = %+v", vg)
	}
	if len(vg.PVs) != 1 || vg.PVs[0].Name != "md0" || vg.PVs[0].Path != "/dev/md0" {
		t.Fatalf("pvs = %+v", vg.PVs)
	}
	if len(vg.LVs) != 2 {
		t.Fatalf("lvs = %+v", vg.LVs)
	}
	home := vg.LVs[0]
	if home.Name != "home" || home.Size != 1073741824000 || home.Attr != "-wi-ao----" {
		t.Fatalf("home = %+v", home)
	}
	if home.Mount == nil || home.Mount.Point != "/home" {
		t.Fatalf("home mount via dm name not resolved: %+v", home.Mount)
	}
	if vg.LVs[1].Mount != nil {
		t.Fatalf("data should be unmounted: %+v", vg.LVs[1].Mount)
	}
}

func TestAtou(t *testing.T) {
	cases := map[string]uint64{
		"1024":         1024,
		"1024B":        1024,
		"<2048B":       2048,
		"  512 ":       512,
		"<1099511627776": 1099511627776,
	}
	for in, want := range cases {
		if got := atou(in); got != want {
			t.Errorf("atou(%q) = %d, want %d", in, got, want)
		}
	}
}
