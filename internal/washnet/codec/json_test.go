package codec

import (
	"encoding/json"
	"math/rand"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

func jsonRoundTrip(t *testing.T, c model.Config) {
	t.Helper()
	b, err := EncodeJSON(c)
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	got, err := DecodeJSON(b)
	if err != nil {
		t.Fatalf("DecodeJSON: %v\njson: %s", err, b)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round-trip mismatch\nwant: %#v\ngot:  %#v\njson: %s", c, got, b)
	}
}

func TestJSONRoundTripTable(t *testing.T) {
	cases := map[string]model.Config{
		"empty": {},
		"static-iface": {Interfaces: []model.Interface{{
			Name:   "lan",
			Device: "br-lan",
			Proto: model.StaticProto{
				IPAddr:  netip.MustParsePrefix("10.0.0.1/24"),
				Gateway: netip.MustParseAddr("10.0.0.254"),
				DNS:     []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")},
			},
		}}},
		"every-proto": {Interfaces: []model.Interface{
			{Name: "a", Proto: model.NoneProto{}},
			{Name: "b", Proto: model.DHCPProto{Hostname: "box"}},
			{Name: "c", Proto: model.DHCPv6Proto{}},
			{Name: "d", Proto: model.PPPoEProto{Username: "u", Password: "p"}},
			{Name: "e", Proto: model.WireGuardProto{PrivateKey: "K", ListenPort: 51820, Addresses: []netip.Prefix{netip.MustParsePrefix("10.9.0.1/24")}}},
		}},
		"wifi-encryption": {SSIDs: []model.WifiIface{
			{Name: "ap0", Device: "radio0", Mode: "ap", SSID: "open", Network: "lan", Encryption: model.EncNone{}},
			{Name: "ap1", Device: "radio0", Mode: "ap", SSID: "psk", Network: "lan", Encryption: model.EncPSK2{Key: "secret123"}},
			{Name: "ap2", Device: "radio0", Mode: "ap", SSID: "sae", Network: "lan", Encryption: model.EncSAE{Key: "secret123"}},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { jsonRoundTrip(t, c) })
	}
}

func TestJSONRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 500; i++ {
		jsonRoundTrip(t, genConfig(r))
	}
}

// TestJSONUnionWireShape pins the FE interchange convention: a union field is a
// JSON object carrying "_tag" + the active variant's fields, keyed by Go field
// name (apps/net/fe/src/objectform-model.ts).
func TestJSONUnionWireShape(t *testing.T) {
	c := model.Config{Interfaces: []model.Interface{{
		Name:  "lan",
		Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("10.0.0.1/24")},
	}}}
	b, err := EncodeJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		Interfaces []struct {
			Name  string `json:"Name"`
			Proto struct {
				Tag    string `json:"_tag"`
				IPAddr string `json:"IPAddr"`
			} `json:"Proto"`
		} `json:"Interfaces"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, b)
	}
	if len(top.Interfaces) != 1 {
		t.Fatalf("want 1 interface, got %d", len(top.Interfaces))
	}
	if got := top.Interfaces[0].Proto.Tag; got != "static" {
		t.Errorf("_tag = %q, want \"static\"", got)
	}
	if got := top.Interfaces[0].Proto.IPAddr; got != "10.0.0.1/24" {
		t.Errorf("IPAddr = %q, want \"10.0.0.1/24\"", got)
	}
}

// TestDecodeJSONFromFEShape decodes a value authored the way the FE emits it
// (object keyed by Go field name, union as {_tag,…}) to prove the contract from
// the consumer's side, not just via our own encoder.
func TestDecodeJSONFromFEShape(t *testing.T) {
	in := `{
	  "Interfaces": [
	    {"Name": "wan", "Device": "eth0", "Proto": {"_tag": "dhcp", "Hostname": "router"}}
	  ],
	  "Zones": [
	    {"Name": "lan", "Input": "ACCEPT", "Output": "ACCEPT", "Forward": "ACCEPT"}
	  ]
	}`
	c, err := DecodeJSON([]byte(in))
	if err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if len(c.Interfaces) != 1 || c.Interfaces[0].Name != "wan" || c.Interfaces[0].Device != "eth0" {
		t.Fatalf("interface decode wrong: %#v", c.Interfaces)
	}
	p, ok := c.Interfaces[0].Proto.(model.DHCPProto)
	if !ok {
		t.Fatalf("Proto = %T, want model.DHCPProto", c.Interfaces[0].Proto)
	}
	if p.Hostname != "router" {
		t.Errorf("Hostname = %q, want \"router\"", p.Hostname)
	}
	if len(c.Zones) != 1 || c.Zones[0].Name != "lan" {
		t.Fatalf("zone decode wrong: %#v", c.Zones)
	}
}

// TestDecodeJSONUnknownVariant rejects an unregistered discriminator rather than
// silently producing a nil union.
func TestDecodeJSONUnknownVariant(t *testing.T) {
	_, err := DecodeJSON([]byte(`{"Interfaces":[{"Name":"x","Proto":{"_tag":"bogus"}}]}`))
	if err == nil || !strings.Contains(err.Error(), "unknown variant") {
		t.Fatalf("want unknown-variant error, got %v", err)
	}
}
