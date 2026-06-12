package washamp

import (
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// AudioAppID is the audio control plane (docs/AUDIO.md §3). Declared
// locally (not imported) so the two apps stay independently buildable —
// the contract is the app-id string + the cross-app message shapes.
const AudioAppID = "com.wash.audio"

// registerAudioRelay bridges the music FE and the com.wash.audio control
// plane. The FE owns playback (webamp); this BE is a typed pipe:
//   - FE register/report/unregister → forwarded cross-app to the service
//     (the router stamps THIS music instance as the source identity, so
//     the service keys the source by our InstanceID).
//   - service `cmd` (transport/volume) → relayed to the FE as audio.cmd,
//     which drives webamp.
func registerAudioRelay(bus *sdk.Bus) {
	to := func(conn *sdk.Conn, payload map[string]any) error {
		return conn.SendAppMsgTo(wire.Recipient{AppID: AudioAppID}, payload)
	}
	sdk.HandleVoid(bus, "audio_register", func(conn *sdk.Conn, _ string, req audioRegisterReq) error {
		// source kind omitted → the service defaults it to fe-decoded.
		return to(conn, map[string]any{"kind": "register", "title": req.Title, "artist": req.Artist})
	})
	sdk.HandleVoid(bus, "audio_report", func(conn *sdk.Conn, _ string, req audioReportReq) error {
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

type audioRegisterReq struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type audioReportReq struct {
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
