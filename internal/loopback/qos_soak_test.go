package loopback

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// TestQoSSoakInteractiveLatencyUnderBulkFlood is the loopback-level
// realization of QOS.md's Phase 6a exit criterion:
//
//	"under a sustained `cat 100MB` from wash-term, …
//	 *other* apps' interactive frames stay <50ms p99."
//
// Setup (single-process, no real apps):
//   - Real router via in-memory pipes.
//   - Real SDK connected as one app with a registered window.
//   - One goroutine on the SDK side floods EvtAppMsgBulk frames
//     (modelling pty output).
//   - Another goroutine on the SDK side periodically sends a small
//     EvtAppMsg (Interactive) with an embedded send-time so the
//     receiver can compute one-way latency.
//   - The shell-side reader drains every frame, recording arrival
//     time for Interactive deliveries.
//
// What this proves: the router's scheduler + per-class queues let
// Interactive `app_msg.deliver` frames overtake Bulk ones at the
// shell-bound writer, even when Bulk producers are saturating the
// connection at SDK-write speed.
//
// What it does NOT prove: real-browser DOM render latency. That's
// a Playwright soak — out of scope here.
func TestQoSSoakInteractiveLatencyUnderBulkFlood(t *testing.T) {
	t.Parallel()

	const bulkFrames = 5000     // ~5k bulk frames → multi-second flood
	const bulkPayloadSize = 4096 // ~4 KiB per frame, like a pty output chunk
	const interactiveFrames = 50
	const interactiveInterval = 2 * time.Millisecond
	// p99 latency cap. Generous because we're at Go-test scheduling
	// granularity, not bare-metal: ~5-10ms p99 is realistic, 50ms
	// catches obvious regressions.
	const p99Cap = 50 * time.Millisecond

	manifest := sdk.Manifest{
		ID:              "com.wash.test",
		Name:            "QoS Soak",
		Version:         "0.0.0",
		ProtocolVersion: sdk.ProtocolVersion,
		Element:         "wash-app-test",
		Surface:         sdk.SurfaceWindow,
		Icon:            "data:image/svg+xml,Q",
		Instancing:      sdk.InstancingMulti,
		Window:          &sdk.WindowHints{DefaultWidth: 320, DefaultHeight: 200},
	}
	rtrManifest := &router.Manifest{
		ID:              manifest.ID,
		Name:            manifest.Name,
		Version:         manifest.Version,
		ProtocolVersion: router.ProtocolVersion,
		Element:         manifest.Element,
		Surface:         router.SurfaceWindow,
		Icon:            manifest.Icon,
		Instancing:      router.InstancingMulti,
		Window:          &router.WindowHints{DefaultWidth: 320, DefaultHeight: 200},
	}

	reg := router.NewRegistry()
	r := router.NewRouter(router.Config{}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	routerAppDone := make(chan struct{})
	go func() {
		_ = r.HandleApp(ctx, appPair.EndA(), rtrManifest, nil)
		close(routerAppDone)
	}()
	routerShellDone := make(chan struct{})
	go func() {
		_ = r.HandleShell(ctx, shellPair.EndA())
		close(routerShellDone)
	}()

	def := &sdk.AppDef{
		Manifest: manifest,
		Assets:   fstest.MapFS{"index.js": &fstest.MapFile{Data: []byte("/* test */")}},
	}
	c, err := sdk.ConnectWith(appPair.EndB(), def)
	if err != nil {
		t.Fatalf("sdk connect: %v", err)
	}
	sdkLoopDone := make(chan error, 1)
	go func() { sdkLoopDone <- c.Run(ctx) }()

	shell := shellPair.EndB()

	// Drain shell-side ctrl frames until the window upsert lands —
	// proves the handshake completed and the app is "live" before
	// we start measuring.
	for {
		f, err := shell.ReadFrame()
		if err != nil {
			t.Fatalf("read pre-soak: %v", err)
		}
		if f.Channel != 0 {
			continue
		}
		msg, err := wire.DecodeCtrl(f.Payload)
		if err != nil {
			continue
		}
		if _, ok := msg.(wire.ShellSessionSnapshot); ok {
			break
		}
		if _, ok := msg.(wire.ShellSessionPatch); ok {
			// Patches arrive after snapshot — also a signal we're live.
			break
		}
	}

	// Drainer: read every shell-bound frame and timestamp anything
	// that carries an interactive `qos.tick` payload.
	type sample struct {
		sentAt time.Time
		gotAt  time.Time
	}
	samples := make(chan sample, interactiveFrames+8)
	drainerErr := make(chan error, 1)
	drainerDone := make(chan struct{})
	go func() {
		defer close(drainerDone)
		for {
			f, err := shell.ReadFrame()
			if err != nil {
				drainerErr <- err
				return
			}
			if f.Channel != 0 {
				continue
			}
			msg, err := wire.DecodeCtrl(f.Payload)
			if err != nil {
				continue
			}
			d, ok := msg.(wire.ShellAppMsgDeliver)
			if !ok {
				continue
			}
			var payload struct {
				Kind   string `json:"kind"`
				SentNs int64  `json:"sent_ns"`
			}
			if err := json.Unmarshal(d.Data, &payload); err != nil {
				continue
			}
			if payload.Kind != "qos.tick" {
				continue
			}
			samples <- sample{
				sentAt: time.Unix(0, payload.SentNs),
				gotAt:  time.Now(),
			}
		}
	}()

	// Bulk producer: SendAppMsgBulk in a tight loop.
	bulkPayload := make([]byte, bulkPayloadSize)
	for i := range bulkPayload {
		bulkPayload[i] = byte(i)
	}
	bulkDone := make(chan struct{})
	go func() {
		defer close(bulkDone)
		for i := 0; i < bulkFrames; i++ {
			if err := c.SendAppMsgBulk(map[string]any{
				"kind": "bulk.chunk",
				"seq":  i,
				"data": bulkPayload,
			}); err != nil {
				return
			}
		}
	}()

	// Give bulk a head start so its queue fills before interactive
	// frames start landing — this is the realistic scenario.
	time.Sleep(20 * time.Millisecond)

	// Interactive producer: SendAppMsg with a sent_ns timestamp.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(interactiveInterval)
		defer tick.Stop()
		for i := 0; i < interactiveFrames; i++ {
			<-tick.C
			now := time.Now()
			_ = c.SendAppMsg(map[string]any{
				"kind":    "qos.tick",
				"seq":     i,
				"sent_ns": now.UnixNano(),
			})
		}
	}()
	wg.Wait()

	// Collect samples with a deadline. Some bulk frames may still be
	// in flight; that's fine — we only care about the interactive
	// ones.
	collected := make([]sample, 0, interactiveFrames)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
collect:
	for len(collected) < interactiveFrames {
		select {
		case s := <-samples:
			collected = append(collected, s)
		case <-deadline.C:
			break collect
		}
	}
	if len(collected) < interactiveFrames/2 {
		t.Fatalf("only collected %d/%d interactive samples — drainer or scheduler is stuck", len(collected), interactiveFrames)
	}

	// Compute latency p50/p95/p99.
	lats := make([]time.Duration, 0, len(collected))
	for _, s := range collected {
		lats = append(lats, s.gotAt.Sub(s.sentAt))
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p := func(pct float64) time.Duration {
		if len(lats) == 0 {
			return 0
		}
		idx := int(float64(len(lats)-1) * pct)
		return lats[idx]
	}
	p50, p95, p99 := p(0.50), p(0.95), p(0.99)
	t.Logf("interactive latency: n=%d  p50=%v  p95=%v  p99=%v  max=%v",
		len(lats), p50, p95, p99, lats[len(lats)-1])

	if p99 > p99Cap {
		t.Errorf("interactive p99 latency %v exceeds cap %v under sustained bulk load (collected %d samples)", p99, p99Cap, len(lats))
	}

	// Don't strand the drainer — it'll exit when shell pipe closes.
	cancel()
	<-bulkDone
	_ = shellPair.EndA().Close()
	_ = shellPair.EndB().Close()
	select {
	case <-drainerDone:
	case <-time.After(500 * time.Millisecond):
	}
}
