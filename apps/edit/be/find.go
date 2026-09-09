package edit

// Recursive file listing for the quick-open palette (Ctrl+P).
//
//	FE → BE  : { kind: "find", id, path, limit? }
//	           { kind: "find_cancel", id }
//	BE → FE  : { kind: "find_ok", id, path, files: [rel…], truncated, cancelled }
//	           { kind: "find_err", id, code, msg }
//
// The walk runs off the read goroutine so a find_cancel (or any other
// message) is not stuck behind it, and it is bounded three ways: an
// entry cap, a wall-clock budget, and the cancel. Version-control and
// dependency directories are skipped — they are where a tree's file
// count lives, and never what someone is trying to open.

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/pkg/sdk"
)

const (
	findDefaultLimit = 5_000
	findMaxLimit     = 20_000
	findBudget       = 5 * time.Second
)

// findSkipDirs are never descended into. Hidden directories (a leading
// dot) are skipped too, in the walk itself.
var findSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "__pycache__": true,
	"target": true, "dist": true, "build": true, "out": true,
}

type findReq struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

type findCancelReq struct {
	ID string `json:"id"`
}

var (
	findMu     sync.Mutex
	findCancel = map[string]context.CancelFunc{}
)

func registerFindHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "find", func(_ *sdk.Conn, id string, req findReq) error {
		if req.Path == "" {
			return sdk.Errf(sdk.ErrBadRequest, "missing path")
		}
		abs, err := editFS.Confine(req.Path)
		if err != nil {
			return sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		limit := req.Limit
		if limit <= 0 {
			limit = findDefaultLimit
		}
		if limit > findMaxLimit {
			limit = findMaxLimit
		}
		ctx, cancel := context.WithTimeout(context.Background(), findBudget)
		if id != "" {
			findMu.Lock()
			if prev := findCancel[id]; prev != nil {
				prev()
			}
			findCancel[id] = cancel
			findMu.Unlock()
		}
		go func() {
			defer func() {
				cancel()
				if id != "" {
					findMu.Lock()
					if findCancel[id] != nil {
						delete(findCancel, id)
					}
					findMu.Unlock()
				}
			}()
			files, truncated := walkFiles(ctx, abs, limit)
			cancelled := ctx.Err() == context.Canceled
			out := map[string]any{
				"path":      abs,
				"files":     files,
				"truncated": truncated,
				"cancelled": cancelled,
			}
			if id != "" {
				out["id"] = id
			}
			log.Printf("wash-edit: find root=%q files=%d truncated=%v cancelled=%v", abs, len(files), truncated, cancelled)
			_ = bus.Emit("find_ok", out)
		}()
		return nil
	})

	sdk.HandleVoid(b, "find_cancel", func(_ *sdk.Conn, _ string, req findCancelReq) error {
		findMu.Lock()
		cancel := findCancel[req.ID]
		delete(findCancel, req.ID)
		findMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	})
}

// walkFiles lists regular files under root as slash-separated paths
// relative to it, breadth-ish: each directory's own files are emitted
// before its subdirectories are descended, so shallow paths lead. Stops
// at limit (truncated=true), on the context, or on an unreadable
// directory (skipped, not fatal).
func walkFiles(ctx context.Context, root string, limit int) (files []string, truncated bool) {
	files = []string{}
	var walk func(dir, rel string) bool
	walk = func(dir, rel string) bool {
		if ctx.Err() != nil {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		var subdirs []fs.DirEntry
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if e.IsDir() {
				if !findSkipDirs[name] {
					subdirs = append(subdirs, e)
				}
				continue
			}
			// Symlinks are listed as files only when they resolve to
			// one; a link to a directory is not followed (cycles).
			if e.Type()&fs.ModeSymlink != 0 {
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
			} else if !e.Type().IsRegular() {
				continue
			}
			if len(files) >= limit {
				truncated = true
				return false
			}
			if rel == "" {
				files = append(files, name)
			} else {
				files = append(files, rel+"/"+name)
			}
		}
		for _, d := range subdirs {
			sub := d.Name()
			if rel != "" {
				sub = rel + "/" + sub
			}
			if !walk(filepath.Join(dir, d.Name()), sub) {
				return false
			}
		}
		return true
	}
	walk(root, "")
	return files, truncated
}
