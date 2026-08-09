package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// TestAppInitiatedWindowClose: an UNSOLICITED confirm_close(allow=true)
// on the app's primary window — no close_clicked handshake pending — is
// the app asking to close itself (wash-term after its last tab exits, or
// after its FE confirmed a close the app had earlier vetoed to show a
// dialog). The router must destroy the window and tear the app down;
// before approveWindowClose this was silently dropped and the window
// lingered.
func TestAppInitiatedWindowClose(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) { t.Logf("router: "+format, args...) })

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()
	app := appPair.EndB()
	shell := shellPair.EndB()

	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.EndA(), multiWinManifest(), nil)
	}()
	shellDone := make(chan struct{})
	go func() { defer close(shellDone); _ = r.HandleShell(context.Background(), shellPair.EndA()) }()

	writeCtrl(t, app, wire.NewIdentity("com.wash.disptest", ProtocolVersion, "0.9.0"))
	ack, ok := readCtrl(t, app).(wire.IdentityAck)
	if !ok || ack.WindowID == 0 {
		t.Fatalf("expected IdentityAck with a window, got %+v", ack)
	}
	primary := ack.WindowID
	if m, ok := readEvt(t, app).(wire.EvtWindowMapped); !ok || m.Win != primary {
		t.Fatalf("expected EvtWindowMapped(%d), got %+v", primary, m)
	}
	waitWindowUpsert(t, shell, primary)

	// No close handshake is in flight; the app asks to close itself.
	writeEvt(t, app, wire.NewEvtWindowConfirmClose(primary, true))

	// The shell must see the window go, and the app connection must be
	// torn down (token-attach branch closes the transport, which ends
	// HandleApp's read loop).
	waitWindowDelete(t, shell, primary)
	waitClose(t, appDone)

	shellPair.Close()
	waitClose(t, shellDone)
}
