package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// An ended workspace keeps nothing a running one needs, and everything in
// it was costing them: the state is deep-copied as JSON on every read, and
// one long run's ended workspaces made it 8.8 MB, 45 ms a copy, one core
// of agentd at all times. So an ended workspace's whole record moves to its
// own file under workspaces-archive/, and the live state keeps a stub: its
// identity and its members' identities and launch settings, which is what
// roster parentage and reopening a member's transcript read. Messages are
// read back from the archive on the rare occasion a transcript needs them.

// ArchiveDir is where Archive writes ended workspaces, beside the state.
func (s *Store) ArchiveDir() string {
	return filepath.Join(filepath.Dir(s.path), "workspaces-archive")
}

// Unarchived lists the ended workspaces still held in full, without
// copying the state.
func (s *Store) Unarchived() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, w := range s.state.Workspaces {
		if w.State == "ended" && w.Archive == "" {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

// Archive writes ended workspace id to its archive file and replaces it in
// the state with a stub. The file is written first, so a crash between the
// two leaves the full record in the state and the next call writes the same
// file again.
func (s *Store) Archive(id string) error {
	return s.change(func(st *State) error {
		for i := range st.Workspaces {
			w := &st.Workspaces[i]
			if w.ID != id {
				continue
			}
			if w.State != "ended" {
				return errors.New("only an ended workspace is archived")
			}
			if w.Archive != "" {
				return nil
			}
			path := filepath.Join(s.ArchiveDir(), w.ID+".json")
			b, err := json.Marshal(w)
			if err != nil {
				return err
			}
			if err := s.write(path, b); err != nil {
				return fmt.Errorf("archive %s: %w", w.ID, err)
			}
			*w = stub(*w, path)
			return nil
		}
		return errors.New("unknown workspace")
	})
}

// stub is what stays of an archived workspace: who it was and who was in
// it. Members keep their sessions, creators and launch settings (Parents
// and a reopened member's launch read them); their instructions, handoffs
// and every message, assignment, thread and plan node go with the archive.
// Its QA directory and plan file are released: their files are its record.
func stub(w Workspace, path string) Workspace {
	out := Workspace{ID: w.ID, Name: w.Name, Root: w.Root, Lead: w.Lead, State: w.State, Revision: w.Revision, MaxActive: w.MaxActive, MaxMembers: w.MaxMembers, Catalog: w.Catalog, Archive: path}
	for _, m := range w.Members {
		out.Members = append(out.Members, Member{ID: m.ID, Key: m.Key, Name: m.Name, Node: m.Node, Role: m.Role, Catalog: m.Catalog, Model: m.Model, LaunchSettings: m.LaunchSettings, Applied: m.Applied, Provider: m.Provider, Cwd: m.Cwd, Session: m.Session, Creator: m.Creator, Lifetime: m.Lifetime, State: m.State})
	}
	return out
}

// LoadArchived reads an archived workspace in full.
func LoadArchived(path string) (*Workspace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Workspace
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	return &w, nil
}
