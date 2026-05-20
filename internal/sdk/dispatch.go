package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/fxamacker/cbor/v2"
	"github.com/sirmick/wash/internal/wire"
)

// Run is the Tier-2 event loop: reads frames from the router and
// dispatches them to AppDef callbacks. Returns when the transport
// closes (ErrConnClosed) or ctx cancels.
func (c *Conn) Run(ctx context.Context) error {
	type rr struct {
		f   wire.Frame
		err error
	}
	ch := make(chan rr, 1)
	go func() {
		for {
			f, err := c.transport.ReadFrame()
			ch <- rr{f, err}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-ch:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return ErrConnClosed
				}
				return r.err
			}
			if err := c.dispatch(r.f); err != nil {
				return err
			}
		}
	}
}

func (c *Conn) dispatch(f wire.Frame) error {
	switch f.Channel {
	case channelControl:
		return c.dispatchCtrl(f.Payload)
	case channelEvent:
		return c.dispatchEvt(f.Payload)
	}
	// v0.0 reserves channels ≥ 2; drop unknown.
	return nil
}

func (c *Conn) dispatchCtrl(payload []byte) error {
	msg, err := wire.DecodeCtrl(payload)
	if err != nil {
		return fmt.Errorf("ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.AssetRead:
		return c.serveAsset(m)
	case wire.Error:
		return fmt.Errorf("router error: %s (%s)", m.Msg, m.Code)
	}
	// Anything else on app-side channel 0 post-handshake is a
	// surprise; ignore to keep forward compat with additive changes.
	return nil
}

func (c *Conn) dispatchEvt(payload []byte) error {
	t, err := wire.PeekEvtType(payload)
	if err != nil {
		return fmt.Errorf("evt peek: %w", err)
	}
	switch t {
	case wire.TEvtWindowMapped:
		var m wire.EvtWindowMapped
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnMapped != nil {
			c.def.OnMapped(c, m.Win)
		}
	case wire.TEvtWindowFocus:
		var m wire.EvtWindowFocus
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnFocus != nil {
			c.def.OnFocus(c, m.Win)
		}
	case wire.TEvtWindowUnfocus:
		var m wire.EvtWindowUnfocus
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnUnfocus != nil {
			c.def.OnUnfocus(c, m.Win)
		}
	case wire.TEvtWindowCloseRequested:
		var m wire.EvtWindowCloseRequested
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		allow := true
		if c.def.OnCloseRequested != nil {
			allow = c.def.OnCloseRequested(c, m.Win)
		}
		return c.ConfirmClose(m.Win, allow)
	case wire.TEvtShutdown:
		if c.def.OnShutdown != nil {
			c.def.OnShutdown(c)
		}
	case wire.TEvtAppMsg:
		var m wire.EvtAppMsg
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnAppMsg != nil {
			c.def.OnAppMsg(c, m.Win, m.Data)
		}
	case wire.TEvtSpawnOk:
		var m wire.EvtSpawnOk
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnSpawnResult != nil {
			c.def.OnSpawnResult(c, m.AppID, m.InstanceID, nil)
		}
	case wire.TEvtSpawnErr:
		var m wire.EvtSpawnErr
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnSpawnResult != nil {
			c.def.OnSpawnResult(c, m.AppID, "", fmt.Errorf("%s: %s", m.Code, m.Msg))
		}
	}
	// Unknown event types: ignore for forward compat.
	return nil
}
