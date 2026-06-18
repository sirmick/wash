// Download egress: confined files streamed OUT to the browser, which
// saves them via a synthetic <a download>. The mirror image of upload —
// same raw-channel transport, opposite direction. fm decides the
// bundling: a lone regular file streams as-is; anything else (a
// directory, or a multi-selection) is zipped on the fly so the browser
// receives a single archive.
//
//	FE → fm   download_begin   {paths:[…]}        → {download_id, filename}  (JSON)
//	fm → FE   download_channel {download_id, channel_id}                     (JSON push)
//	fm → FE   …file/zip bytes…                                              (raw channel, fm writes)
//	fm → FE   download_done    {download_id, status, error}                  (JSON push)
//
// Unlike upload there's no per-file framing: fm sends exactly one opaque
// payload (the file, or the zip), so the FE just concatenates every
// chunk into a Blob and saves it under `filename`. download_done is the
// finalize signal — it rides the JSON bus and is emitted only after the
// last raw frame is flushed, so the FE has all bytes in hand by then.
//
// Why a goroutine: OpenChannel blocks on the router's channel.opened
// reply delivered on the SDK read goroutine, so it can't run from a bus
// handler — identical constraint to upload's reader.
package fm

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/sirmick/wash/internal/sdk"
)

var (
	downloads   = map[string]*downloadSession{}
	downloadsMu sync.Mutex
	downloadSeq atomic.Uint64
)

type downloadBeginReq struct {
	Paths []string `json:"paths"`
}

type downloadBeginResp struct {
	DownloadID string `json:"download_id"`
	Filename   string `json:"filename"`
}

// downloadSession is one in-flight egress, 1:1 with a raw channel.
type downloadSession struct {
	id       string
	paths    []string // confined absolute source paths
	zip      bool     // stream a zip archive vs a lone file
	filename string   // what the browser saves it as

	mu     sync.Mutex
	failed string
}

func (s *downloadSession) fail(msg string) {
	s.mu.Lock()
	if s.failed == "" {
		s.failed = msg
	}
	s.mu.Unlock()
}

func (s *downloadSession) failedMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func clearDownload(id string) {
	downloadsMu.Lock()
	delete(downloads, id)
	downloadsMu.Unlock()
}

func registerDownloadHandlers(b *sdk.Bus) {
	sdk.Handle(b, "download_begin", func(c *sdk.Conn, _ string, req downloadBeginReq) (downloadBeginResp, error) {
		return downloadBegin(c, req)
	})
}

// downloadBegin confines+stats every requested path, decides single-file
// vs zip and the save name, then hands off to the writer goroutine.
func downloadBegin(c *sdk.Conn, req downloadBeginReq) (downloadBeginResp, error) {
	if len(req.Paths) == 0 {
		return downloadBeginResp{}, sdk.Errf(sdk.ErrBadRequest, "no paths")
	}
	abs := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		a, err := fmFS.Confine(p)
		if err != nil {
			return downloadBeginResp{}, fsErr(err, p)
		}
		if _, err := os.Lstat(a); err != nil {
			return downloadBeginResp{}, fsErr(err, p)
		}
		abs = append(abs, a)
	}

	useZip := len(abs) > 1
	var name string
	switch {
	case len(abs) > 1:
		name = "wash-download.zip"
	default:
		fi, err := os.Lstat(abs[0])
		if err != nil {
			return downloadBeginResp{}, fsErr(err, req.Paths[0])
		}
		base := filepath.Base(abs[0])
		if fi.IsDir() {
			useZip = true
			name = base + ".zip"
		} else {
			name = base
		}
	}

	id := "dl-" + fmInstance + "-" + strconv.FormatUint(downloadSeq.Add(1), 10)
	s := &downloadSession{id: id, paths: abs, zip: useZip, filename: name}
	downloadsMu.Lock()
	downloads[id] = s
	downloadsMu.Unlock()
	go runDownloadWriter(c, s)
	return downloadBeginResp{DownloadID: id, Filename: name}, nil
}

// runDownloadWriter opens the raw channel, announces it, streams the
// payload, then reports the terminal status (which doubles as the FE's
// finalize trigger).
func runDownloadWriter(c *sdk.Conn, s *downloadSession) {
	defer clearDownload(s.id)

	ch, err := c.OpenChannel(context.Background(), c.WindowID())
	if err != nil {
		s.fail("channel open: " + err.Error())
		finishDownload(c, s)
		return
	}
	defer ch.Close()
	_ = bus.Emit("download_channel", map[string]any{"download_id": s.id, "channel_id": uint64(ch.ID())})

	bw := bufio.NewWriterSize(ch, 64*1024)
	var werr error
	if s.zip {
		werr = streamZip(bw, s.paths)
	} else {
		werr = streamFile(bw, s.paths[0])
	}
	if werr == nil {
		werr = bw.Flush()
	}
	if werr != nil {
		s.fail(werr.Error())
	}
	finishDownload(c, s)
}

// finishDownload pushes the terminal status to the FE and fires a toast
// via the notify helpers — Info on success, Fail on error.
func finishDownload(c *sdk.Conn, s *downloadSession) {
	status := "done"
	if s.failedMsg() != "" {
		status = "failed"
	}
	log.Printf("wash-fm download id=%s status=%s file=%q zip=%v err=%q", s.id, status, s.filename, s.zip, s.failedMsg())
	_ = bus.Emit("download_done", map[string]any{"download_id": s.id, "status": status, "error": s.failedMsg()})
	switch status {
	case "done":
		c.Info("Download ready", s.filename)
	case "failed":
		c.Fail("Download failed: "+s.filename, errors.New(s.failedMsg()))
	}
}

// streamFile copies a single regular file's bytes to w.
func streamFile(w io.Writer, abs string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// streamZip writes a zip of every path to w. Directories are walked
// recursively; each top-level selection keeps its own base name as the
// archive prefix (so dl of dir "proj" yields proj/… inside the zip).
// Non-regular entries (symlinks, devices) and empty dirs are skipped —
// v1 ships file contents only.
func streamZip(w io.Writer, paths []string) error {
	zw := zip.NewWriter(w)
	for _, abs := range paths {
		fi, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			base := filepath.Dir(abs) // rel-anchor so the dir name is included
			err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.Type().IsRegular() {
					return nil
				}
				rel, err := filepath.Rel(base, p)
				if err != nil {
					return err
				}
				return addZipFile(zw, p, rel)
			})
			if err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			if err := addZipFile(zw, abs, filepath.Base(abs)); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

func addZipFile(zw *zip.Writer, abs, name string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	dst, err := zw.Create(filepath.ToSlash(name))
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}
