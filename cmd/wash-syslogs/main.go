// wash-syslogs — viewer for classic /var/log/*.log files.
//
// Sibling to wash-journal. Where the journal app drives journalctl
// for structured systemd records, this app reads plain syslog-style
// text files: one selection = one open file tailed with `tail -F`.
//
// Use cases:
//   - OpenRC / Alpine / Gentoo / Devuan / non-systemd boxes.
//   - Auth / apache / nginx logs that are written outside the
//     journal even on systemd boxes.
//   - Containers / chroots without a running journald.
//
// Privilege: most /var/log/* files are root:adm 640. Users in `adm`
// can read system logs unprivileged; everyone else gets the same
// perm-denied → "Read with root" banner the journal app uses, OR
// can launch this app via the "Root System Logs" synthetic launcher
// entry (parallel to Root Terminal) so the binary starts as uid 0.
//
// Wire shapes (BE→FE):
//   {kind:"files", files:[{path, name, size, mtime}]}
//   {kind:"lines", gen, lines:[LogEntry]}
//   {kind:"stream_state", gen, status, error}
//
// Wire (FE→BE):
//   {kind:"select", path, as_root}
//   {kind:"refresh_files"}
//   {kind:"stop"}

package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"sync"
	"sync/atomic"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// syslogsIcon — text-search reads as "look through log text" without
// stepping on journal's scroll-text or edit's file-pen.
const syslogsIcon = "text-search"

type be struct {
	conn *sdk.Conn

	mu      sync.Mutex
	stream  *streamCtl
	nextGen atomic.Int64
}

var st *be

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-syslogs: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.syslogs",
			Name:            "System Logs",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-syslogs",
			Surface:         sdk.SurfaceWindow,
			Icon:            syslogsIcon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 560},
			// /var/log/* is mostly root:adm 640; many users will want
			// the root variant by default. The synthetic launcher row
			// makes that one-click.
			RootVariant: &sdk.RootVariant{},
		},
		Assets:       sub,
		OnReady:      onReady,
		OnAppMsgFrom: onAppMsgFrom,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-syslogs ready instance=%s window=%d uid=%d", instanceID, windowID, currentUID())
	st = &be{conn: c}
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	go pushFiles(c)
	// No auto-stream on launch — unlike wash-journal there's no
	// natural "all sources merged" view; the user picks a file.
}

// ----- FE → BE bus -----

type selectReq struct {
	Path   string `cbor:"path"`
	AsRoot bool   `cbor:"as_root"`
}

type emptyReq struct{}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "select", func(c *sdk.Conn, _ string, req selectReq) error {
		go startStream(c, req)
		return nil
	})
	sdk.HandleVoid(b, "refresh_files", func(c *sdk.Conn, _ string, _ emptyReq) error {
		go pushFiles(c)
		return nil
	})
	sdk.HandleVoid(b, "stop", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		stopStream()
		return nil
	})
}

// onAppMsgFrom — same priv-stream dispatch as wash-journal. Only
// wash-priv senders are honored; everything else is dropped.
func onAppMsgFrom(_ *sdk.Conn, _ uint32, data any, from wire.Sender) {
	if from.AppID != sdk.PrivAppID {
		return
	}
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	reqID := sdk.ToString(m["req_id"])
	if reqID == "" {
		return
	}
	st.mu.Lock()
	s := st.stream
	st.mu.Unlock()
	if s == nil || s.privReqID != reqID {
		return
	}
	switch sdk.ToString(m["kind"]) {
	case "priv.stream":
		stream := sdk.ToString(m["stream"])
		raw := sdk.DecodeBase64(m["bytes"])
		if len(raw) > 0 {
			s.feedPriv(stream, raw)
		}
	case "priv.result":
		s.finishPriv(int(sdk.ToInt64(m["exit_code"])), sdk.ToString(m["error"]))
	case "rejected":
		s.finishPriv(1, "rejected: "+sdk.ToString(m["reason"]))
	}
}

func stopStream() {
	st.mu.Lock()
	s := st.stream
	st.stream = nil
	st.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func startStream(c *sdk.Conn, req selectReq) {
	if req.Path == "" {
		return
	}
	gen := st.nextGen.Add(1)
	stopStream()

	s := newStream(c, gen, req)
	st.mu.Lock()
	st.stream = s
	st.mu.Unlock()

	if req.AsRoot {
		s.runPriv(context.Background())
	} else {
		s.runDirect(context.Background())
	}
}
