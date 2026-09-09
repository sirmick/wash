package session

import (
	"log"

	"github.com/sirmick/wash/pkg/sdk"
)

// registerLauncher installs the start-menu state handlers (launcher.go).
//
//	open.routed {path, app_id}   — from the ROUTER (open_routed.go): a file
//	                               reached its handler; remember it.
//	recent.open {path}           — FE clicked a Recent row: re-issue the open
//	                               through the router (CapOpen), so the same
//	                               ext → handler resolution applies.
//	recent.remove {path}         — FE context menu "Remove".
//	recent.clear                 — FE context menu "Clear recent".
//	launcher.pin {app_id, on}    — FE context menu Pin / Unpin.
//
// Every mutation pushes the full launcher.state back so the FE re-renders
// from one source. open.routed is registered as a PATTERN handler rather
// than HandleVoid: the bus only dispatches patterns for From-less messages,
// which is precisely the router-originated (or own-FE) case — a cross-app
// sender's forgery arrives with an attested From and never matches.
func registerLauncher(bus *sdk.Bus, store *launcherStore) {
	bus.HandlePattern("open.routed", func(c *sdk.Conn, _ string, data map[string]any) {
		path, _ := data["path"].(string)
		appID, _ := data["app_id"].(string)
		if path == "" {
			return
		}
		store.Note(path, appID)
		log.Printf("wash-session: recent noted path=%q app=%s", path, appID)
		sendLauncherState(c, store)
	})
	sdk.HandleVoid(bus, "recent.open", func(c *sdk.Conn, _ string, req recentPathReq) error {
		if req.Path == "" {
			return nil
		}
		log.Printf("wash-session: recent open path=%q", req.Path)
		return c.OpenPath(req.Path)
	})
	sdk.HandleVoid(bus, "recent.remove", func(c *sdk.Conn, _ string, req recentPathReq) error {
		store.Remove(req.Path)
		sendLauncherState(c, store)
		return nil
	})
	sdk.HandleVoid(bus, "recent.clear", func(c *sdk.Conn, _ string, _ struct{}) error {
		store.Clear()
		sendLauncherState(c, store)
		return nil
	})
	sdk.HandleVoid(bus, "launcher.pin", func(c *sdk.Conn, _ string, req launcherPinReq) error {
		store.SetPinned(req.AppID, req.On)
		log.Printf("wash-session: pin app=%s on=%v", req.AppID, req.On)
		sendLauncherState(c, store)
		return nil
	})
}

// sendLauncherState ships {kind:"launcher.state", recent:[…], pinned:[…]}
// to the FE. Recent entries are {path, app_id, at}; the FE derives the
// display name from the path.
func sendLauncherState(c *sdk.Conn, store *launcherStore) {
	snap := store.Snapshot()
	recent := make([]map[string]any, 0, len(snap.Recent))
	for _, e := range snap.Recent {
		recent = append(recent, map[string]any{
			"path":   e.Path,
			"app_id": e.AppID,
			"at":     e.At.UnixMilli(),
		})
	}
	if err := c.SendAppMsg(map[string]any{
		"kind":   "launcher.state",
		"recent": recent,
		"pinned": snap.Pinned,
	}); err != nil {
		log.Printf("wash-session: send launcher.state: %v", err)
	}
}

type recentPathReq struct {
	Path string `json:"path"`
}

type launcherPinReq struct {
	AppID string `json:"app_id"`
	On    bool   `json:"on"`
}
