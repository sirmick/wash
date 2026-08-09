package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// fakeAuth accepts exactly one (user, pass) and resolves it to a fixed
// Identity — a stand-in for internal/login's real backends, kept local so the
// shared guest core's tests carry no dependency on the HTTP login server.
type fakeAuth struct {
	user, pass string
	id         Identity
}

func (a fakeAuth) Authenticate(user, password string) (Identity, error) {
	if user == a.user && password == a.pass {
		return a.id, nil
	}
	return Identity{}, errors.New("invalid credentials")
}

// pipePair returns two StreamTransports over a synchronous net.Pipe: one for
// the front under test, one acting as the browser/FE.
func pipePair(t *testing.T) (front, fe *wire.StreamTransport, close func()) {
	t.Helper()
	a, b := net.Pipe()
	return wire.NewStreamTransport(a), wire.NewStreamTransport(b), func() { a.Close(); b.Close() }
}

func loginFrame(t *testing.T, user, pass string) wire.Frame {
	t.Helper()
	p, err := json.Marshal(loginReq{T: msgLogin, User: user, Pass: pass})
	if err != nil {
		t.Fatal(err)
	}
	return wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: p}
}

func readResp(t *testing.T, fe *wire.StreamTransport) loginResp {
	t.Helper()
	fr, err := fe.ReadFrame()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var r loginResp
	if err := json.Unmarshal(fr.Payload, &r); err != nil {
		t.Fatalf("decode response %q: %v", fr.Payload, err)
	}
	return r
}

func mustAuth(t *testing.T) Authenticator {
	t.Helper()
	return fakeAuth{user: "root", pass: "hunter2", id: Identity{UID: 4242, GID: 4243, Name: "root", Shell: "/bin/sh"}}
}

func TestFront_SuccessfulLoginForksRouterAsUser(t *testing.T) {
	front, fe, closeAll := pipePair(t)
	defer closeAll()

	spawned := make(chan Identity, 1)
	f := &Front{
		Auth: mustAuth(t),
		Spawn: func(ctx context.Context, id Identity) error {
			spawned <- id
			return nil // simulate a viewer session that immediately ends
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- f.Serve(context.Background(), front) }()

	// Proxy injects SessionOpen first; the front announces the login gate.
	openP, _ := json.Marshal(wire.NewSessionOpen())
	if err := fe.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: openP}); err != nil {
		t.Fatalf("write session.open: %v", err)
	}
	if got := readResp(t, fe); got.T != msgLoginRequired {
		t.Fatalf("on session.open got t = %q, want %q", got.T, msgLoginRequired)
	}
	if err := fe.WriteFrame(loginFrame(t, "root", "hunter2")); err != nil {
		t.Fatalf("write login: %v", err)
	}
	if got := readResp(t, fe); got.T != msgLoginOK {
		t.Fatalf("response t = %q, want %q", got.T, msgLoginOK)
	}

	select {
	case id := <-spawned:
		if id.UID != 4242 || id.Name != "root" {
			t.Fatalf("spawned identity = %+v, want uid 4242 name root", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SpawnRouter never called after login.ok")
	}

	// Closing only the FE end makes the front's next ReadFrame return EOF →
	// Serve returns nil (the launcher loop respawns on real device close).
	fe.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil on EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after channel close")
	}
}

func TestFront_BadCredentialsDoNotFork(t *testing.T) {
	front, fe, closeAll := pipePair(t)
	defer closeAll()

	spawned := make(chan Identity, 1)
	f := &Front{
		Auth: mustAuth(t),
		Spawn: func(ctx context.Context, id Identity) error {
			spawned <- id
			return nil
		},
	}
	go f.Serve(context.Background(), front)

	if err := fe.WriteFrame(loginFrame(t, "root", "wrong")); err != nil {
		t.Fatalf("write login: %v", err)
	}
	if got := readResp(t, fe); got.T != msgLoginErr {
		t.Fatalf("response t = %q, want %q", got.T, msgLoginErr)
	}
	// Bad creds must not have forked anything.
	select {
	case id := <-spawned:
		t.Fatalf("SpawnRouter called on failed auth: %+v", id)
	default:
	}

	// A retry with correct creds on the same channel must now succeed + fork.
	if err := fe.WriteFrame(loginFrame(t, "root", "hunter2")); err != nil {
		t.Fatalf("write retry login: %v", err)
	}
	if got := readResp(t, fe); got.T != msgLoginOK {
		t.Fatalf("retry response t = %q, want %q", got.T, msgLoginOK)
	}
	select {
	case <-spawned:
	case <-time.After(2 * time.Second):
		t.Fatal("SpawnRouter not called after successful retry")
	}
}

func TestFront_NonControlFramesIgnoredUntilLogin(t *testing.T) {
	front, fe, closeAll := pipePair(t)
	defer closeAll()

	f := &Front{
		Auth:  mustAuth(t),
		Spawn: func(ctx context.Context, id Identity) error { return nil },
	}
	go f.Serve(context.Background(), front)

	// Junk on a data channel before login — must be skipped, not auth'd.
	if err := fe.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 7, Payload: []byte("noise")}); err != nil {
		t.Fatalf("write noise: %v", err)
	}
	if err := fe.WriteFrame(loginFrame(t, "root", "hunter2")); err != nil {
		t.Fatalf("write login: %v", err)
	}
	if got := readResp(t, fe); got.T != msgLoginOK {
		t.Fatalf("response t = %q, want %q", got.T, msgLoginOK)
	}
}
