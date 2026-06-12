// Package audiorelay is the producer-side bridge to the com.wash.audio
// control plane (docs/AUDIO.md §3), shared by every wash media app
// (Washamp, the native Music/Radio players, a future video player). Each
// producer BE calls Register(bus) once in its OnReady; the app's FE then
// owns playback and talks the audio_* contract to its own BE, which this
// relays cross-app to the service.
package audiorelay

import (
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// AudioAppID is the audio control plane. Declared as a string (not an
// import of the audio package) so producers stay independently buildable —
// the contract is the app-id + the cross-app message shapes.
const AudioAppID = "com.wash.audio"

// Register wires the bridge on bus:
//   - FE audio_register / audio_report / audio_unregister → forwarded
//     cross-app to the service (the router stamps THIS instance as the
//     source identity, so the service keys the source by our InstanceID);
//   - service `cmd` (transport/volume) → relayed back to the FE as a
//     wash:msg `audio.cmd`, which drives the producer's player.
func Register(bus *sdk.Bus) {
	to := func(conn *sdk.Conn, payload map[string]any) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, payload)
	}
	sdk.HandleVoid(bus, "audio_register", func(conn *sdk.Conn, _ string, req registerReq) error {
		// source kind omitted → the service defaults it to fe-decoded.
		return to(conn, map[string]any{"kind": "register", "title": req.Title, "artist": req.Artist})
	})
	sdk.HandleVoid(bus, "audio_report", func(conn *sdk.Conn, _ string, req reportReq) error {
		return to(conn, map[string]any{
			"kind": "report", "title": req.Title, "artist": req.Artist,
			"status": req.Status, "pos": req.Pos, "dur": req.Dur,
		})
	})
	sdk.HandleVoid(bus, "audio_unregister", func(conn *sdk.Conn, _ string, _ struct{}) error {
		return to(conn, map[string]any{"kind": "unregister"})
	})

	// service → FE: transport/volume command.
	sdk.HandleFromVoid(bus, "cmd", func(conn *sdk.Conn, _ string, req cmdReq, from wire.Sender) error {
		if from.AppID != AudioAppID {
			return nil
		}
		out := map[string]any{"kind": "audio.cmd", "action": req.Action}
		if req.Value != nil {
			out["value"] = *req.Value
		}
		return conn.SendAppMsg(out)
	})
}

type registerReq struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type reportReq struct {
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Status string  `json:"status"`
	Pos    float64 `json:"pos"`
	Dur    float64 `json:"dur"`
}

type cmdReq struct {
	Action string   `json:"action"`
	Value  *float64 `json:"value"`
}
