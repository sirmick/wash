package sdk

import (
	"context"
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/sirmick/wash/internal/wire"
)

// Run is the Tier-2 event loop: reads frames from the router and
// dispatches them to AppDef callbacks. Returns when the transport
// closes (ErrConnClosed) or ctx cancels.
func (c *Conn) Run(ctx context.Context) error {
	err := wire.ReadLoop(ctx, c.transport, c.dispatch)
	if err == nil {
		return ErrConnClosed
	}
	return err
}

func (c *Conn) dispatch(f wire.Frame) error {
	switch f.Channel {
	case channelControl:
		return c.dispatchCtrl(f.Payload)
	case channelEvent:
		return c.dispatchEvt(f.Payload)
	}
	// Channel ≥ 2: raw bytes for a dynamic channel.
	rc := c.lookupChannel(f.Channel)
	if rc == nil {
		// Bytes for a channel we no longer track — drop. Happens
		// briefly between local Close and the router's ChannelClosed.
		return nil
	}
	// Copy the payload — the frame's buffer may be reused on the
	// next read.
	b := make([]byte, len(f.Payload))
	copy(b, f.Payload)
	rc.deliver(b)
	return nil
}

func (c *Conn) dispatchCtrl(payload []byte) error {
	msg, err := wire.DecodeCtrl(payload)
	if err != nil {
		return fmt.Errorf("ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.ChannelOpened:
		rc := newRawChannel(m.ChannelID, c)
		c.registerChannel(rc)
		if !c.resolveOpen(m.ReqID, openResult{ch: rc}) {
			// Open was abandoned (ctx cancelled). Release the id.
			c.removeChannel(m.ChannelID)
			_ = c.writeCtrl(wire.NewChannelClose(m.ChannelID))
		}
		return nil
	case wire.ChannelOpenErr:
		c.resolveOpen(m.ReqID, openResult{err: channelOpenErrFromMsg(m)})
		return nil
	case wire.ChannelClosed:
		rc := c.lookupChannel(m.ChannelID)
		if rc != nil {
			c.removeChannel(m.ChannelID)
			rc.teardown(fmt.Errorf("router closed channel: %s", m.Reason))
		}
		return nil
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
	case wire.TEvtWindowResize:
		var m wire.EvtWindowResize
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnResize != nil {
			c.def.OnResize(c, m.Win, m.W, m.H)
		}
	case wire.TEvtWindowState:
		var m wire.EvtWindowState
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnState != nil {
			c.def.OnState(c, m.Win, m.State)
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
		// Intercept wash-priv replies that belong to an in-flight
		// PrivRunInlineSync. Other priv messages and all non-priv
		// senders fall through to the user's callbacks.
		if m.From != nil && m.From.AppID == "com.wash.priv" {
			if c.tryConsumePrivReply(m.Data) {
				return nil
			}
		}
		if m.From != nil && c.def.OnAppMsgFrom != nil {
			c.def.OnAppMsgFrom(c, m.Win, m.Data, *m.From)
		} else if c.def.OnAppMsg != nil {
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
	case wire.TEvtPrepareSpawnOk:
		var m wire.EvtPrepareSpawnOk
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnPrepareSpawnResult != nil {
			c.def.OnPrepareSpawnResult(c, m.ReqID, m.InstanceID, m.AttachToken, m.Binary, nil)
		}
	case wire.TEvtPrepareSpawnErr:
		var m wire.EvtPrepareSpawnErr
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnPrepareSpawnResult != nil {
			c.def.OnPrepareSpawnResult(c, m.ReqID, "", "", "", fmt.Errorf("%s: %s", m.Code, m.Msg))
		}
	case wire.TEvtClipboardData:
		var m wire.EvtClipboardData
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		c.clipMu.Lock()
		ch, ok := c.pendingClipboardGet[m.ReqID]
		if ok {
			delete(c.pendingClipboardGet, m.ReqID)
		}
		c.clipMu.Unlock()
		if ok {
			ch <- clipboardResult{mime: m.Mime, data: m.Data}
		}
	case wire.TEvtClipboardChanged:
		var m wire.EvtClipboardChanged
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		if c.def.OnClipboardChanged != nil {
			c.def.OnClipboardChanged(c, m.Mime)
		}
	}
	// Unknown event types: ignore for forward compat.
	return nil
}
