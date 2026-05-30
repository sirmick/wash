// Package codec serializes a model.Config to native UCI text and parses it
// back. It is reflection-driven off the `uci` struct tags, so adding a model
// object (with the tags + UCIPackage/UCISection methods) makes it codec-aware
// with no codec changes. The round-trip Parse(Render(c)) == c is the A1 commit
// gate (see docs/NET.md §9, §11). The format emitted is standard UCI export:
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
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// uciObject is implemented by every model object that maps to a UCI section.
type uciObject interface {
	UCIPackage() string
	UCISection() string
}

// Render serializes a Config to one UCI text blob per package (e.g. "network",
// "firewall"), keyed by package name. Zero-valued optional fields are omitted,
// which is what makes the round-trip total: omitted ⇄ zero.
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
	t := v.Type()
	var name string
	var opts []string
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
		case hasFlag(flags, "list"):
			for _, s := range formatList(fv) {
				opts = append(opts, fmt.Sprintf("\tlist %s '%s'", optName, s))
			}
		default:
			if s, ok := formatScalar(fv); ok {
				opts = append(opts, fmt.Sprintf("\toption %s '%s'", optName, s))
			}
		}
	}
	var b strings.Builder
	if name != "" {
		fmt.Fprintf(&b, "config %s '%s'\n", section, name)
	} else {
		fmt.Fprintf(&b, "config %s\n", section)
	}
	for _, o := range opts {
		b.WriteString(o)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func formatScalar(fv reflect.Value) (string, bool) {
	switch x := fv.Interface().(type) {
	case string:
		return x, x != ""
	case bool:
		if !x {
			return "", false
		}
		return "1", true
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
	}
	return out
}

// binding maps a (package/section) key to the Config slice field and element
// type that receives parsed sections.
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
			elem := reflect.New(b.elemType).Elem()
			if err := fillObject(elem, sec); err != nil {
				return c, fmt.Errorf("package %q section %q %q: %w", pkg, sec.typ, sec.name, err)
			}
			fld := cv.Field(b.fieldIndex)
			fld.Set(reflect.Append(fld, elem))
		}
	}
	return c, nil
}

func fillObject(v reflect.Value, sec section) error {
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
	case string:
		fv.SetString(s)
	case bool:
		fv.SetBool(parseBool(s))
	case netip.Addr:
		a, err := netip.ParseAddr(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(a))
	case netip.Prefix:
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(p))
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
				i++ // consume closing quote
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
