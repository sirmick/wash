// Package audio is wash-audio (com.wash.audio) — the audio control
// plane (docs/AUDIO.md §3). It is a singleton background service that
// holds the mixer state: which sources are making (or able to make)
// sound, plus the master volume/mute. It owns NO audio bytes — playback
// stays in each producer's FE (the browser is the only sink). The
// service is the one global observer of all sources, so it is also where
// ducking/exclusivity policy lands later.
//
// Producers (e.g. com.wash.washamp) register and report their playback
// over cross-app app_msg; the router stamps the producer's attested
// identity, and the service keys each source by the producer's
// InstanceID (never a caller-supplied id). Subscribers (the session
// sidebar, via its BE gateway) get the AudioState snapshot through the
// standard sdk.StateService subscribe-with-snapshot protocol and issue
// transport/volume commands, which the service relays back to the
// owning producer.
//
// Wire shape — inbound from a producer (cross-app, From attested):
//
//	{kind:"register",   title, artist, kind}   // kind: "fe-decoded"|"server-live"
//	{kind:"report",     title, artist, status, pos, dur}
//	{kind:"unregister"}
//
// Wire shape — inbound from a subscriber (the session gateway):
//
//	{kind:"control",            id, action}    // action: play|pause|next|prev|stop
//	{kind:"set_master_volume",  value}         // 0..1
//
// Wire shape — outbound to a producer (transport/volume relay):
//
//	{kind:"cmd", action, value}                // action: play|pause|next|prev|stop|volume
//
// Wire shape — state pushed to subscribers (sdk.StateService):
//
//	{kind:"state", state:{ sources:[…], master_volume, master_mute }}
package audio

import (
	"context"
	"github.com/sirmick/wash/internal/version"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
)

// AppID is the reserved app id for the audio control plane.
const AppID = "com.wash.audio"

// Kind values for a source's data plane (docs/AUDIO.md §1). M3 only
// produces fe-decoded sources; server-live arrives with Case 2.
const (
	KindFEDecoded  = "fe-decoded"
	KindServerLive = "server-live"
)

// State is the public mixer state. Sources is ordered most-recently-
// registered first.
type State struct {
	Sources []Source `json:"sources"`
	// ActiveID is the source the sidebar shows + drives: whichever last
	// went playing, kept across pause so the widget still targets "the
	// thing you'd resume". Empty before anything plays (the widget then
	// falls back to the front source). See docs/AUDIO.md §3.
	ActiveID     string  `json:"active_id"`
	MasterVolume float64 `json:"master_volume"`
	MasterMute   bool    `json:"master_mute"`
}

// Source is one thing making (or able to make) sound. ID is the
// producer's router-attested InstanceID — stable for the producer's
// lifetime and the address the service relays commands back to.
type Source struct {
	ID     string  `json:"id"`
	AppID  string  `json:"app_id"`
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Status string  `json:"status"` // playing | paused | stopped
	PosSec float64 `json:"pos_sec"`
	DurSec float64 `json:"dur_sec"`
}

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Audio",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-audio",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }
