package recipe

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/validate"
)

// base is a coherent gateway with the prerequisites recipes reference (a radio,
// a wan zone, a wireguard interface).
func base() model.Config {
	return model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("192.168.1.1/24")}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
			{Name: "vpn", Proto: model.WireGuardProto{PrivateKey: "K"}},
		},
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
		},
		Radios: []model.WifiDevice{{Name: "radio0", Band: "5g", Channel: "36"}},
	}
}

func errs(ds []validate.Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == validate.Error {
			n++
		}
	}
	return n
}

// TestRecipeSoundness: every recipe's output validates clean under Full caps —
// the recipe-soundness property (docs/NET.md §9.2).
func TestRecipeSoundness(t *testing.T) {
	b := base()
	recipes := map[string]func() (change.ChangeSet, error){
		"guest": func() (change.ChangeSet, error) {
			return AddGuestNetwork(b, GuestNetwork{SSID: "guest", Key: "supersecret", Radio: "radio0", Gateway: netip.MustParsePrefix("192.168.20.1/24")})
		},
		"expose": func() (change.ChangeSet, error) {
			return ExposeService(b, PortForward{Name: "ssh", ExternalPort: "2222", InternalIP: netip.MustParseAddr("192.168.1.10"), InternalPort: "22"})
		},
		"reserve": func() (change.ChangeSet, error) {
			return ReserveLease(b, Reservation{Name: "nas", MAC: "aa:bb:cc:dd:ee:ff", IP: netip.MustParseAddr("192.168.1.50"), Hostname: "nas"})
		},
		"wgpeer": func() (change.ChangeSet, error) {
			return AddWireGuardPeer(b, WireGuardPeer{Interface: "vpn", PublicKey: "PUB", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.2/32")}, PersistentKeepalive: 25})
		},
	}
	for name, mk := range recipes {
		t.Run(name, func(t *testing.T) {
			cs, err := mk()
			if err != nil {
				t.Fatalf("recipe error: %v", err)
			}
			out, err := cs.Apply(b)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if n := errs(validate.Validate(out, caps.Full())); n != 0 {
				t.Fatalf("recipe output has %d validation error(s): %+v", n, validate.Validate(out, caps.Full()))
			}
		})
	}
}

func TestGuestNetworkCreatesAllObjects(t *testing.T) {
	b := base()
	cs, err := AddGuestNetwork(b, GuestNetwork{SSID: "guest", Key: "supersecret", Radio: "radio0", Gateway: netip.MustParsePrefix("192.168.20.1/24")})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := cs.Apply(b)
	if len(out.Interfaces) != len(b.Interfaces)+1 {
		t.Error("guest interface not added")
	}
	if len(out.Zones) != len(b.Zones)+1 {
		t.Error("guest zone not added")
	}
	if len(out.Forwardings) != 1 || out.Forwardings[0].Dest != "wan" {
		t.Error("guest→wan forwarding not added")
	}
	if len(out.Pools) != 1 || len(out.SSIDs) != 1 {
		t.Error("guest pool/SSID not added")
	}
}

// TestChangeSetDoesNotMutateInput: recipes are all-or-nothing and pure.
func TestChangeSetDoesNotMutateInput(t *testing.T) {
	b := base()
	snapshot := base() // identical fresh copy
	cs, _ := AddGuestNetwork(b, GuestNetwork{SSID: "g", Key: "supersecret", Radio: "radio0", Gateway: netip.MustParsePrefix("10.0.20.1/24")})
	_, _ = cs.Apply(b)
	if !reflect.DeepEqual(b, snapshot) {
		t.Fatal("recipe Apply mutated the input Config")
	}
}

func TestRecipeValidationRejectsBadParams(t *testing.T) {
	if _, err := AddGuestNetwork(base(), GuestNetwork{SSID: "g"}); err == nil {
		t.Error("guest without radio/key/gateway should error")
	}
	if _, err := ExposeService(base(), PortForward{Name: "x"}); err == nil {
		t.Error("expose without ports/ip should error")
	}
	if _, err := AddWireGuardPeer(base(), WireGuardPeer{Interface: "vpn"}); err == nil {
		t.Error("peer without pubkey/allowedips should error")
	}
}
