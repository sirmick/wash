// Upload ingestion: OS files (from a file picker or an external
// drag-drop) streamed in from the FE and written into the confined
// session filesystem. The bytes land HERE (fm owns the confined fs);
// wash-bulk is only the progress surface — fm mirrors each upload as
// an external "upload" job into wash-bulk's StateService via job_report
// so it renders in the sidebar BulkWidget alongside move/copy/delete.
//
// Transport: the file BYTES ride a raw wash channel (opaque frames,
// credit-flow backpressure) — NOT the JSON bus, so there's no base64
// inflation and no per-chunk request/reply round-trip. Only metadata
// crosses the JSON bus. Shape:
//
//	FE → fm   upload_check  {dest, rels}        → {conflicts}   (JSON)
//	FE → fm   upload_begin  {dest, total_bytes, → {upload_id}   (JSON)
//	                         policy, labels}
//	fm → FE   upload_channel {upload_id, channel_id}            (JSON push)
//	FE → fm   …file bytes…                                      (raw channel)
//	fm → FE   upload_done   {upload_id, status}                 (JSON push)
//	bulk → fm upload_cancel {upload_id}         → void          (JSON, relayed)
//
// The raw stream is a sequence of self-delimiting records the FE
// builds (see web/fs-client encodeRecordHeader):
//
//	[u32 relPathLen LE][relPath utf8][u64 size LE][size bytes] …
//	[u32 0]   ← end marker; the reader finalizes on it
//
// The reader goroutine drains the channel with io.ReadFull/CopyN into
// the confined UploadWriter — one open writer at a time, so fd count
// stays at one regardless of how many files a directory upload carries.
//
// Why a separate goroutine + async channel handshake: OpenChannel
// blocks on the router's channel.opened reply, which is delivered on
// the SDK read goroutine — opening from a bus handler (which runs on
// that goroutine) would deadlock. We hand off, exactly like wash-term's
// openTab.
package fm

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// bulkAppID is wash-bulk's reserved singleton app id (mirrors
// apps/bulk.AppID; duplicated as a literal to avoid an import cycle /
// heavy dependency for one string).
const bulkAppID = "com.wash.bulk"

const (
	// Per-file cap. The writer streams to disk so memory isn't the
	// constraint; this guards against a runaway client.
	maxUploadFileBytes = 2 << 30 // 2 GiB
	// Report intra-file progress to wash-bulk at most this often (in
	// bytes) to avoid a state-publish storm on large files.
	uploadReportStep = 1 << 20 // 1 MiB
	// Copy buffer for draining the channel into the writer.
	uploadCopyBuf = 256 * 1024
	// Sanity cap on a record's relpath length.
	maxUploadRelLen = 4096
)

// errUploadCancelled is the sentinel the copy loop returns when a
// cancel landed mid-file — distinguished from a real I/O error so the
// reader reports "cancelled", not "failed".
var errUploadCancelled = errors.New("upload cancelled")

// fmInstance is this fm window's instance id, captured in onReady. The
// upload job_report carries it so wash-bulk can relay a sidebar cancel
// back to the right fm window.
var fmInstance string

// uploadSession is one in-flight upload, 1:1 with a wash-bulk job.
// done is atomic (read by reportJob from the read goroutine on cancel,
// written by the reader goroutine); the rest of the mutable state is
// under mu.
type uploadSession struct {
	id      string // == the bulk job_id
	dest    string // confined destination dir
	replace bool   // conflict policy: replace (true) vs skip (false)
	labels  []string
	total   int64        // total bytes the FE will send (immutable after begin)
	done    atomic.Int64 // bytes committed so far

	mu        sync.Mutex
	ch        *sdk.RawChannel
	cancelled bool
	failed    string
}

func (s *uploadSession) isCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

func (s *uploadSession) failedMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *uploadSession) fail(msg string) {
	s.mu.Lock()
	if s.failed == "" {
		s.failed = msg
	}
	s.mu.Unlock()
}

// setChannel records the open channel, unless a cancel already landed
// during the open (returns false → caller tears the channel down).
func (s *uploadSession) setChannel(ch *sdk.RawChannel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelled {
		return false
	}
	s.ch = ch
	return true
}

// cancel marks the session cancelled and returns the channel to close
// (closing unblocks the reader's pending Read).
func (s *uploadSession) cancel() *sdk.RawChannel {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = true
	return s.ch
}

var (
	uploadsMu sync.Mutex
	uploads   = map[string]*uploadSession{}
	uploadSeq atomic.Uint64
)

