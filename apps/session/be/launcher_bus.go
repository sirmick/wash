package session

import (
	"log"
	"os"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// RadioAppID is the one app recent.play knows how to replay into.
const RadioAppID = "com.wash.radio"

// FmAppID is the file manager, whose recent entries are places.
const FmAppID = "com.wash.fm"

// registerLauncher installs the start-menu state handlers (launcher.go).
//
//	open.routed {path, app_id}   — from the ROUTER (open_routed.go): a file
//	                               reached its handler; remember it.
//	recent.note {path?, name?}   — from ANOTHER APP: remember something no
//	                               open request describes (fm's last folder,
//	                               Radio's station). The app id is the
//	                               router-attested sender's, never the
//	                               payload's, so an app can only ever add
//	                               entries under its own name.
//	recent.open {path, app_id}   — FE clicked a Recent row: re-issue the open
//	                               through the router (CapOpen), so the same
//	                               ext → handler resolution applies. A folder
//	                               has no extension to resolve, so it is
//	                               spawned on the app that recorded it.
//	recent.play {app_id, name}   — FE clicked a Radio row: raise or start
//	                               Radio, then tell that instance to tune.
//	recent.remove {path|name}    — FE context menu "Remove".
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
	sdk.HandleFromVoid(bus, "recent.note", func(c *sdk.Conn, _ string, req recentNoteReq, from wire.Sender) error {
		if from.AppID == "" {
			return nil
		}
		switch {
		case req.Path != "":
			store.Note(req.Path, from.AppID)
			log.Printf("wash-session: recent noted path=%q app=%s via=note", req.Path, from.AppID)
		case req.Name != "":
			store.NoteName(req.Name, from.AppID)
			log.Printf("wash-session: recent noted name=%q app=%s via=note", req.Name, from.AppID)
		default:
			return nil
		}
		sendLauncherState(c, store)
		return nil
	})
	sdk.HandleVoid(bus, "recent.open", func(c *sdk.Conn, _ string, req recentPathReq) error {
		if req.Path == "" {
			return nil
		}
		// fm is spawned even on a file: an entry it recorded is a place to
		// go (it lists the file's folder), not a document to open.
		if req.AppID != "" && (isDir(req.Path) || req.AppID == FmAppID) {
			log.Printf("wash-session: recent open dir=%q app=%s", req.Path, req.AppID)
			return c.SpawnRequestOpen(req.AppID, req.Path)
		}
		log.Printf("wash-session: recent open path=%q", req.Path)
		return c.OpenPath(req.Path)
	})
	sdk.HandleVoid(bus, "recent.play", func(c *sdk.Conn, _ string, req recentPathReq) error {
		if req.AppID != RadioAppID || req.Name == "" {
			return nil
		}
		log.Printf("wash-session: recent play app=%s name=%q", req.AppID, req.Name)
		pendingPlay.set(req.AppID, req.Name)
		if err := c.SpawnRequest(req.AppID); err != nil {
			pendingPlay.take(req.AppID)
			return err
		}
		return nil
	})
	sdk.HandleVoid(bus, "recent.remove", func(c *sdk.Conn, _ string, req recentPathReq) error {
		store.Remove(req.Path, req.Name, req.AppID)
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
// to the FE. Recent entries are {path, app_id, at, name?}; the FE derives
// a file's display name from its path.
func sendLauncherState(c *sdk.Conn, store *launcherStore) {
	snap := store.Snapshot()
	recent := make([]map[string]any, 0, len(snap.Recent))
	for _, e := range snap.Recent {
		entry := map[string]any{
			"path":   e.Path,
			"app_id": e.AppID,
			"at":     e.At.UnixMilli(),
		}
		if e.Name != "" {
			entry["name"] = e.Name
		}
		recent = append(recent, entry)
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
	Path  string `json:"path"`
	Name  string `json:"name"`
	AppID string `json:"app_id"`
}

type recentNoteReq struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// pendingPlay holds the station a recent.play asked for until the router
// answers the spawn with the Radio instance to send it to. Latest click
// wins: two quick clicks tune to the second station, not both in turn.
var pendingPlay = &playQueue{names: map[string]string{}}

type playQueue struct {
	mu    sync.Mutex
	names map[string]string
}

func (q *playQueue) set(appID, name string) {
	q.mu.Lock()
	q.names[appID] = name
	q.mu.Unlock()
}

func (q *playQueue) take(appID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := q.names[appID]
	delete(q.names, appID)
	return name
}

// onSpawnResult finishes a recent.play: the router has raised or started
// Radio, so the instance id is now known.
func onSpawnResult(c *sdk.Conn, appID, instanceID string, err error) {
	name := pendingPlay.take(appID)
	if name == "" {
		return
	}
	if err != nil {
		log.Printf("wash-session: recent play spawn app=%s: %v", appID, err)
		return
	}
	if e := c.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, map[string]any{
		"kind": "play_station",
		"name": name,
	}); e != nil {
		log.Printf("wash-session: recent play send instance=%s: %v", instanceID, e)
		return
	}
	log.Printf("wash-session: recent play sent instance=%s name=%q", instanceID, name)
}

type launcherPinReq struct {
	AppID string `json:"app_id"`
	On    bool   `json:"on"`
}
