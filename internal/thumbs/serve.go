package thumbs

import (
	"bufio"
	"context"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/sdk"
)

// Confiner resolves an FE-supplied path to a confined absolute path. Each
// app passes its own fs root's Confine so thumbs never widens the app's
// existing file exposure.
type Confiner func(path string) (abs string, err error)

type getFileReq struct {
	ReqID string `json:"req_id"`
	Path  string `json:"path"`
	Dim   int    `json:"dim"` // 0 = full bytes; >0 = thumbnail max edge
}

// RegisterServer wires a `get_file` handler on b. Each request streams the
// file's bytes (dim==0) or a cached thumbnail (dim>0) to the requesting FE
// over a fresh raw channel — the image-bytes transport for fm's folder grid
// and the image viewer. The exchange (cf. fm's download egress, but for
// in-place display rather than saving):
//
//	FE → app   get_file     {req_id, path, dim}
//	app → FE   file_channel {req_id, channel_id, mime}   then raw bytes…
//	app → FE   file_done    {req_id, status, error}      (emitted after the
//	                                                       last frame flushes,
//	                                                       so the FE has every
//	                                                       byte by then)
func RegisterServer(b *sdk.Bus, confine Confiner) {
	sdk.HandleVoid(b, "get_file", func(c *sdk.Conn, _ string, req getFileReq) error {
		abs, err := confine(req.Path)
		if err != nil {
			emitDone(b, req.ReqID, err.Error())
			return nil
		}
		fi, err := os.Stat(abs)
		if err != nil {
			emitDone(b, req.ReqID, err.Error())
			return nil
		}
		if fi.IsDir() {
			emitDone(b, req.ReqID, "is a directory")
			return nil
		}
		// OpenChannel blocks on the router's reply, which is delivered on
		// this same read goroutine — so the streaming must hand off to its
		// own goroutine (identical constraint to download.go).
		go serveFile(c, b, req, abs, fi)
		return nil
	})
}

func serveFile(c *sdk.Conn, b *sdk.Bus, req getFileReq, abs string, fi os.FileInfo) {
	srcPath, contentType := abs, mimeForPath(abs)
	if req.Dim > 0 {
		tp, err := Get(abs, fi.ModTime().Unix(), fi.Size(), req.Dim)
		if err != nil {
			emitDone(b, req.ReqID, err.Error())
			return
		}
		srcPath, contentType = tp, "image/jpeg"
	}
	ch, err := c.OpenChannel(context.Background(), c.WindowID())
	if err != nil {
		emitDone(b, req.ReqID, "channel open: "+err.Error())
		return
	}
	defer ch.Close()
	_ = b.Emit("file_channel", map[string]any{
		"req_id":     req.ReqID,
		"channel_id": uint64(ch.ID()),
		"mime":       contentType,
	})
	bw := bufio.NewWriterSize(ch, 64*1024)
	werr := streamPath(bw, srcPath)
	if werr == nil {
		werr = bw.Flush()
	}
	if werr != nil {
		emitDone(b, req.ReqID, werr.Error())
		return
	}
	emitDone(b, req.ReqID, "")
}

// emitDone is the single finalize signal; an empty errMsg means success.
// It's also the failure path for errors that occur before a channel ever
// opens, so the FE has one place to settle every request.
func emitDone(b *sdk.Bus, reqID, errMsg string) {
	status := "done"
	if errMsg != "" {
		status = "failed"
	}
	_ = b.Emit("file_done", map[string]any{"req_id": reqID, "status": status, "error": errMsg})
}

func streamPath(w io.Writer, abs string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func mimeForPath(p string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(p))); t != "" {
		return t
	}
	return "application/octet-stream"
}
