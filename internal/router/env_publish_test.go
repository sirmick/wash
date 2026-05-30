package router

import (
	"context"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// envManifest builds a surface=window manifest with the given caps,
// reusing the disptest identity so the router accepts the handshake.
func envManifest(caps ...string) *Manifest {
	m := multiWinManifest(caps...)
	return m
}

// hasEnv reports whether env contains exactly key=val.
func hasEnv(env []string, key, val string) bool {
	want := key + "=" + val
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// drainToIdentityAck performs the app handshake and consumes the initial
// window.mapped event, leaving the app ready to send further events.
func drainToIdentityAck(t *testing.T, app wire.FrameTransport) {
	t.Helper()
	writeCtrl(t, app, wire.NewIdentity("com.wash.disptest", ProtocolVersion, "0.8.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	if m, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || m.Win != ack.WindowID {
		t.Fatalf("expected EvtWindowMapped, got %+v", m)
	}
}

// TestEnvPublishMergedIntoSpawnEnv: a capable app's WASH_*-namespaced
// hints land in spawnEnv (so every later-spawned app, incl. wash-term's
// shell, inherits the compositor's DISPLAY). Non-WASH keys are dropped.
// See docs/DISPLAY_ENV.md.
func TestEnvPublishMergedIntoSpawnEnv(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	app := appPair.EndB()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), envManifest(CapEnvPublish), nil)
	}()
	drainToIdentityAck(t, app)

	writeEvt(t, app, wire.NewEvtEnvPublish(map[string]string{
		"WASH_X_DISPLAY":       ":2",
		"WASH_WAYLAND_DISPLAY": "wayland-0",
		"PATH":                 "/evil", // disallowed: not WASH_*
	}))

	// spawnEnv reads publishedEnv under a lock; poll briefly since the
	// publish is processed on the app's read goroutine.
	var env []string
	for i := 0; i < 200; i++ {
		env = r.spawnEnv()
		if hasEnv(env, "WASH_X_DISPLAY", ":2") {
			break
		}
	}
	if !hasEnv(env, "WASH_X_DISPLAY", ":2") {
		t.Errorf("WASH_X_DISPLAY not in spawnEnv: %v", env)
	}
	if !hasEnv(env, "WASH_WAYLAND_DISPLAY", "wayland-0") {
		t.Errorf("WASH_WAYLAND_DISPLAY not in spawnEnv: %v", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=/evil") {
			t.Errorf("disallowed PATH key leaked into spawnEnv: %v", env)
		}
	}

	appPair.Close()
	waitClose(t, appDone)
}

// TestEnvPublishRequiresCapability: an app without CapEnvPublish is
// refused and publishes nothing.
func TestEnvPublishRequiresCapability(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	app := appPair.EndB()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), envManifest(), nil)
	}()
	drainToIdentityAck(t, app)

	writeEvt(t, app, wire.NewEvtEnvPublish(map[string]string{"WASH_X_DISPLAY": ":2"}))

	errEvt, ok := readEvt(t, app).(wire.EvtEnvPublishErr)
	if !ok || errEvt.Code != wire.ErrCodeForbidden {
		t.Fatalf("expected EvtEnvPublishErr forbidden, got %+v", errEvt)
	}
	if hasEnv(r.spawnEnv(), "WASH_X_DISPLAY", ":2") {
		t.Errorf("uncapable app's env leaked into spawnEnv")
	}

	appPair.Close()
	waitClose(t, appDone)
}
