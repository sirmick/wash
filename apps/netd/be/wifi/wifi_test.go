package wifi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeRunner returns canned output for the first reply key whose substring
// appears in the joined command, and records every call for argv assertions.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	reply map[string]struct {
		out string
		err error
	}
}

func (f *fakeRunner) do(name string, args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	for sub, r := range f.reply {
		if strings.Contains(cmd, sub) {
			return r.out, r.err
		}
	}
	return "", nil
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	return f.do(name, args)
}
func (f *fakeRunner) runStdout(_ context.Context, name string, args ...string) (string, error) {
	return f.do(name, args)
}
func (f *fakeRunner) saw(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

var _ runner = (*fakeRunner)(nil)

func reply(m map[string]string) *fakeRunner {
	f := &fakeRunner{reply: map[string]struct {
		out string
		err error
	}{}}
	for k, v := range m {
		f.reply[k] = struct {
			out string
			err error
		}{out: v}
	}
	return f
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name              string
		radios            []string // injected sysfs probe result
		out               string   // nmcli general status stdout
		err               error    // nmcli error (absent / NM down)
		wantRadio, wantNM bool
	}{
		{"nm live with radio", []string{"wlan0"}, "connected", nil, true, true},
		{"radio but no NM (declarative box)", []string{"wlan0"}, "", errors.New("not running"), true, false},
		{"NM live, no radio", nil, "connected", nil, false, true},
		{"neither", nil, "", errors.New("not running"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRunner{reply: map[string]struct {
				out string
				err error
			}{"general status": {out: c.out, err: c.err}}}
			l := &Live{run: f, radio: func() []string { return c.radios }}
			radio, nm := l.Detect()
			if radio != c.wantRadio || nm != c.wantNM {
				t.Errorf("Detect() = (radio %v, nm %v), want (%v, %v)", radio, nm, c.wantRadio, c.wantNM)
			}
		})
	}
}

func TestScanSwallowsRescanRateLimit(t *testing.T) {
	f := reply(map[string]string{
		"wifi list": "*:home:80:WPA2\n:cafe:55:--\n",
	})
	// rescan errors with the rate-limit message; Scan must ignore it and still list.
	f.reply["rescan"] = struct {
		out string
		err error
	}{err: errors.New("Error: Scanning not allowed immediately following previous scan")}

	aps, err := (&Live{run: f}).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan should swallow the rescan rate-limit error, got %v", err)
	}
	if !f.saw("device wifi rescan") {
		t.Error("Scan should attempt a rescan")
	}
	if len(aps) != 2 || aps[0].SSID != "home" || !aps[0].InUse || aps[1].Security != "" {
		t.Fatalf("unexpected scan result: %+v", aps)
	}
}

func TestStatusFiltersWifi(t *testing.T) {
	f := reply(map[string]string{
		"connection show --active": "Wired:802-3-ethernet:eth0\nhome:802-11-wireless:wlan0\n",
	})
	conns, err := (&Live{run: f}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 || conns[0].Device != "wlan0" {
		t.Fatalf("want one wlan0 wifi conn, got %+v", conns)
	}
}

func TestConnectArgv(t *testing.T) {
	cases := []struct {
		name          string
		security, psk string
		hidden        bool
		wantKeyMgmt   string // "" ⇒ no wifi-sec (open)
		wantHidden    bool
	}{
		{"wpa2-psk", SecPSK2, "hunter2!!", false, "wpa-psk", false},
		{"wpa3-sae", SecSAE, "correcthorse", false, "sae", false},
		{"open", SecNone, "", false, "", false},
		{"hidden psk2", SecPSK2, "hunter2!!", true, "wpa-psk", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := reply(nil)
			if _, err := (&Live{run: f}).Connect(context.Background(), "MyNet", c.security, c.psk, c.hidden); err != nil {
				t.Fatal(err)
			}
			// explicit profile add + up, and a stale-profile delete for idempotency
			if !f.saw("connection add type wifi con-name MyNet ssid MyNet") {
				t.Fatalf("missing profile add, calls=%v", f.calls)
			}
			if !f.saw("connection up MyNet") {
				t.Errorf("missing connection up, calls=%v", f.calls)
			}
			if c.wantKeyMgmt == "" {
				if f.saw("wifi-sec.key-mgmt") {
					t.Errorf("open should set no key-mgmt, calls=%v", f.calls)
				}
			} else {
				if !f.saw("wifi-sec.key-mgmt " + c.wantKeyMgmt) {
					t.Errorf("want key-mgmt %s, calls=%v", c.wantKeyMgmt, f.calls)
				}
				if !f.saw("wifi-sec.psk " + c.psk) {
					t.Errorf("missing psk in argv, calls=%v", f.calls)
				}
			}
			if got := f.saw("802-11-wireless.hidden yes"); got != c.wantHidden {
				t.Errorf("hidden-in-argv = %v, want %v", got, c.wantHidden)
			}
		})
	}
}

func TestForgetArgv(t *testing.T) {
	f := reply(nil)
	if _, err := (&Live{run: f}).Forget(context.Background(), "OldNet"); err != nil {
		t.Fatal(err)
	}
	if !f.saw("connection delete id OldNet") {
		t.Fatalf("missing delete call, calls=%v", f.calls)
	}
}

func TestSetEnabledArgv(t *testing.T) {
	for _, c := range []struct {
		on   bool
		want string
	}{{true, "radio wifi on"}, {false, "radio wifi off"}} {
		f := reply(nil)
		if _, err := (&Live{run: f}).SetEnabled(context.Background(), c.on); err != nil {
			t.Fatal(err)
		}
		if !f.saw(c.want) {
			t.Errorf("SetEnabled(%v) missing %q, calls=%v", c.on, c.want, f.calls)
		}
	}
}

func TestEnabled(t *testing.T) {
	on := &Live{run: reply(map[string]string{"general status": "enabled"})}
	off := &Live{run: reply(map[string]string{"general status": "disabled"})}
	if !on.Enabled(context.Background()) || off.Enabled(context.Background()) {
		t.Error("Enabled should track the WIFI column")
	}
}
