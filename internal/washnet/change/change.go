// Package change models a staged mutation of a Config (ChangeSet) and the
// object-level diff between two Configs. Recipes produce ChangeSets; the txn
// engine stages them and shows the Diff before apply (docs/NET.md §4.3, §4.4).
package change

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/sirmick/wash/internal/washnet/model"
)

// ChangeSet is a pure transform of a Config plus a human-readable summary of
// what it does. It must be all-or-nothing: Apply either returns the fully
// mutated Config or an error (no partial mutation).
type ChangeSet struct {
	Summary []string
	Apply   func(model.Config) (model.Config, error)
}

// Op is the kind of change to one object.
type Op string

const (
	OpAdd    Op = "add"
	OpRemove Op = "remove"
	OpUpdate Op = "update"
)

// Entry is one object-level change.
type Entry struct {
	Kind string // package/section
	Name string // object identity (section name, name option, or value digest)
	Op   Op
}

// Diff is the ordered set of object-level changes between two Configs.
type Diff struct {
	Entries []Entry
}

// Empty reports whether the diff changes nothing.
func (d Diff) Empty() bool { return len(d.Entries) == 0 }

// Compute diffs before→after at object granularity, matching objects by
// identity (section name / name option, else value digest for anonymous
// sections). Output is sorted (kind, then name) for determinism.
func Compute(before, after model.Config) Diff {
	bv := reflect.ValueOf(before)
	av := reflect.ValueOf(after)
	var entries []Entry
	for i := 0; i < bv.NumField(); i++ {
		bf, af := bv.Field(i), av.Field(i)
		if bf.Kind() != reflect.Slice {
			continue
		}
		kind := kindOf(bf, af)
		if kind == "" {
			continue
		}
		bm := identityMap(bf)
		am := identityMap(af)
		seen := map[string]bool{}
		var ids []string
		for id := range bm {
			ids = append(ids, id)
		}
		for id := range am {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			b, okB := bm[id]
			a, okA := am[id]
			switch {
			case okA && !okB:
				entries = append(entries, Entry{kind, id, OpAdd})
			case okB && !okA:
				entries = append(entries, Entry{kind, id, OpRemove})
			case !reflect.DeepEqual(b, a):
				entries = append(entries, Entry{kind, id, OpUpdate})
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
	return Diff{Entries: entries}
}

func kindOf(fields ...reflect.Value) string {
	for _, f := range fields {
		if f.Len() > 0 {
			if o, ok := f.Index(0).Interface().(model.Kinded); ok {
				return o.UCIPackage() + "/" + o.UCISection()
			}
		}
	}
	// Empty on both sides: resolve via the element type.
	et := fields[0].Type().Elem()
	if o, ok := reflect.New(et).Elem().Interface().(model.Kinded); ok {
		return o.UCIPackage() + "/" + o.UCISection()
	}
	return ""
}

func identityMap(slice reflect.Value) map[string]reflect.Value {
	m := map[string]reflect.Value{}
	for i := 0; i < slice.Len(); i++ {
		v := slice.Index(i)
		id := objectIdentity(v)
		if id == "" {
			id = fmt.Sprintf("#%v", v.Interface())
		}
		m[id] = v
	}
	return m
}

// objectIdentity returns the section name (uci ",name" field) or the value of a
// "name" option, else "" for anonymous sections.
func objectIdentity(v reflect.Value) string {
	t := v.Type()
	var nameOpt string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("uci")
		if tag == "" || tag == "-" {
			continue
		}
		opt, flags := splitTag(tag)
		if hasFlag(flags, "name") {
			if s := v.Field(i).String(); s != "" {
				return s
			}
		}
		if opt == "name" {
			nameOpt = v.Field(i).String()
		}
	}
	return nameOpt
}

func splitTag(tag string) (string, []string) {
	var name string
	flags := []string{}
	first := true
	start := 0
	for i := 0; i <= len(tag); i++ {
		if i == len(tag) || tag[i] == ',' {
			tok := tag[start:i]
			if first {
				name = tok
				first = false
			} else {
				flags = append(flags, tok)
			}
			start = i + 1
		}
	}
	return name, flags
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
