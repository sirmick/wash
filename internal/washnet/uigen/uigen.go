// Package uigen turns the model's Go structs + `ui`/`uci` tags into the
// artifacts the FE consumes (docs/NET.md §6): a UI descriptor (drives the
// generic <ObjectForm>), TypeScript types (form binding), and an i18n label
// scaffold. Go structs are the single source of truth; everything here is
// generated downstream. Widgets derive from the field type; `ui` tags only
// augment (group, ref, widget override, advanced). Unions emit a discriminator
// + one variant sub-form each, which is how the FE gets conditional visibility
// for free.
package uigen

import (
	"net/netip"
	"reflect"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// Descriptor is the root UI-generation artifact.
type Descriptor struct {
	Objects []ObjectDescriptor `json:"objects"`
}

type ObjectDescriptor struct {
	Kind    string            `json:"kind"` // package/section
	Package string            `json:"package"`
	Section string            `json:"section"`
	GoType  string            `json:"goType"`
	Fields  []FieldDescriptor `json:"fields"`
}

type FieldDescriptor struct {
	Name     string           `json:"name"`   // Go field name
	Option   string           `json:"option"` // UCI option ("" for the section name)
	Widget   string           `json:"widget"` // toggle|text|number|ip|cidr|password|mac|ref|union|section-name|list-*
	Group    string           `json:"group,omitempty"`
	Ref      string           `json:"ref,omitempty"` // referenced object kind, for pickers
	List     bool             `json:"list,omitempty"`
	Advanced bool             `json:"advanced,omitempty"`
	LabelKey string           `json:"labelKey"` // i18n key
	Union    *UnionDescriptor `json:"union,omitempty"`
}

type UnionDescriptor struct {
	Discriminator string              `json:"discriminator"` // the UCI option holding the tag
	Variants      []VariantDescriptor `json:"variants"`
}

type VariantDescriptor struct {
	Tag    string            `json:"tag"`
	GoType string            `json:"goType"`
	Fields []FieldDescriptor `json:"fields"`
}

var (
	addrType   = reflect.TypeOf(netip.Addr{})
	prefixType = reflect.TypeOf(netip.Prefix{})
)

// BuildDescriptor reflects every model object into a UI descriptor.
func BuildDescriptor() Descriptor {
	var objs []ObjectDescriptor
	for _, t := range model.ObjectTypes() {
		inst := reflect.New(t).Elem().Interface().(model.Kinded)
		objs = append(objs, ObjectDescriptor{
			Kind:    inst.UCIPackage() + "/" + inst.UCISection(),
			Package: inst.UCIPackage(),
			Section: inst.UCISection(),
			GoType:  t.Name(),
			Fields:  fieldsOf(t),
		})
	}
	return Descriptor{Objects: objs}
}

func fieldsOf(t reflect.Type) []FieldDescriptor {
	var fs []FieldDescriptor
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		uciTag := f.Tag.Get("uci")
		if uciTag == "" || uciTag == "-" {
			continue
		}
		optName, flags := splitTag(uciTag)
		ui := parseUI(f.Tag.Get("ui"))
		isName := has(flags, "name")
		isList := has(flags, "list")
		isUnion := has(flags, "union")
		_, advanced := ui["advanced"]
		fd := FieldDescriptor{
			Name:     f.Name,
			Option:   optName,
			Widget:   widgetFor(f.Type, ui, isList, isName, isUnion),
			Group:    ui["group"],
			Ref:      ui["ref"],
			List:     isList,
			Advanced: advanced,
			LabelKey: t.Name() + "." + f.Name,
		}
		if isUnion {
			ud := &UnionDescriptor{Discriminator: optName}
			for _, vt := range model.VariantsOf(f.Type) {
				ud.Variants = append(ud.Variants, VariantDescriptor{
					Tag:    model.TagOf(vt),
					GoType: vt.Name(),
					Fields: fieldsOf(vt),
				})
			}
			fd.Union = ud
		}
		fs = append(fs, fd)
	}
	return fs
}

func widgetFor(ft reflect.Type, ui map[string]string, isList, isName, isUnion bool) string {
	if w := ui["widget"]; w != "" {
		return w
	}
	if isName {
		return "section-name"
	}
	if isUnion {
		return "union"
	}
	if ui["ref"] != "" {
		return "ref"
	}
	if isList {
		return "list-" + baseWidget(ft.Elem())
	}
	return baseWidget(ft)
}

func baseWidget(t reflect.Type) string {
	switch t {
	case addrType:
		return "ip"
	case prefixType:
		return "cidr"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "toggle"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "number"
	}
	return "text"
}

// --- tag parsing -----------------------------------------------------------

func splitTag(tag string) (name string, flags []string) {
	parts := strings.Split(tag, ",")
	return parts[0], parts[1:]
}

func parseUI(tag string) map[string]string {
	m := map[string]string{}
	if tag == "" {
		return m
	}
	for _, tok := range strings.Split(tag, ",") {
		if k, v, ok := strings.Cut(tok, "="); ok {
			m[k] = v
		} else {
			m[tok] = ""
		}
	}
	return m
}

func has(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}
