package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// TestOpenRefusedIsToasted: an open request the router will not route —
// no app for the extension, or a sender without CapOpen — tells the user,
// as a warn toast from the app that asked. It used to be a log line only,
// so a click on a file did nothing.
func TestOpenRefusedIsToasted(t *testing.T) {
	opener := aboutManifest()
	opener.Capabilities = []string{CapOpen}
	closed := aboutManifest()
	closed.ID, closed.Name = "com.wash.closed", "Closed"

	cases := []struct {
		name string
		app  *Manifest
		path string
		body string
	}{
		{"no handler", opener, "/tmp/notes.xyz", "No app opens .xyz files."},
		{"no extension", opener, "/tmp/Makefile", "No app opens files without an extension."},
		{"lacks CapOpen", closed, "/tmp/notes.md", "Closed is not allowed to open files."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			reg.RegisterEntry(&Entry{Path: "/unused/wash-notify", Manifest: notifyManifest()})
			reg.RegisterEntry(&Entry{Path: "/unused/wash-app", Manifest: tc.app})
			r := NewRouter(Config{}, reg, nil)

			notifyPair := wiretest.NewPipePair()
			notifyDone := make(chan struct{})
			go func() {
				defer close(notifyDone)
				_ = r.HandleApp(context.Background(), notifyPair.EndA(), notifyManifest(), nil)
			}()
			_ = connectApp(t, notifyPair, NotifyAppID)

			appPair := wiretest.NewPipePair()
			appDone := make(chan struct{})
			go func() {
				defer close(appDone)
				_ = r.HandleApp(context.Background(), appPair.EndA(), tc.app, nil)
			}()
			inst := connectApp(t, appPair, tc.app.ID)
			_, _ = appPair.EndB().ReadFrame() // EvtWindowMapped

			writeEvtFrame(t, appPair.EndB(), wire.NewEvtOpenRequest(tc.path))

			env, from := readForwardedNotify(t, notifyPair, 2*time.Second)
			if from == nil || from.InstanceID != inst {
				t.Errorf("from=%+v, want the asking instance %q", from, inst)
			}
			if env["level"] != wire.NotifyLevelWarn || env["body"] != tc.body {
				t.Errorf("toast = %v, want warn %q", env, tc.body)
			}

			appPair.Close()
			notifyPair.Close()
			waitClose(t, appDone)
			waitClose(t, notifyDone)
		})
	}
}

// readForwardedNotify waits for the notify service's app_msg{kind:"notify"}
// and returns its payload and sender.
func readForwardedNotify(t *testing.T, pp *wiretest.PipePair, timeout time.Duration) (map[string]any, *wire.Sender) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := pp.EndB().ReadFrame()
		if err != nil {
			t.Fatalf("read forward: %v", err)
		}
		if f.Channel != ChannelEvent {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		msg, ok := m.(wire.EvtAppMsg)
		if !ok {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		if env["kind"] == "notify" {
			return env, msg.From
		}
	}
	t.Fatalf("no notify forward arrived within %v", timeout)
	return nil, nil
}
