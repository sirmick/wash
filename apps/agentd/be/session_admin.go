// Renaming and deleting sessions (docs/Review-findings.md P2 → agent).
//
// A session's title is the AGENT's: it names its own work once on
// session_info_update, first wins, and nothing could change it after. That
// is right as a default and wrong as a rule — "Fix the reconnect banner
// race" is a fine name until the session turns into three days of
// something else, and a session the agent never named is "codex · wash"
// forever. So a person can name one, and their name wins wherever the
// title is shown: the roster row, the window title, the History menu and
// panel. The agent's own title is kept underneath, not overwritten, so
// clearing the user's name falls back to it.
//
// The name lives in two places on purpose. The history entry
// (agent-sessions.json) is what the Recent menu reads and is capped; the
// transcript file is what the History panel reads and is not. A summary
// record appended to the transcript carries it (the store is append-only,
// and the last summary wins — transcript_store.go), so a session older
// than the history cap keeps its name too.
//
// Deleting is the other half nothing offered: the store grew without bound
// and the only way to lose a conversation was `rm` in the state dir. A
// stored session can be deleted one at a time or pruned by age; a LIVE one
// cannot — its adapter is running and its file is being written — so the
// verb refuses rather than pulling the file out from under a session.
package agentd

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// maxUserTitle bounds a name typed into the rename box. A title is a row
// label, and a paragraph pasted into it would widen every surface it is
// shown on.
const maxUserTitle = 200

// setUserTitle records the user's name for a session in the in-memory
// history. Returns true when the entry existed and changed.
func setUserTitle(sessionID, title string) bool {
	for i := range history {
		if history[i].SessionID != sessionID {
			continue
		}
		if history[i].UserTitle == title {
			return false
		}
		history[i].UserTitle = title
		return true
	}
	return false
}

// hostedBySession finds the live hosted session for an agent session id,
// or nil. Session ids are the agent's own and unique per adapter; two
// live sessions with one id would be the same conversation twice, which
// startHosted/resumeHosted never produce.
func hostedBySession(sessionID string) *hosted {
	if sessionID == "" {
		return nil
	}
	hostedMu.Lock()
	defer hostedMu.Unlock()
	for _, h := range hostedAll {
		if h.sessionID == sessionID {
			return h
		}
	}
	return nil
}

// renameSession applies a user title everywhere it is shown. An empty
// title clears the user's name and lets the agent's own show again.
func renameSession(key, sessionID, title string, now time.Time) (string, error) {
	if len(title) > maxUserTitle {
		title = title[:maxUserTitle]
	}
	h := lookupHosted(key)
	if h == nil {
		h = hostedBySession(sessionID)
	}
	if h != nil {
		hostedMu.Lock()
		h.userTitle = title
		sessionID = h.sessionID
		hostedMu.Unlock()
		h.republish()
	}
	if sessionID == "" {
		return "", fmt.Errorf("no session to rename")
	}
	mutateState(func(s *State) {
		if setUserTitle(sessionID, title) {
			historyDirty = true
		}
		s.Recent = publishHistory()
	})
	saveHistorySoon()
	// The transcript's own copy, so the History panel — which reads files,
	// not the capped history list — agrees. UserTitleSet is what lets an
	// empty title mean "cleared" rather than "unchanged" on read-back.
	writeSummary(sessionID, transcriptSummary{UserTitle: title, UserTitleSet: true, AtMS: now.UnixMilli()})
	log.Printf("agentd: session renamed key=%s session=%s title=%q", key, sessionID, title)
	return sessionID, nil
}

// deleteStoredSession removes a session's transcript and history entry.
// Refuses a live session: the verb is for what ran, not what is running.
func deleteStoredSession(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("no session id")
	}
	if h := hostedBySession(sessionID); h != nil {
		return fmt.Errorf("that session is still running — end it first")
	}
	path := transcriptPath(sessionID)
	if path != "" {
		// Make the writer let go of the file before it goes: a sink still
		// open on the inode would keep appending into the void.
		closeTranscriptFile(sessionID)
		waitForTranscriptWrites()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	forgetSession(sessionID)
	log.Printf("agentd: session deleted session=%s path=%s", sessionID, path)
	return nil
}

// pruneStoredSessions deletes every stored session whose last activity is
// older than maxAge. maxAge <= 0 means every session that is not running.
// Returns how many went.
func pruneStoredSessions(maxAge time.Duration, now time.Time) int {
	cutoff := now.Add(-maxAge).UnixMilli()
	n := 0
	for _, m := range listSessionMeta() {
		if maxAge > 0 && sessionRecency(m) >= cutoff {
			continue
		}
		if hostedBySession(m.SessionID) != nil {
			continue
		}
		if err := deleteStoredSession(m.SessionID); err != nil {
			log.Printf("agentd: prune session=%s: %v", m.SessionID, err)
			continue
		}
		n++
	}
	log.Printf("agentd: sessions pruned max_age=%s deleted=%d", maxAge, n)
	return n
}

// registerSessionAdminHandlers installs the rename / delete / prune verbs.
//
//	{kind:"agent_rename", key?, session_id?, title}
//	{kind:"agent_delete", session_id}       → {kind:"history_deleted", session_id, error?}
//	{kind:"agent_prune",  max_age_ms}       → {kind:"history_pruned", deleted}
//
// Delete and prune answer the asker, so the window that clicked can
// refresh its History list; the Recent menu refreshes through the roster
// push like everything else.
func registerSessionAdminHandlers(bus *sdk.Bus) {
	sdk.HandleFromVoid(bus, "agent_rename", func(conn *sdk.Conn, _ string, req renameReq, _ wire.Sender) error {
		if _, err := renameSession(req.Key, req.SessionID, req.Title, time.Now()); err != nil {
			log.Printf("agentd: rename key=%s session=%s: %v", req.Key, req.SessionID, err)
			conn.Warn("Could not rename that session", err.Error())
		}
		return nil
	})

	sdk.HandleFromVoid(bus, "agent_delete", func(conn *sdk.Conn, _ string, req deleteReq, from wire.Sender) error {
		reply := map[string]any{"kind": "history_deleted", "session_id": req.SessionID}
		if err := deleteStoredSession(req.SessionID); err != nil {
			log.Printf("agentd: delete session=%s: %v", req.SessionID, err)
			conn.Warn("Could not delete that session", err.Error())
			reply["error"] = err.Error()
		}
		if from.InstanceID == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, reply)
	})

	sdk.HandleFromVoid(bus, "agent_prune", func(conn *sdk.Conn, _ string, req pruneReq, from wire.Sender) error {
		n := pruneStoredSessions(time.Duration(req.MaxAgeMS)*time.Millisecond, time.Now())
		if from.InstanceID == "" {
			return nil
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind":    "history_pruned",
			"deleted": n,
		})
	})
}

type renameReq struct {
	Key       string `json:"key,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
}

type deleteReq struct {
	SessionID string `json:"session_id"`
}

type pruneReq struct {
	// MaxAgeMS is how old a session must be to go; 0 means every stored
	// session that is not running. A duration rather than a cutoff so a
	// browser clock on another machine cannot be the one deciding.
	MaxAgeMS int64 `json:"max_age_ms"`
}
