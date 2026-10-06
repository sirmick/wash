package agentd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/wire"
)

// qaDirState is what Wash last wrote into one workspace's QA directory: per
// thread, the state it wrote and the file it left, so an unchanged thread is
// not rewritten and a file replaced behind Wash's back is.
type qaDirState struct {
	Threads map[string]qaFileState
	Status  agentproto.QADocumentStatus
}
type qaFileState struct {
	Key  string
	Info os.FileInfo
}

// qaFileKey changes whenever a thread's file would: every event and
// transition bumps its revision, and a withdrawn decision changes its state.
func qaFileKey(q swarm.QAThread) string { return fmt.Sprintf("%d/%s", q.Revision, q.State) }

// writeQAThreadFile replaces dir/<id>.md atomically. A file there that does
// not carry this thread's marker is someone's document and is left alone, as
// is anything that is not a regular file.
func writeQAThreadFile(dir, id string, content []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := id + ".md"
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		head, _ := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
		f.Close()
		if info.Size() > 0 && head != swarm.QAFileMarker(id)+"\n" {
			return fmt.Errorf("%s is not a Wash QA thread file; move it away", name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := ".wash-qa-" + swarm.ID() + ".tmp"
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// syncQADocuments writes each changed thread to its file. The store is
// authoritative: a failed write is reported and retried on the workspace
// loop, and a file removed or replaced behind Wash's back is written again.
// A thread read back as a header only is never written: its file is its
// record. Writers are serialized and take their snapshot under the lock, so
// a delayed caller cannot put an older thread back.
func (ws *workspaceService) syncQADocuments() {
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if ws.qaFiles == nil {
		ws.qaFiles = map[string]*qaDirState{}
	}
	home, _ := os.UserHomeDir()
	for _, w := range ws.store.Shared().Workspaces {
		if w.QADir == "" {
			delete(ws.qaFiles, w.ID)
			continue
		}
		st := ws.qaFiles[w.ID]
		if st == nil || st.Status.Path != w.QADir {
			st = &qaDirState{Threads: map[string]qaFileState{}, Status: agentproto.QADocumentStatus{Path: w.QADir, State: "saved"}}
			ws.qaFiles[w.ID] = st
		}
		var failed error
		wrote := false
		for _, q := range w.QA {
			if q.Archived {
				continue
			}
			path := filepath.Join(w.QADir, q.ID+".md")
			key := qaFileKey(q)
			prior, seen := st.Threads[q.ID]
			info, err := os.Lstat(path)
			if seen && prior.Key == key && err == nil && prior.Info != nil && os.SameFile(info, prior.Info) && info.ModTime().Equal(prior.Info.ModTime()) && info.Size() == prior.Info.Size() {
				continue
			}
			content, err := swarm.EncodeQAFile(swarm.QAFileFor(&w, q.ID), w.Root, home)
			if err == nil {
				err = writeQAThreadFile(w.QADir, q.ID, content)
			}
			if err != nil {
				if failed == nil {
					failed = fmt.Errorf("thread %s: %w", q.ID, err)
				}
				delete(st.Threads, q.ID)
				continue
			}
			info, _ = os.Lstat(path)
			st.Threads[q.ID] = qaFileState{Key: key, Info: info}
			wrote = true
		}
		prior := st.Status
		if failed != nil {
			st.Status = agentproto.QADocumentStatus{Path: w.QADir, State: "error", Error: failed.Error(), Updated: prior.Updated}
			if ws.conn != nil && (prior.State != "error" || prior.Error != st.Status.Error) {
				desktop(ws.conn, agentproto.Notify{Title: w.Name + " · QA save failed", Body: st.Status.Error + ". Records retained; Wash will retry.", Level: wire.NotifyLevelError})
			}
		} else if wrote || prior.State != "saved" {
			st.Status = agentproto.QADocumentStatus{Path: w.QADir, State: "saved", Updated: time.Now().UnixMilli()}
		}
	}
}

func (ws *workspaceService) qaDocumentStatus(w *swarm.Workspace) agentproto.QADocumentStatus {
	if w == nil || w.QADir == "" {
		return agentproto.QADocumentStatus{State: "unconfigured"}
	}
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if st := ws.qaFiles[w.ID]; st != nil && st.Status.Path == w.QADir {
		return st.Status
	}
	return agentproto.QADocumentStatus{Path: w.QADir, State: "pending"}
}

// readQAFile reads one thread file, bounded.
func readQAFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxQAFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxQAFileBytes {
		return nil, fmt.Errorf("%s exceeds %d MiB", filepath.Base(path), maxQAFileBytes>>20)
	}
	return bytes.Clone(b), nil
}