// ----- request types -----

type uploadCheckReq struct {
	Dest string   `json:"dest"`
	Rels []string `json:"rels"`
}

type uploadCheckResp struct {
	Conflicts []string `json:"conflicts"`
}

type uploadBeginReq struct {
	Dest       string   `json:"dest"`
	TotalBytes int64    `json:"total_bytes"`
	Policy     string   `json:"policy"` // "replace" | "skip"
	Labels     []string `json:"labels"`
}

type uploadBeginResp struct {
	UploadID string `json:"upload_id"`
}

type uploadIDReq struct {
	UploadID string `json:"upload_id"`
}

// registerUploadHandlers wires the upload protocol onto fm's bus. Split
// out of registerHandlers to keep app.go's handler block readable.
func registerUploadHandlers(b *sdk.Bus) {
	sdk.Handle(b, "upload_check", func(_ *sdk.Conn, _ string, req uploadCheckReq) (uploadCheckResp, error) {
		return uploadCheck(req)
	})
	sdk.Handle(b, "upload_begin", func(c *sdk.Conn, _ string, req uploadBeginReq) (uploadBeginResp, error) {
		return uploadBegin(c, req)
	})
	// Relayed from wash-bulk when the user cancels the row in the
	// sidebar. Tears down the channel (unblocking the reader) and nudges
	// the FE to stop streaming.
	sdk.HandleVoid(b, "upload_cancel", func(_ *sdk.Conn, _ string, req uploadIDReq) error {
		abortUpload(req.UploadID)
		return nil
	})
}

// uploadCheck reports which of rels already exist under dest, so the FE
// can prompt once (replace-all / skip / cancel) before streaming.
func uploadCheck(req uploadCheckReq) (uploadCheckResp, error) {
	dirAbs, err := fmFS.Confine(req.Dest)
	if err != nil {
		return uploadCheckResp{}, fsErr(err, req.Dest)
	}
	conflicts := []string{}
	for _, rel := range req.Rels {
		clean, err := wfs.SanitizeRel(rel)
		if err != nil {
			return uploadCheckResp{}, sdk.Errf(sdk.ErrBadRequest, "bad rel path")
		}
		abs, err := fmFS.Confine(filepath.Join(dirAbs, clean))
		if err != nil {
			return uploadCheckResp{}, fsErr(err, rel)
		}
		if _, err := os.Lstat(abs); err == nil {
			conflicts = append(conflicts, rel)
		}
	}
	return uploadCheckResp{Conflicts: conflicts}, nil
}

// uploadBegin creates a session, mirrors a running job into bulk, and
// hands off to the reader goroutine (which opens the raw channel).
func uploadBegin(c *sdk.Conn, req uploadBeginReq) (uploadBeginResp, error) {
	dirAbs, err := fmFS.Confine(req.Dest)
	if err != nil {
		return uploadBeginResp{}, fsErr(err, req.Dest)
	}
	if fi, err := os.Stat(dirAbs); err != nil || !fi.IsDir() {
		return uploadBeginResp{}, sdk.Errf("not_dir", "destination is not a directory")
	}
	id := "up-" + fmInstance + "-" + strconv.FormatUint(uploadSeq.Add(1), 10)
	labels := req.Labels
	if len(labels) == 0 {
		labels = []string{"files"}
	}
	s := &uploadSession{
		id:      id,
		dest:    dirAbs,
		replace: req.Policy == "replace",
		labels:  labels,
		total:   req.TotalBytes,
	}
	uploadsMu.Lock()
	uploads[id] = s
	uploadsMu.Unlock()
	reportJob(c, s, "running")
	go runUploadReader(c, s)
	return uploadBeginResp{UploadID: id}, nil
}

