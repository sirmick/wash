// Package guest is the shared in-guest login front for both wash VMs (the
// in-browser RISC-V box and the wemu QEMU microvm). It owns the wash-wire
// channel device until a browser authenticates, then forks wash-router as the
// resolved Unix user, handing it the same channel fd (suid/fork handoff).
//
// It is the single piece that replaces the per-image "su wash -c wash-router"
// launcher loop with an auth gate: one user, serial, a root/diagnostic tool —
// so no session registry, no picker, no SCM_RIGHTS. See wash-vm/UNIFY.md.
//
// Login protocol (JSON control frames on channel 0, same frame envelope as
// asset.read):
//
//	FE  → front : {"t":"login","user":"…","pass":"…"}
//	front → FE  : {"t":"login.ok"}  | {"t":"login.err","msg":"…"}
//
// On login.ok the front forks `wash-router --transport=fd:3 --session-preopened`
// as the authed uid/gid (SpawnRouter), waits for that viewer session to end,
// then loops for the next login.
package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"github.com/sirmick/wash/pkg/wire"
)

// Identity is the resolved Unix account the front forks wash-router under. It
// mirrors internal/login.Identity but is redeclared here so the shared guest
// core carries no dependency on the HTTP login server package (which embeds
// staged shell assets). The cmd layer adapts one to the other.
type Identity struct {
	UID   uint32
	GID   uint32
	Name  string
	Shell string
}

// Authenticator validates a (user, password) pair, returning the Identity to
// fork under. The cmd wiring plugs in internal/login's su/passwd or test
// backend via a thin adapter.
type Authenticator interface {
	Authenticate(user, password string) (Identity, error)
}

// Login protocol message tags. Kept local (not in internal/wire's central
// dispatch) — this handshake is specific to the VM front and rides the frame
// envelope only; wash-router never sees these (the front consumes them before
// handoff).
const (
	msgLogin         = "login"
	msgLoginOK       = "login.ok"
	msgLoginErr      = "login.err"
	msgLoginRequired = "login.required"
)

type loginReq struct {
	T    string `json:"t"`
	User string `json:"user"`
	Pass string `json:"pass"`
}

type loginResp struct {
	T   string `json:"t"`
	Msg string `json:"msg,omitempty"`
}

// SpawnRouter forks wash-router for an authenticated identity and blocks until
// that viewer session ends (browser disconnect → SessionClose, or EOF). The
// real implementation (cmd wiring) forks `wash-router --transport=fd:3
// --session-preopened` with SysProcAttr.Credential set to id.UID/GID and the
// channel fd inherited as fd 3. Injected so Serve is unit-testable without a
// real fork.
type SpawnRouter func(ctx context.Context, id Identity) error

// Front is the login gate. Auth and Spawn are required; Logger defaults to a
// discarding logger.
type Front struct {
	Auth   Authenticator
	Spawn  SpawnRouter
	Logger *log.Logger
}

// Serve runs the login loop over t until the channel closes (ReadFrame EOF) or
// ctx is cancelled. A clean EOF returns nil — the guest launcher loop respawns
// the front on a genuine device close.
func (f *Front) Serve(ctx context.Context, t wire.FrameTransport) error {
	logf := f.logf
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fr, err := t.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		// Only channel-0 control frames matter pre-login. The proxy's
		// SessionOpen/SessionClose and anything else are harmlessly ignored
		// until a login frame arrives.
		if fr.Channel != 0 {
			continue
		}
		var probe struct {
			T string `json:"t"`
		}
		if err := json.Unmarshal(fr.Payload, &probe); err != nil {
			continue
		}
		// A fresh viewer attached (the proxy injects SessionOpen on browser
		// connect). Announce that this side is the login gate so the FE shows
		// the login form. A browser that instead reattaches to a live session
		// reaches wash-router (which owns the channel while the front blocks in
		// Spawn), sees the catalog rather than this prompt, and skips login.
		if probe.T == wire.TSessionOpen {
			f.reply(t, loginResp{T: msgLoginRequired})
			continue
		}
		if probe.T != msgLogin {
			continue
		}

		var req loginReq
		if err := json.Unmarshal(fr.Payload, &req); err != nil {
			f.reply(t, loginResp{T: msgLoginErr, Msg: "malformed login"})
			continue
		}
		id, err := f.Auth.Authenticate(req.User, req.Pass)
		if err != nil {
			logf("login: user=%q rejected: %v", req.User, err)
			// Never leak why — generic message only (see internal/login).
			f.reply(t, loginResp{T: msgLoginErr, Msg: "invalid credentials"})
			continue
		}
		logf("login: user=%q uid=%d ok — handing off to wash-router", id.Name, id.UID)
		if err := f.reply(t, loginResp{T: msgLoginOK}); err != nil {
			return err
		}
		if f.Spawn == nil {
			logf("login: no SpawnRouter configured")
			continue
		}
		// Blocks for the viewer session's lifetime. wash-router reads the
		// rest of the stream (router protocol + the trailing SessionClose)
		// off the same fd; the front MUST NOT touch the channel meanwhile.
		if err := f.Spawn(ctx, id); err != nil {
			logf("router session for uid=%d ended: %v", id.UID, err)
		}
		logf("router session ended — awaiting next login")
	}
}

// reply writes a JSON control frame on channel 0.
func (f *Front) reply(t wire.FrameTransport, v loginResp) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return t.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: payload})
}

func (f *Front) logf(format string, args ...any) {
	if f.Logger != nil {
		f.Logger.Printf(format, args...)
	}
}
