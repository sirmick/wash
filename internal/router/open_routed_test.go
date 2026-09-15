package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

func TestOpenPathFromArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{nil, ""},
		{[]string{"wash-edit"}, ""},
		{[]string{"wash-edit", "--open"}, ""},
		{[]string{"wash-edit", "--open", "/tmp/a.md"}, "/tmp/a.md"},
		{[]string{"wash", "wash-edit", "--login", "--open", "/x y/z.txt", "--other"}, "/x y/z.txt"},
	}
	for _, c := range cases {
		if got := openPathFromArgv(c.argv); got != c.want {
			t.Errorf("openPathFromArgv(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// TestNoteOpenRouted_NotifiesSessionApp — the router tells the session app
// about every routed open as a From-less app_msg {kind:"open.routed"}, and
// logs the line the e2e asserts on.
func TestNoteOpenRouted_NotifiesSessionApp(t *testing.T) {
	const sessionID = "com.wash.session"
	var logs []string
	r := NewRouter(Config{SessionAppID: sessionID}, NewRegistry(), func(f string, a ...any) {
		logs = append(logs, fmt.Sprintf(f, a...))
	})
	pp := wiretest.NewPipePair()
	defer pp.Close()
	sess := &AppInstance{
		Transport:  pp.EndA(),
		AppID:      sessionID,
		InstanceID: "i-session",
		Manifest:   &Manifest{ID: sessionID, Surface: SurfaceDesktop},
		router:     r,
	}
	// A bystander window app must not receive the notice.
	other := &AppInstance{
		Transport:  wiretest.NewPipePair().EndA(),
		AppID:      "com.wash.edit",
		InstanceID: "i-edit",
		WindowID:   7,
		Manifest:   &Manifest{ID: "com.wash.edit", Surface: SurfaceWindow},
		router:     r,
	}
	r.registerApp(other)
	r.registerApp(sess)

	r.noteOpenRouted("/home/u/notes.md", "com.wash.edit", "open.request")

	fe := pp.EndB()
	done := make(chan wire.Frame, 1)
	go func() {
		f, err := fe.ReadFrame()
		if err == nil {
			done <- f
		}
	}()
	var f wire.Frame
	select {
	case f = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session app never received the open.routed app_msg")
	}
	if f.Channel != ChannelEvent {
		t.Fatalf("frame on channel %d, want event channel %d", f.Channel, ChannelEvent)
	}
	msg, err := wire.DecodeEvt(f.Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	am, ok := msg.(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("got %T, want EvtAppMsg", msg)
	}
	if am.From != nil {
		t.Errorf("router-originated notice must carry no From, got %+v", am.From)
	}
	var body struct {
		Kind  string `json:"kind"`
		Path  string `json:"path"`
		AppID string `json:"app_id"`
	}
	if err := json.Unmarshal(am.Data, &body); err != nil {
		t.Fatalf("data: %v", err)
	}
	if body.Kind != openRoutedKind || body.Path != "/home/u/notes.md" || body.AppID != "com.wash.edit" {
		t.Errorf("body = %+v", body)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, `open.routed: path="/home/u/notes.md" app=com.wash.edit via=open.request`) {
			found = true
		}
	}
	if !found {
		t.Errorf("missing open.routed log line; logs=%q", logs)
	}
}

// TestNoteOpenRouted_NoSessionIsSilent — kiosk / --no-session routers have
// nobody to tell; the notice is dropped without error.
func TestNoteOpenRouted_NoSessionIsSilent(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	if r.sessionInstance() != nil {
		t.Fatal("empty router reported a session instance")
	}
	r.noteOpenRouted("/tmp/x.txt", "com.wash.edit", "attach") // must not panic
	r.noteOpenRouted("", "com.wash.edit", "attach")
}

// TestResolveRecipient_DesktopByAppID — the session app is InstancingSingle,
// not a singleton service, yet apps address it by app id (recent.note from
// fm and Radio). It resolves to the live desktop instance; any other
// non-singleton stays forbidden, and a desktop that is not running is not
// spawned to receive a note.
func TestResolveRecipient_DesktopByAppID(t *testing.T) {
	const sessionID = "com.wash.session"
	reg := NewRegistry()
	reg.RegisterEntry(&Entry{Path: "/unused/wash-session", Manifest: &Manifest{ID: sessionID, Surface: SurfaceDesktop, Instancing: InstancingSingle}})
	reg.RegisterEntry(&Entry{Path: "/unused/wash-edit", Manifest: &Manifest{ID: "com.wash.edit", Surface: SurfaceWindow, Instancing: InstancingMulti}})
	r := NewRouter(Config{SessionAppID: sessionID}, reg, func(string, ...any) {})

	if _, code, err := r.resolveRecipient(context.Background(), wire.Recipient{AppID: sessionID}); err == nil || code != wire.ErrCodeForbidden {
		t.Fatalf("no desktop running: code=%q err=%v, want forbidden (never spawned for a note)", code, err)
	}

	sess := &AppInstance{
		Transport:  wiretest.NewPipePair().EndA(),
		AppID:      sessionID,
		InstanceID: "i-session",
		Manifest:   &Manifest{ID: sessionID, Surface: SurfaceDesktop, Instancing: InstancingSingle},
		router:     r,
	}
	r.registerApp(sess)
	got, _, err := r.resolveRecipient(context.Background(), wire.Recipient{AppID: sessionID})
	if err != nil || got != sess {
		t.Fatalf("desktop by app id = %v, %v; want the session instance", got, err)
	}
	if _, code, err := r.resolveRecipient(context.Background(), wire.Recipient{AppID: "com.wash.edit"}); err == nil || code != wire.ErrCodeForbidden {
		t.Errorf("multi-instance app by app id: code=%q err=%v, want forbidden", code, err)
	}
}
