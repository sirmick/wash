package audio

import (
	"log"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

var svc *sdk.StateService[State]

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-audio ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	// Master volume starts at full; the sidebar slider drives it down.
	svc = sdk.NewStateService(bus, State{MasterVolume: 1.0})

	// --- producer → service (cross-app, From router-attested) ---

	// register: a producer announces a source. Keyed by the producer's
	// InstanceID so a re-register (reconnect) updates in place rather
	// than duplicating. Newly-registered sources go to the front.
	sdk.HandleFromVoid(bus, "register", func(_ *sdk.Conn, _ string, req registerReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		kind := req.Kind
		if kind != KindServerLive {
			kind = KindFEDecoded
		}
		svc.Mutate(func(s *State) {
			src := Source{
				ID:     from.InstanceID,
				AppID:  from.AppID,
				Kind:   kind,
				Title:  req.Title,
				Artist: req.Artist,
				Status: "stopped",
			}
			s.Sources = upsertFront(s.Sources, src)
		})
		// Hand the new producer the current master volume so it starts
		// at the user's chosen level rather than its own default.
		mv := svc.Snapshot().MasterVolume
		return c.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID},
			map[string]any{"kind": "cmd", "action": "volume", "value": mv})
	})

	// report: a producer pushes its live playback fields. When it went
	// playing, applyReport makes it the active source and returns the
	// other playing sources to pause (single-play exclusivity); we relay
	// a pause cmd to each. See docs/AUDIO.md §3.
	sdk.HandleFromVoid(bus, "report", func(conn *sdk.Conn, _ string, req reportReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		var pause []string
		svc.Mutate(func(s *State) { pause = applyReport(s, from.InstanceID, from.AppID, req) })
		for _, id := range pause {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: id},
				map[string]any{"kind": "cmd", "action": "pause"})
		}
		return nil
	})

	// unregister: producer is going away (window closed).
	sdk.HandleFromVoid(bus, "unregister", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		svc.Mutate(func(s *State) {
			s.Sources = removeByID(s.Sources, from.InstanceID)
			if s.ActiveID == from.InstanceID {
				s.ActiveID = ""
			}
		})
		return nil
	})

	// --- subscriber → service (the session sidebar gateway) ---

	// control: transport for one source. Relayed to the owning producer
	// by InstanceID (the source id).
	sdk.HandleFromVoid(bus, "control", func(conn *sdk.Conn, _ string, req controlReq, _ wire.Sender) error {
		if req.ID == "" || !validAction(req.Action) {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: req.ID},
			map[string]any{"kind": "cmd", "action": req.Action})
	})

	// set_master_volume: store it and fan a volume cmd to every producer
	// so they scale their output. M3's effective volume == master (per-
	// source volume is deferred, docs/AUDIO.md §3).
	sdk.HandleFromVoid(bus, "set_master_volume", func(conn *sdk.Conn, _ string, req volumeReq, _ wire.Sender) error {
		v := clamp01(req.Value)
		var ids []string
		svc.Mutate(func(s *State) {
			s.MasterVolume = v
			ids = make([]string, 0, len(s.Sources))
			for _, src := range s.Sources {
				ids = append(ids, src.ID)
			}
		})
		for _, id := range ids {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: id},
				map[string]any{"kind": "cmd", "action": "volume", "value": v})
		}
		return nil
	})

	// report_volume: a producer's own volume control moved (e.g. the Webamp
	// slider). Adopt it as the new master so the sidebar's slider tracks it
	// (the StateService push updates subscribers), and fan the change to the
	// OTHER producers so a single master still scales them all. The reporter
	// is skipped on purpose — it's already at this volume, and echoing the
	// cmd back would bounce through its onStateChange into another report.
	sdk.HandleFromVoid(bus, "report_volume", func(conn *sdk.Conn, _ string, req volumeReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		v := clamp01(req.Value)
		var ids []string
		svc.Mutate(func(s *State) {
			s.MasterVolume = v
			for _, src := range s.Sources {
				if src.ID != from.InstanceID {
					ids = append(ids, src.ID)
				}
			}
		})
		for _, id := range ids {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: id},
				map[string]any{"kind": "cmd", "action": "volume", "value": v})
		}
		return nil
	})
}

// applyReport updates s for a producer's report and enforces single-play
// exclusivity: when the producer went `playing`, it becomes the active
// source and every OTHER currently-playing source is flipped to paused
// and returned so the caller can relay it a pause cmd. Pure (no I/O) so
// the policy is unit-testable. See docs/AUDIO.md §3.
func applyReport(s *State, id, appID string, req reportReq) (pause []string) {
	found := false
	for i := range s.Sources {
		if s.Sources[i].ID == id {
			s.Sources[i].Title = req.Title
			s.Sources[i].Artist = req.Artist
			s.Sources[i].Status = req.Status
			s.Sources[i].PosSec = req.Pos
			s.Sources[i].DurSec = req.Dur
			found = true
			break
		}
	}
	if !found {
		// A report before register (race): adopt it as a source.
		s.Sources = upsertFront(s.Sources, Source{
			ID: id, AppID: appID, Kind: KindFEDecoded,
			Title: req.Title, Artist: req.Artist, Status: req.Status,
			PosSec: req.Pos, DurSec: req.Dur,
		})
	}
	if req.Status == "playing" {
		s.ActiveID = id
		for i := range s.Sources {
			if s.Sources[i].ID != id && s.Sources[i].Status == "playing" {
				pause = append(pause, s.Sources[i].ID)
				s.Sources[i].Status = "paused"
			}
		}
	}
	return pause
}

// upsertFront replaces a same-id source in place (preserving order) or
// prepends a new one.
func upsertFront(srcs []Source, src Source) []Source {
	for i := range srcs {
		if srcs[i].ID == src.ID {
			srcs[i] = src
			return srcs
		}
	}
	return append([]Source{src}, srcs...)
}

func removeByID(srcs []Source, id string) []Source {
	out := srcs[:0]
	for _, s := range srcs {
		if s.ID != id {
			out = append(out, s)
		}
	}
	return out
}

func validAction(a string) bool {
	switch a {
	case "play", "pause", "next", "prev", "stop":
		return true
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

type registerReq struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	// Kind is the source data plane. Tagged source_kind (not "kind")
	// because "kind" is the message envelope's own field — a producer
	// sends source_kind only for server-live; fe-decoded is the default.
	Kind string `json:"source_kind"`
}

type reportReq struct {
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Status string  `json:"status"`
	Pos    float64 `json:"pos"`
	Dur    float64 `json:"dur"`
}

type controlReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type volumeReq struct {
	Value float64 `json:"value"`
}
