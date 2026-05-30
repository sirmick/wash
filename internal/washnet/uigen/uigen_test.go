package uigen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDescriptorSanity asserts structural invariants that must always hold,
// independent of the golden snapshot.
func TestDescriptorSanity(t *testing.T) {
	d := BuildDescriptor()
	if len(d.Objects) == 0 {
		t.Fatal("no objects")
	}
	seenKind := map[string]bool{}
	for _, o := range d.Objects {
		if seenKind[o.Kind] {
			t.Errorf("duplicate kind %q", o.Kind)
		}
		seenKind[o.Kind] = true
		for _, f := range o.Fields {
			if f.Widget == "" {
				t.Errorf("%s.%s has empty widget", o.GoType, f.Name)
			}
			if f.LabelKey == "" {
				t.Errorf("%s.%s has empty labelKey", o.GoType, f.Name)
			}
			if f.Widget == "union" && (f.Union == nil || len(f.Union.Variants) == 0) {
				t.Errorf("%s.%s union widget without variants", o.GoType, f.Name)
			}
		}
	}
	// Spot-check the proto union and a derived/override widget.
	iface := findObj(d, "network/interface")
	if iface == nil {
		t.Fatal("network/interface missing")
	}
	proto := findField(iface.Fields, "Proto")
	if proto == nil || proto.Union == nil {
		t.Fatal("Interface.Proto union missing")
	}
	if proto.Union.Discriminator != "proto" {
		t.Errorf("proto discriminator = %q", proto.Union.Discriminator)
	}
	tags := map[string]bool{}
	for _, v := range proto.Union.Variants {
		tags[v.Tag] = true
	}
	for _, want := range []string{"static", "dhcp", "wireguard", "pppoe", "none", "dhcpv6"} {
		if !tags[want] {
			t.Errorf("proto union missing variant %q", want)
		}
	}
	host := findObj(d, "dhcp/host")
	if mac := findField(host.Fields, "MAC"); mac == nil || mac.Widget != "mac" {
		t.Errorf("Host.MAC widget = %v, want mac", mac)
	}
	zone := findObj(d, "firewall/zone")
	if nets := findField(zone.Fields, "Networks"); nets == nil || nets.Ref != "interface" || !nets.List {
		t.Errorf("Zone.Networks should be a list ref to interface, got %+v", nets)
	}
}

// TestGolden snapshots the descriptor, TS types, and i18n scaffold. Regenerate
// with UPDATE_GOLDEN=1.
func TestGolden(t *testing.T) {
	artifacts := map[string]string{
		"descriptor.json": DescriptorJSON(),
		"types.ts":        EmitTS(),
		"i18n.json":       I18nJSON(),
	}
	update := os.Getenv("UPDATE_GOLDEN") != ""
	if update {
		_ = os.MkdirAll("testdata", 0o755)
	}
	for name, got := range artifacts {
		path := filepath.Join("testdata", name)
		if update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s (run UPDATE_GOLDEN=1): %v", path, err)
		}
		if string(want) != got {
			t.Errorf("golden %s mismatch:\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
		}
	}
}

func findObj(d Descriptor, kind string) *ObjectDescriptor {
	for i := range d.Objects {
		if d.Objects[i].Kind == kind {
			return &d.Objects[i]
		}
	}
	return nil
}

func findField(fs []FieldDescriptor, name string) *FieldDescriptor {
	for i := range fs {
		if fs[i].Name == name {
			return &fs[i]
		}
	}
	return nil
}
