package router

import (
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

func TestTearDownBroadcastsInstanceGone(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), nil)
	deadPair := wiretest.NewPipePair()
	livePair := wiretest.NewPipePair()
	dead := &AppInstance{
		Transport:  deadPair.EndA(),
		AppID:      "com.wash.ai",
		InstanceID: "i-dead",
		Manifest:   &Manifest{ID: "com.wash.ai"},
		router:     r,
	}
	live := &AppInstance{
		Transport:  livePair.EndA(),
		AppID:      "com.wash.agentd",
		InstanceID: "i-live",
		Manifest:   &Manifest{ID: "com.wash.agentd"},
		router:     r,
	}
	r.registerApp(dead)
	r.registerApp(live)

	r.tearDown(dead)

	f, err := livePair.EndB().ReadFrame()
	if err != nil {
		t.Fatalf("read instance.gone: %v", err)
	}
	if f.Channel != ChannelEvent {
		t.Fatalf("channel=%d, want event channel", f.Channel)
	}
	msg, err := wire.DecodeEvt(f.Payload)
	if err != nil {
		t.Fatalf("decode evt: %v", err)
	}
	gone, ok := msg.(wire.EvtInstanceGone)
	if !ok {
		t.Fatalf("message=%T, want EvtInstanceGone", msg)
	}
	if gone.AppID != "com.wash.ai" || gone.InstanceID != "i-dead" {
		t.Fatalf("gone=%+v", gone)
	}
}
