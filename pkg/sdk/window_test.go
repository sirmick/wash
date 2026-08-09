package sdk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// connectForTest runs ConnectWith against a fake router on EndB,
// completing the handshake and returning the live Conn.
func connectForTest(t *testing.T, pp *wiretest.PipePair) *Conn {
	t.Helper()
	ch := make(chan *Conn, 1)
	go func() {
		c, err := ConnectWith(pp.EndA(), aboutDef(nil))
		if err != nil {
			t.Errorf("connect: %v", err)
			ch <- nil
			return
		}
		ch <- c
		_ = c.Run(context.Background()) // dispatch loop; ends on Close
	}()
	_ = readCtrl(t, pp.EndB()) // identity
	writeCtrl(t, pp.EndB(), wire.NewIdentityAck("i-1", 9))
	c := <-ch
	if c == nil {
		t.Fatal("connect failed")
	}
	return c
}

func TestCreateWindowReturnsAllocatedID(t *testing.T) {
	pp := wiretest.NewPipePair()
	c := connectForTest(t, pp)
	defer c.Close()

	type res struct {
		win uint32
		err error
	}
	rc := make(chan res, 1)
	go func() {
		w, err := c.CreateWindow(context.Background(), WindowOpts{Title: "Second", W: 300, H: 200})
		rc <- res{w, err}
	}()

	// Router side: expect the window.create, reply with window.created.
	cr, ok := readEvt(t, pp.EndB()).(wire.EvtWindowCreate)
	if !ok || cr.Title != "Second" || cr.W != 300 {
		t.Fatalf("expected EvtWindowCreate{Second,300}, got %+v", cr)
	}
	writeEvt(t, pp.EndB(), wire.NewEvtWindowCreated(cr.ReqID, 55))

	select {
	case got := <-rc:
		if got.err != nil || got.win != 55 {
			t.Fatalf("CreateWindow = (%d, %v), want (55, nil)", got.win, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateWindow hung")
	}

	// DestroyWindow is fire-and-forget on the event channel.
	if err := c.DestroyWindow(55); err != nil {
		t.Fatalf("DestroyWindow: %v", err)
	}
	if d, ok := readEvt(t, pp.EndB()).(wire.EvtWindowDestroy); !ok || d.Win != 55 {
		t.Fatalf("expected EvtWindowDestroy{55}, got %+v", d)
	}
}

func TestCreateWindowSurfacesRouterError(t *testing.T) {
	pp := wiretest.NewPipePair()
	c := connectForTest(t, pp)
	defer c.Close()

	rc := make(chan error, 1)
	go func() {
		_, err := c.CreateWindow(context.Background(), WindowOpts{Title: "Nope"})
		rc <- err
	}()

	cr, ok := readEvt(t, pp.EndB()).(wire.EvtWindowCreate)
	if !ok {
		t.Fatalf("expected EvtWindowCreate, got %+v", cr)
	}
	writeEvt(t, pp.EndB(), wire.NewEvtWindowCreateErr(cr.ReqID, wire.ErrCodeForbidden, "windows capability not declared"))

	select {
	case err := <-rc:
		if !errors.Is(err, ErrWindowCreate) {
			t.Fatalf("want ErrWindowCreate, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateWindow hung")
	}
}
