// Package codec serializes a model.Config to native UCI text and parses it
// back. It is reflection-driven off the `uci` struct tags, so adding a model
// object (with the tags + UCIPackage/UCISection methods) makes it codec-aware
// with no codec changes. Tagged unions (uci:"opt,union") render the
// discriminator option plus the concrete variant's fields inlined into the same
// section. The round-trip Parse(Render(c)) == c is a commit gate (docs/NET.md
// §9, §11). Output is standard UCI export:
//
//	config interface 'lan'
//		option proto 'static'
//		option ipaddr '10.0.0.1/24'
//		list dns '1.1.1.1'
package codec

import (
	"bufio"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

type uciObject interface {
	UCIPackage() string
	UCISection() string
}

type uciTagger interface{ UCITag() string }

// Render serializes a Config to one UCI text blob per package, keyed by package
// name. Zero-valued optional fields are omitted, which makes the round-trip
// total: omitted ⇄ zero.
func Render(c model.Config) (map[string]string, error) {
	byPkg := map[string][]string{}
	cv := reflect.ValueOf(c)
	for i := 0; i < cv.NumField(); i++ {
		f := cv.Field(i)
		if f.Kind() != reflect.Slice {
			continue
		}
		for j := 0; j < f.Len(); j++ {
			elem := f.Index(j)
			obj, ok := elem.Interface().(uciObject)
			if !ok {
				continue
			}
			block, err := renderObject(elem, obj.UCISection())
			if err != nil {
				return nil, err
			}
			pkg := obj.UCIPackage()
			byPkg[pkg] = append(byPkg[pkg], block)
		}
	}
	out := make(map[string]string, len(byPkg))
	for pkg, blocks := range byPkg {
		out[pkg] = strings.Join(blocks, "\n")
	}
	return out, nil
}

func renderObject(v reflect.Value, section string) (string, error) {
	name, lines, err := collectFields(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if name != "" {
		fmt.Fprintf(&b, "config %s '%s'\n", section, name)
	} else {
		fmt.Fprintf(&b, "config %s\n", section)
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// collectFields walks a struct's `uci` tags, returning the section name (if a
// ,name field is present) and the option/list lines. Union fields recurse,
// inlining the variant's lines into the same section.
func collectFields(v reflect.Value) (name string, lines []string, err error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("uci")
		if tag == "" || tag == "-" {
			continue
		}
		optName, flags := parseTag(tag)
		fv := v.Field(i)
		switch {
		case hasFlag(flags, "name"):
			name = fv.String()
		case hasFlag(flags, "union"):
			if fv.IsNil() {
				continue
			}
			tagger, ok := fv.Interface().(uciTagger)
			if !ok {
				return "", nil, fmt.Errorf("union field %s does not implement UCITag", t.Field(i).Name)
			}
			tag := tagger.UCITag()
			// A v6-only DHCP interface renders as OpenWRT's distinct `proto
			// 'dhcpv6'` (inverse of normalizeDHCPv6), so it round-trips a real
			// box's wan6. Dual-stack/v4 stays `dhcp`.
			if d, ok := fv.Interface().(model.DHCPProto); ok && d.IPv6 && !d.IPv4 {
				tag = "dhcpv6"
			}
			lines = append(lines, fmt.Sprintf("\toption %s '%s'", optName, tag))
			_, sub, serr := collectFields(reflect.ValueOf(fv.Interface()))
			if serr != nil {
				return "", nil, serr
			}
			lines = append(lines, sub...)
		case hasFlag(flags, "list"):
			for _, s := range formatList(fv) {
				lines = append(lines, fmt.Sprintf("\tlist %s '%s'", optName, s))
			}
		default:
			if s, ok := formatScalar(fv); ok {
				lines = append(lines, fmt.Sprintf("\toption %s '%s'", optName, s))
			}
		}
	}
	return name, lines, nil
}

func formatScalar(fv reflect.Value) (string, bool) {
	switch x := fv.Interface().(type) {
	case netip.Addr:
		if !x.IsValid() {
			return "", false
		}
		return x.String(), true
	case netip.Prefix:
		if !x.IsValid() {
			return "", false
		}
		return x.String(), true
	}
	switch fv.Kind() {
	case reflect.String:
		s := fv.String()
		return s, s != ""
	case reflect.Bool:
		if !fv.Bool() {
			return "", false
		}
		return "1", true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := fv.Int()
		if n == 0 {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := fv.Uint()
		if n == 0 {
			return "", false
		}
		return strconv.FormatUint(n, 10), true
	}
	return "", false
}

func formatList(fv reflect.Value) []string {
	var out []string
	switch xs := fv.Interface().(type) {
	case []string:
		for _, s := range xs {
			if s != "" {
				out = append(out, s)
			}
		}
	case []netip.Addr:
		for _, a := range xs {
			if a.IsValid() {
				out = append(out, a.String())
			}
		}
	case []netip.Prefix:
		for _, p := range xs {
			if p.IsValid() {
				out = append(out, p.String())
			}
		}
	}
	return out
}

// binding maps a package/section key to the Config slice field and element type
// that receives parsed sections.
type binding struct {
	fieldIndex int
	elemType   reflect.Type
}

func registry() map[string]binding {
	reg := map[string]binding{}
	ct := reflect.TypeOf(model.Config{})
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		if f.Type.Kind() != reflect.Slice {
			continue
		}
		et := f.Type.Elem()
		obj, ok := reflect.New(et).Elem().Interface().(uciObject)
		if !ok {
			continue
		}
		reg[obj.UCIPackage()+"/"+obj.UCISection()] = binding{fieldIndex: i, elemType: et}
	}
	return reg
}

// Parse reconstructs a Config from per-package UCI text. Unknown sections are
// skipped (forward-compatible with config wash doesn't model yet).
func Parse(files map[string]string) (model.Config, error) {
	reg := registry()
	var c model.Config
	cv := reflect.ValueOf(&c).Elem()
	for pkg, text := range files {
		secs, err := parseSections(text)
		if err != nil {
			return c, fmt.Errorf("package %q: %w", pkg, err)
		}
		for _, sec := range secs {
			b, ok := reg[pkg+"/"+sec.typ]
			if !ok {
				continue
			}
			combineNetmask(sec.opts)
			normalizeDHCPv6(sec.opts)
			elem := reflect.New(b.elemType).Elem()
			if err := fillFields(elem, sec); err != nil {
				return c, fmt.Errorf("package %q section %q %q: %w", pkg, sec.typ, sec.name, err)
			}
			fld := cv.Field(b.fieldIndex)
			fld.Set(reflect.Append(fld, elem))
		}
	}
	return c, nil
}

// combineNetmask folds OpenWRT's split IPv4 addressing (`option ipaddr '1.2.3.4'`
// + `option netmask '255.255.255.0'`) into the single CIDR `ipaddr` wash's model
// expects — so a STOCK OpenWRT box (which ships split form) reads instead of
// parsing as empty. No-op unless a maskless ipaddr AND a netmask are both
// present. IPv6 always uses a prefix (`ip6addr 'fd00::1/64'`), so it needs no
// equivalent.
func combineNetmask(opts map[string]string) {
	addr, hasAddr := opts["ipaddr"]
	mask, hasMask := opts["netmask"]
	if !hasAddr || !hasMask || addr == "" || strings.Contains(addr, "/") {
		return
	}
	if n, ok := netmaskBits(mask); ok {
		opts["ipaddr"] = addr + "/" + strconv.Itoa(n)
	}
}

// normalizeDHCPv6 maps OpenWRT's distinct `proto 'dhcpv6'` (the v6-only DHCP
// uplink, e.g. wan6) onto wash's single dual-stack DHCP proto with the IPv6
// family flag — so it reads as DHCPProto{IPv6:true} instead of failing the union
// lookup (which aborted the whole network parse). The inverse is in collectFields
// (a v6-only DHCPProto renders back as `proto 'dhcpv6'`). UCI-specific impedance:
// OpenWRT splits dual-stack into two interfaces (dhcp + dhcpv6); wash uses one.
func normalizeDHCPv6(opts map[string]string) {
	if opts["proto"] == "dhcpv6" {
		opts["proto"] = "dhcp"
		opts["ipv6"] = "1"
		delete(opts, "ipv4") // dhcpv6 is v6-only
	}
}

// netmaskBits converts a dotted IPv4 netmask to its prefix length, rejecting a
// non-contiguous mask.
func netmaskBits(mask string) (int, bool) {
	a, err := netip.ParseAddr(mask)
	if err != nil || !a.Is4() {
		return 0, false
	}
	b := a.As4()
	m := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	ones, seenZero := 0, false
	for i := 31; i >= 0; i-- {
		if m&(1<<uint(i)) != 0 {
			if seenZero {
				return 0, false // ones after a zero — not a valid contiguous mask
			}
			ones++
		} else {
			seenZero = true
		}
	}
	return ones, true
}

func fillFields(v reflect.Value, sec section) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("uci")
		if tag == "" || tag == "-" {
			continue
		}
		name, flags := parseTag(tag)
		fv := v.Field(i)
		switch {
		case hasFlag(flags, "name"):
			fv.SetString(sec.name)
		case hasFlag(flags, "union"):
			tag, ok := sec.opts[name]
			if !ok {
				continue
			}
			vt, ok := model.VariantType(fv.Type(), tag)
			if !ok {
				return fmt.Errorf("unknown %s variant %q", name, tag)
			}
			variant := reflect.New(vt).Elem()
			if err := fillFields(variant, sec); err != nil {
				return err
			}
			fv.Set(variant)
		case hasFlag(flags, "list"):
			if vals, ok := sec.lists[name]; ok {
				if err := setList(fv, vals); err != nil {
					return err
				}
			}
		default:
			if s, ok := sec.opts[name]; ok {
				if err := setScalar(fv, s); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func setScalar(fv reflect.Value, s string) error {
	switch fv.Interface().(type) {
	case netip.Addr:
		a, err := netip.ParseAddr(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(a))
		return nil
	case netip.Prefix:
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(p))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		fv.SetBool(parseBool(s))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	}
	return nil
}

func setList(fv reflect.Value, vals []string) error {
	switch fv.Interface().(type) {
	case []string:
		fv.Set(reflect.ValueOf(append([]string(nil), vals...)))
	case []netip.Addr:
		addrs := make([]netip.Addr, 0, len(vals))
		for _, s := range vals {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return err
			}
			addrs = append(addrs, a)
		}
		fv.Set(reflect.ValueOf(addrs))
	case []netip.Prefix:
		ps := make([]netip.Prefix, 0, len(vals))
		for _, s := range vals {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return err
			}
			ps = append(ps, p)
		}
		fv.Set(reflect.ValueOf(ps))
	}
	return nil
}

// --- UCI text parsing ------------------------------------------------------

type section struct {
	typ, name string
	opts      map[string]string
	lists     map[string][]string
}

func parseSections(text string) ([]section, error) {
	var secs []section
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		toks := tokenize(line)
		switch toks[0] {
		case "config":
			if len(toks) < 2 {
				return nil, fmt.Errorf("malformed config line: %q", line)
			}
			s := section{typ: toks[1], opts: map[string]string{}, lists: map[string][]string{}}
			if len(toks) >= 3 {
				s.name = toks[2]
			}
			secs = append(secs, s)
		case "option":
			if len(secs) == 0 || len(toks) < 3 {
				return nil, fmt.Errorf("malformed option line: %q", line)
			}
			secs[len(secs)-1].opts[toks[1]] = toks[2]
		case "list":
			if len(secs) == 0 || len(toks) < 3 {
				return nil, fmt.Errorf("malformed list line: %q", line)
			}
			cur := &secs[len(secs)-1]
			cur.lists[toks[1]] = append(cur.lists[toks[1]], toks[2])
		default:
			return nil, fmt.Errorf("unknown directive: %q", line)
		}
	}
	return secs, sc.Err()
}

// tokenize splits a UCI line, honoring single-quoted values (which may contain
// spaces). It does not handle escaped quotes — UCI export doesn't emit them.
func tokenize(line string) []string {
	var toks []string
	for i := 0; i < len(line); {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		var tok strings.Builder
		if line[i] == '\'' {
			i++
			for i < len(line) && line[i] != '\'' {
				tok.WriteByte(line[i])
				i++
			}
			if i < len(line) {
				i++
			}
		} else {
			for i < len(line) && line[i] != ' ' && line[i] != '\t' {
				tok.WriteByte(line[i])
				i++
			}
		}
		toks = append(toks, tok.String())
	}
	return toks
}

func parseTag(tag string) (name string, flags []string) {
	parts := strings.Split(tag, ",")
	return parts[0], parts[1:]
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func parseBool(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
