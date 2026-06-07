package disks

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// lvmProvider reads LVM via `lvm fullreport` (one privileged call → one priv
// approval, vs. three for separate pvs/vgs/lvs). Privileged: the LVM reports
// read /run/lvm + device metadata that need root.
type lvmProvider struct{}

func init() { registerProvider(lvmProvider{}) }

func (lvmProvider) Name() string     { return "lvm" }
func (lvmProvider) Privileged() bool { return true }

// Detect is cheap + unprivileged: is the lvs tool installed? (Capability =
// "supported"; the actual VG list needs the privileged Collect.)
func (lvmProvider) Detect() bool { return toolOnPath("lvs") }

// fullreport columns are requested per sub-report so vg/pv/lv each get the
// fields we need. --units b --nosuffix → plain byte integers.
var lvmArgv = []string{
	"lvm", "fullreport", "--reportformat", "json", "--units", "b", "--nosuffix",
	"--configreport", "vg", "-o", "vg_name,vg_size,vg_free",
	"--configreport", "pv", "-o", "pv_name,vg_name,pv_size,pv_free",
	"--configreport", "lv", "-o", "lv_name,vg_name,lv_size,lv_attr,lv_path",
}

func (lvmProvider) ScanScript(_ context.Context) (string, bool) {
	return strings.Join(lvmArgv, " "), true
}

func (lvmProvider) ParseScan(out string) (Manager, bool, error) {
	vgs, err := parseLvmFullreport([]byte(out), readMounts())
	if err != nil {
		return Manager{}, false, err
	}
	if len(vgs) == 0 {
		return Manager{}, false, nil
	}
	objs := make([]any, len(vgs))
	for i, vg := range vgs {
		objs[i] = vg
	}
	return Manager{Kind: "lvm", Objects: objs}, true, nil
}

// lvmReport mirrors `lvm fullreport --reportformat json`. fullreport groups
// the report array by VG: each element carries one vg plus its pvs and lvs.
type lvmReport struct {
	Report []struct {
		VG []struct {
			Name string `json:"vg_name"`
			Size string `json:"vg_size"`
			Free string `json:"vg_free"`
		} `json:"vg"`
		PV []struct {
			Name string `json:"pv_name"`
			VG   string `json:"vg_name"`
			Size string `json:"pv_size"`
			Free string `json:"pv_free"`
		} `json:"pv"`
		LV []struct {
			Name string `json:"lv_name"`
			VG   string `json:"vg_name"`
			Size string `json:"lv_size"`
			Attr string `json:"lv_attr"`
			Path string `json:"lv_path"`
		} `json:"lv"`
	} `json:"report"`
}

func parseLvmFullreport(b []byte, mounts map[string]mountInfo) ([]LVMVG, error) {
	var r lvmReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []LVMVG
	for _, e := range r.Report {
		if len(e.VG) == 0 {
			continue
		}
		vg := LVMVG{
			Name: e.VG[0].Name,
			Size: atou(e.VG[0].Size),
			Free: atou(e.VG[0].Free),
		}
		for _, pv := range e.PV {
			vg.PVs = append(vg.PVs, LVMPV{
				Name: strings.TrimPrefix(pv.Name, "/dev/"),
				Path: pv.Name,
				Size: atou(pv.Size),
				Free: atou(pv.Free),
			})
		}
		for _, lv := range e.LV {
			l := LVMLV{
				Name: lv.Name,
				Path: lv.Path,
				Size: atou(lv.Size),
				Attr: lv.Attr,
			}
			l.Mount = lvMount(lv.Path, vg.Name, lv.Name, mounts)
			vg.LVs = append(vg.LVs, l)
		}
		out = append(out, vg)
	}
	return out, nil
}

// lvMount finds an LV's mount. /proc/mounts may list either the lv_path
// (/dev/vg/lv) or the device-mapper form (/dev/mapper/vg-lv, with literal
// dashes in names doubled). Try both.
func lvMount(lvPath, vg, lv string, mounts map[string]mountInfo) *Mount {
	candidates := []string{
		lvPath,
		"/dev/mapper/" + dmEscape(vg) + "-" + dmEscape(lv),
	}
	for _, c := range candidates {
		if mi, ok := mounts[c]; ok {
			m := &Mount{Point: mi.point, Opts: mi.opts}
			if total, used, avail, ok := statfsUsage(mi.point); ok {
				m.FSTotal, m.FSUsed, m.FSAvail = total, used, avail
			}
			return m
		}
	}
	return nil
}

// dmEscape doubles literal dashes, matching device-mapper's /dev/mapper naming.
func dmEscape(s string) string { return strings.ReplaceAll(s, "-", "--") }

// atou parses an lvm/zfs byte count, tolerating a leading '<' (approximation)
// and a trailing unit letter.
func atou(s string) uint64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimRight(s, "Bb")
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v
}
