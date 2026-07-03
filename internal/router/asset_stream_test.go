package router

import (
	"context"
	"math/rand"
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// TestAssetReadDoesNotBlockDispatch is the regression for REVIEW-DATAPATH F5
// (input-stall half): a large asset must stream off the shell dispatch loop,
// so handleAssetRead returns promptly even when the FE link is stalled and
// the scheduler/pipe buffers are full. Before the fix the chunk loop ran
// inline and blocked in Submit, freezing the desktop's input for the whole
// transfer.
func TestAssetReadDoesNotBlockDispatch(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(string, ...any) {})

	// ~10 MiB of incompressible bytes: gzip won't shrink it, so the streamed
	// payload stays larger than the pipe (32) + Background queue (64) chunks
	// combined — guaranteeing the stream blocks in Submit once buffers fill.
	body := make([]byte, 10*1024*1024)
	rng := rand.New(rand.NewSource(1))
	rng.Read(body)
	r.SetAssets(http.FS(fstest.MapFS{"big.bin": &fstest.MapFile{Data: body}}))

	// A shell whose FE end (EndB) is deliberately never read: the drainLoop
	// blocks writing, buffers fill, and an inline stream would wedge here.
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport:   pp.EndA(),
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
		router:      r,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go sess.drainLoop(ctx)
	defer func() {
		sess.scheduler.Close()
		cancel()
		<-sess.drainerDone
		_ = pp.EndA().Close()
		_ = pp.EndB().Close()
	}()

	done := make(chan error, 1)
	go func() { done <- sess.handleAssetRead(wire.ShellAssetRead{ReqID: 1, Path: "/big.bin"}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleAssetRead: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handleAssetRead blocked on the dispatch loop — asset stream not offloaded to a goroutine")
	}
}