// runUploadReader opens the raw channel, tells the FE its id, drains
// the framed byte stream into confined files, then reports the terminal
// status to both wash-bulk and the FE. Runs on its own goroutine
// (OpenChannel can't run on the SDK read goroutine).
func runUploadReader(c *sdk.Conn, s *uploadSession) {
	defer clearUpload(s.id)

	ch, err := c.OpenChannel(context.Background(), c.WindowID())
	if err != nil {
		s.fail("channel open: " + err.Error())
		finishUpload(c, s)
		return
	}
	if !s.setChannel(ch) { // cancel landed during open
		_ = ch.Close()
		finishUpload(c, s)
		return
	}
	defer ch.Close()
	_ = bus.Emit("upload_channel", map[string]any{"upload_id": s.id, "channel_id": uint64(ch.ID())})

	br := bufio.NewReaderSize(ch, 64*1024)
	var lastReported int64
	for {
		if s.isCancelled() {
			break
		}
		var nameLen uint32
		if err := binary.Read(br, binary.LittleEndian, &nameLen); err != nil {
			// Channel closed before the end marker. A user-cancel closes
			// it deliberately; anything else is a truncated stream.
			if !s.isCancelled() && s.failedMsg() == "" {
				s.fail("upload stream ended early")
			}
			break
		}
		if nameLen == 0 {
			break // end marker → all files received
		}
		if nameLen > maxUploadRelLen {
			s.fail("relpath too long")
			break
		}
		name := make([]byte, nameLen)
		if _, err := io.ReadFull(br, name); err != nil {
			s.fail(err.Error())
			break
		}
		var size uint64
		if err := binary.Read(br, binary.LittleEndian, &size); err != nil {
			s.fail(err.Error())
			break
		}
		if err := writeUploadFile(c, s, string(name), int64(size), br, &lastReported); err != nil {
			if !errors.Is(err, errUploadCancelled) {
				s.fail(err.Error())
			}
			break
		}
	}
	finishUpload(c, s)
}

// writeUploadFile drains exactly size bytes from r into a confined
// writer for rel, reporting throttled progress. On any error the temp
// file is discarded; a skip-policy collision (rare race) is a no-op.
func writeUploadFile(c *sdk.Conn, s *uploadSession, rel string, size int64, r io.Reader, lastReported *int64) error {
	w, err := fmFS.NewUpload(s.dest, rel, maxUploadFileBytes)
	if err != nil {
		return err
	}
	buf := make([]byte, uploadCopyBuf)
	remaining := size
	for remaining > 0 {
		if s.isCancelled() {
			_ = w.Abort()
			return errUploadCancelled
		}
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		rd, rerr := io.ReadFull(r, buf[:n])
		if rd > 0 {
			if _, werr := w.Write(buf[:rd]); werr != nil {
				_ = w.Abort()
				return werr
			}
			done := s.done.Add(int64(rd))
			remaining -= int64(rd)
			if done-*lastReported >= uploadReportStep {
				reportJob(c, s, "running")
				*lastReported = done
			}
		}
		if rerr != nil {
			_ = w.Abort()
			return rerr
		}
	}
	if _, err := w.Commit(s.replace); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // skip-policy collision → leave the existing file
		}
		return err
	}
	return nil
}

// finishUpload computes the terminal status, reports it to wash-bulk,
// and pushes upload_done to the FE (which is awaiting it to refresh).
func finishUpload(c *sdk.Conn, s *uploadSession) {
	status := "done"
	switch {
	case s.isCancelled():
		status = "cancelled"
	case s.failedMsg() != "":
		status = "failed"
	default:
		s.done.Store(s.total)
	}
	reportJob(c, s, status)
	// User-facing toast. Cancelled is omitted — the user just initiated
	// it, so a toast would be noise.
	switch status {
	case "done":
		c.Info("Upload complete", s.dest)
	case "failed":
		c.Fail("Upload failed", errors.New(s.failedMsg()))
	}
	_ = bus.Emit("upload_done", map[string]any{"upload_id": s.id, "status": status})
}

// abortUpload marks a session cancelled and closes its channel so the
// reader's pending Read unblocks. The reader then reports the cancelled
// status. notifyFE is implicit — we always nudge the FE to stop.
func abortUpload(id string) {
	uploadsMu.Lock()
	s := uploads[id]
	uploadsMu.Unlock()
	if s == nil {
		return
	}
	if ch := s.cancel(); ch != nil {
		_ = ch.Close()
	}
	_ = bus.Emit("upload_cancelled", map[string]any{"upload_id": id})
}

// reportJob upserts the session's state into wash-bulk as an external
// "upload" job. Fire-and-forget cross-app send; the StateService
// fan-out is what the sidebar observes.
func reportJob(c *sdk.Conn, s *uploadSession, status string) {
	if err := c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
		"kind":     "job_report",
		"job_id":   s.id,
		"instance": fmInstance,
		"op":       "upload",
		"paths":    s.labels,
		"dest":     s.dest,
		"status":   status,
		"done":     s.done.Load(),
		"total":    s.total,
		"error":    s.failedMsg(),
	}); err != nil {
		log.Printf("wash-fm: job_report: %v", err)
	}
}

func clearUpload(id string) {
	uploadsMu.Lock()
	delete(uploads, id)
	uploadsMu.Unlock()
}
