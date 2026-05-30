// JSON codec for model.Config — the FE↔BE interchange (docs/NET.md §6, §2.11).
//
// The wash-net FE (apps/net/fe) holds each object as a plain JSON map keyed by
// Go field name, with tagged-union fields shaped as {"_tag": "<variant>", …the
// variant's own fields}. That is exactly what standard encoding/json produces
// for a model object EXCEPT for the union fields: an interface field marshals to
// its concrete variant's fields with no discriminator, and won't unmarshal back
// into the interface at all. So this codec marshals/parses normally and only
// special-cases the union fields, splicing in (encode) or reading (decode) the
// "_tag" discriminator via the model's union registry.
//
// Everything else round-trips for free: netip.Addr/Prefix implement
// Text(Un)Marshaler (zero ⇄ ""), MAC/enums are plain strings, lists are JSON
// arrays. Top-level empty slices are omitted so nil ⇄ absent stays total — the
// JSON sibling of codec.Render's omitted ⇄ zero. Round-trip Decode(Encode(c))==c
// is a unit gate, mirroring the UCI round-trip property.
package codec

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/sirmick/wash/internal/washnet/model"
)

// unionTagKey is the discriminator key in a union field's JSON object. It
// matches the FE convention in apps/net/fe/src/objectform-model.ts.
const unionTagKey = "_tag"

// EncodeJSON serializes a Config to the FE interchange JSON: an object keyed by
// Config field name (Interfaces, Zones, …), each holding an array of object
// JSONs keyed by Go field name. Empty Config slices are omitted.
func EncodeJSON(c model.Config) ([]byte, error) {
	cv := reflect.ValueOf(c)
	out := map[string]json.RawMessage{}
	for i := 0; i < cv.NumField(); i++ {
		f := cv.Field(i)
		if f.Kind() != reflect.Slice || f.Len() == 0 {
			continue
		}
		elems := make([]json.RawMessage, f.Len())
		for j := 0; j < f.Len(); j++ {
			raw, err := encodeObject(f.Index(j))
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", cv.Type().Field(i).Name, j, err)
			}
			elems[j] = raw
		}
		arr, err := json.Marshal(elems)
		if err != nil {
			return nil, err
		}
		out[cv.Type().Field(i).Name] = arr
	}
	return json.Marshal(out)
}

// encodeObject marshals one model object, then rewrites each union field's value
// to carry the "_tag" discriminator alongside the active variant's fields.
func encodeObject(v reflect.Value) (json.RawMessage, error) {
	raw, err := json.Marshal(v.Interface())
	if err != nil {
		return nil, err
	}
	uf := unionFields(v.Type())
	if len(uf) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, u := range uf {
		fv := v.Field(u.index)
		if fv.IsNil() {
			m[u.name] = json.RawMessage("null")
			continue
		}
		tagger, ok := fv.Interface().(interface{ UCITag() string })
		if !ok {
			return nil, fmt.Errorf("union field %s does not implement UCITag", u.name)
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(m[u.name], &inner); err != nil {
			return nil, fmt.Errorf("union field %s: %w", u.name, err)
		}
		if inner == nil {
			inner = map[string]json.RawMessage{}
		}
		tag, _ := json.Marshal(tagger.UCITag())
		inner[unionTagKey] = tag
		spliced, err := json.Marshal(inner)
		if err != nil {
			return nil, err
		}
		m[u.name] = spliced
	}
	return json.Marshal(m)
}

// DecodeJSON parses the FE interchange JSON back into a Config.
func DecodeJSON(data []byte) (model.Config, error) {
	var c model.Config
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return c, err
	}
	cv := reflect.ValueOf(&c).Elem()
	ct := cv.Type()
	for i := 0; i < ct.NumField(); i++ {
		raw, ok := top[ct.Field(i).Name]
		if !ok {
			continue
		}
		f := cv.Field(i)
		if f.Kind() != reflect.Slice {
			continue
		}
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return c, fmt.Errorf("%s: %w", ct.Field(i).Name, err)
		}
		elemType := f.Type().Elem()
		slice := reflect.MakeSlice(f.Type(), 0, len(elems))
		for j, er := range elems {
			ev, err := decodeObject(er, elemType)
			if err != nil {
				return c, fmt.Errorf("%s[%d]: %w", ct.Field(i).Name, j, err)
			}
			slice = reflect.Append(slice, ev)
		}
		f.Set(slice)
	}
	return c, nil
}

// decodeObject parses one object: union fields are resolved by their "_tag" and
// set on the interface; the rest unmarshal through encoding/json as usual.
func decodeObject(raw json.RawMessage, t reflect.Type) (reflect.Value, error) {
	out := reflect.New(t)
	uf := unionFields(t)
	if len(uf) == 0 {
		if err := json.Unmarshal(raw, out.Interface()); err != nil {
			return reflect.Value{}, err
		}
		return out.Elem(), nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return reflect.Value{}, err
	}
	// Resolve unions, then strip their keys so the residual map fills only the
	// plain fields (the interface fields stay nil through json).
	resolved := make([]reflect.Value, len(uf))
	for i, u := range uf {
		ur, ok := m[u.name]
		delete(m, u.name)
		if !ok || string(ur) == "null" {
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(ur, &inner); err != nil {
			return reflect.Value{}, fmt.Errorf("union field %s: %w", u.name, err)
		}
		var tag string
		if t, ok := inner[unionTagKey]; ok {
			if err := json.Unmarshal(t, &tag); err != nil {
				return reflect.Value{}, fmt.Errorf("union field %s _tag: %w", u.name, err)
			}
		}
		vt, ok := model.VariantType(u.iface, tag)
		if !ok {
			return reflect.Value{}, fmt.Errorf("union field %s: unknown variant %q", u.name, tag)
		}
		variant := reflect.New(vt)
		if err := json.Unmarshal(ur, variant.Interface()); err != nil { // _tag ignored by the struct
			return reflect.Value{}, fmt.Errorf("union field %s variant %q: %w", u.name, tag, err)
		}
		resolved[i] = variant.Elem()
	}
	residual, err := json.Marshal(m)
	if err != nil {
		return reflect.Value{}, err
	}
	if err := json.Unmarshal(residual, out.Interface()); err != nil {
		return reflect.Value{}, err
	}
	for i, u := range uf {
		if resolved[i].IsValid() {
			out.Elem().Field(u.index).Set(resolved[i])
		}
	}
	return out.Elem(), nil
}

type unionField struct {
	index int
	name  string       // Go field name (the JSON key)
	iface reflect.Type // the union interface type, for VariantType resolution
}

// unionFields returns the tagged-union fields (uci:"opt,union") of a struct.
func unionFields(t reflect.Type) []unionField {
	var out []unionField
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("uci")
		if tag == "" || tag == "-" {
			continue
		}
		if _, flags := parseTag(tag); hasFlag(flags, "union") {
			out = append(out, unionField{index: i, name: t.Field(i).Name, iface: t.Field(i).Type})
		}
	}
	return out
}
