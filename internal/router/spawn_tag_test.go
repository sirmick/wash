package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// A normal spawn's reply echoes the request's Tag, so a caller with two
// spawns of the same app in flight can tell its own from the other
// (internal/places adopts only the window its click opened). The refusal
// paths are what a unit test can reach without forking a child; spawnChild
// sets the same field on its Ok/Err replies.
func TestSpawnReplyEchoesTag(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	for _, tc := range []struct {
		name string
		caps []string
		code string
	}{
		{"no spawn capability", nil, wire.ErrCodeForbidden},
		{"unknown app", []string{CapSpawn}, wire.ErrCodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := singleWinManifest()
			m.Capabilities = tc.caps
			pair := wiretest.NewPipePair()
			app := pair.EndB()
			done := make(chan struct{})
			go func() { defer close(done); _ = r.HandleApp(context.Background(), pair.EndA(), m, nil) }()
			writeCtrl(t, app, wire.NewIdentity(m.ID, ProtocolVersion, "0.9.0"))
			if _, ok := readCtrl(t, app).(wire.IdentityAck); !ok {
				t.Fatal("expected IdentityAck")
			}
			writeEvt(t, app, wire.NewEvtSpawnRequestTagged("com.wash.nosuch", "", 42))
			for i := 0; ; i++ {
				if i > 20 {
					t.Fatal("no spawn reply within 20 events")
				}
				if e, ok := readEvt(t, app).(wire.EvtSpawnErr); ok {
					if e.Tag != 42 || e.Code != tc.code || e.ReqID != 0 {
						t.Fatalf("reply = %+v, want tag 42, code %s, no ReqID", e, tc.code)
					}
					break
				}
			}
			pair.Close()
			waitClose(t, done)
		})
	}
}
