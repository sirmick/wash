package agentd

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/swarm"
)

type qaDocumentStatus struct {
	Path     string `json:"path"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	Updated  int64  `json:"updated_at,omitempty"`
	Revision int64  `json:"saved_revision,omitempty"`
}
type qaFileState struct {
	Digest [32]byte
	Info   os.FileInfo
	Status qaDocumentStatus
}

func qaFileMarker(id string) string { return "<!-- wash-workspace-qa: " + id + " -->" }

// Existing project documents are never claimed as generated output. Parent
// directories must already exist and output may not follow a replaced symlink.
func validateQAFile(path, owner string, originalHash ...string) error {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(path) {
		return errors.New("QA output directory changed; reconfigure the path")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	return validateQATarget(root, filepath.Base(path), owner, originalHash...)
}
func validateQATarget(root *os.Root, name, owner string, originalHash ...string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("QA output must be a regular file, not a symlink")
	}
	if info.Size() == 0 {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	header, err := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if owner == "" || strings.TrimSpace(header) != qaFileMarker(owner) {
		if len(originalHash) > 0 && originalHash[0] != "" {
			if _, err := f.Seek(0, 0); err != nil {
				return err
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, io.LimitReader(f, maxQAFileBytes+1)); err != nil {
				return err
			}
			if fmt.Sprintf("%x", hash.Sum(nil)) == originalHash[0] {
				return nil
			}
		}
		return errors.New("QA output already contains another document; choose an empty or new file")
	}
	return nil
}
func writeQAFile(w *swarm.Workspace) error {
	path := w.QADocument.Path
	if err := validateQAFile(path, qaOwner(w), w.QAOriginalHash); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	if err = validateQATarget(root, name, qaOwner(w), w.QAOriginalHash); err != nil {
		return err
	}
	temp := ".wash-qa-" + swarm.ID() + ".tmp"
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	out := bufio.NewWriter(f)
	_, err = fmt.Fprintln(out, qaFileMarker(qaOwner(w)))
	if err == nil {
		err = swarm.WriteQAMarkdown(out, w)
	}
	if err == nil {
		err = writeQACheckpoint(out, w)
	}
	if err == nil {
		err = out.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Serialize writers and take the snapshot after acquiring the lock. A delayed
// older caller therefore cannot replace newer QA with its original snapshot.
// The durable store is authoritative; failed projections are retried on the
// workspace loop and regenerated after restart. UI rendering stays bounded.
func (ws *workspaceService) syncQADocuments() {
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if ws.qaFiles == nil {
		ws.qaFiles = map[string]qaFileState{}
	}
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.QADocument == nil {
			continue
		}
		names := map[string]string{}
		for _, m := range w.Members {
			names[m.ID] = m.Name
		}
		encoded, _ := json.Marshal(struct {
			Document *swarm.Document
			Archive  qaArchive
			Names    map[string]string
		}{w.QADocument, archiveQA(&w), names})
		digest := sha256.Sum256(encoded)
		prior := ws.qaFiles[w.ID]
		info, statErr := os.Lstat(w.QADocument.Path)
		if prior.Status.State == "saved" && prior.Digest == digest && statErr == nil && info.Mode().IsRegular() && prior.Info != nil && os.SameFile(info, prior.Info) && info.ModTime() == prior.Info.ModTime() && info.Size() == prior.Info.Size() {
			continue
		}
		next := qaFileState{Digest: digest, Status: qaDocumentStatus{Path: w.QADocument.Path, State: "saved", Updated: time.Now().UnixMilli()}}
		if err := writeQAFile(&w); err != nil {
			next.Status.State = "error"
			next.Status.Error = err.Error()
			next.Status.Updated = prior.Status.Updated
			next.Status.Revision = prior.Status.Revision
			if ws.conn != nil && (prior.Status.State != "error" || prior.Status.Error != next.Status.Error) {
				ws.conn.NotifyAbout("", w.Name+" · QA save failed", next.Status.Error+". Records retained; Wash will retry.", "error")
			}
		} else {
			next.Info, _ = os.Lstat(w.QADocument.Path)
			next.Status.Revision = w.Revision
		}
		ws.qaFiles[w.ID] = next
	}
}
func (ws *workspaceService) qaDocumentStatus(w *swarm.Workspace) qaDocumentStatus {
	if w == nil || w.QADocument == nil {
		return qaDocumentStatus{State: "unconfigured"}
	}
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if file, ok := ws.qaFiles[w.ID]; ok && file.Status.Path == w.QADocument.Path {
		return file.Status
	}
	return qaDocumentStatus{Path: w.QADocument.Path, State: "pending"}
}
