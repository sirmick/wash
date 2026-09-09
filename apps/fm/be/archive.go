// Archives from the file manager: "Extract here" and "Compress".
//
//	FE → fm   extract  {path}                  → extract_ok {dest}
//	FE → fm   compress {paths, format}         → compress_ok {name, dest}
//
// Both are enqueued on wash-bulk rather than run here: unpacking a big
// tarball or zipping a big folder is exactly the long walk the queue's
// progress bar and cancel button exist for, and fm's BE has no business
// blocking on it. The work itself (including the zip-slip guard) lives
// in internal/bulkops/archive.go.
//
// fm's part is the naming and the confinement: which folder an archive
// unpacks into, what the new archive is called, and that both stay
// inside the session's fs root.
package fm

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

type extractReq struct {
	Path string `json:"path"`
	// Dest is optional: the folder to unpack into. Empty means "here" —
	// a folder beside the archive, named after it.
	Dest string `json:"dest"`
}

type extractResp struct {
	Dest string `json:"dest"`
}

type compressReq struct {
	Paths []string `json:"paths"`
	// Format is the container: "zip" (default) or "tar.gz".
	Format string `json:"format"`
}

type compressResp struct {
	Name string `json:"name"`
	Dest string `json:"dest"`
}

func registerArchiveHandlers(b *sdk.Bus) {
	sdk.Handle(b, "extract", func(c *sdk.Conn, _ string, req extractReq) (extractResp, error) {
		abs, err := fmFS.Confine(req.Path)
		if err != nil {
			log.Printf("fm: extract path=%q: %v", req.Path, err)
			return extractResp{}, fsErr(err, req.Path)
		}
		if !bulkops.IsExtractable(abs) {
			// .tar.xz lands here: recognisably an archive, no stdlib codec.
			return extractResp{}, sdk.Errf(sdk.ErrBadRequest, "%s: unsupported archive format", filepath.Base(abs))
		}
		dest := req.Dest
		if dest == "" {
			// "Extract here" means a folder of its own beside the archive,
			// not the archive's contents strewn across the current folder.
			dest = freeDirName(filepath.Dir(abs), archiveStem(filepath.Base(abs)))
		} else if dest, err = fmFS.Confine(dest); err != nil {
			return extractResp{}, fsErr(err, req.Dest)
		}
		log.Printf("fm: extract path=%q dest=%q", abs, dest)
		if err := c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
			"kind": "enqueue", "op": "extract", "paths": []string{abs}, "dest": dest,
		}); err != nil {
			return extractResp{}, err
		}
		return extractResp{Dest: dest}, nil
	})

	sdk.Handle(b, "compress", func(c *sdk.Conn, _ string, req compressReq) (compressResp, error) {
		if len(req.Paths) == 0 {
			return compressResp{}, sdk.Errf(sdk.ErrBadRequest, "nothing to compress")
		}
		ext := ".zip"
		if req.Format == "tar.gz" {
			ext = ".tar.gz"
		} else if req.Format != "" && req.Format != "zip" {
			return compressResp{}, sdk.Errf(sdk.ErrBadRequest, "unsupported archive format %q", req.Format)
		}
		abs := make([]string, 0, len(req.Paths))
		dest := ""
		for _, p := range req.Paths {
			a, err := fmFS.Confine(p)
			if err != nil {
				log.Printf("fm: compress path=%q: %v", p, err)
				return compressResp{}, fsErr(err, p)
			}
			dir := filepath.Dir(a)
			if dest == "" {
				dest = dir
			} else if dir != dest {
				return compressResp{}, sdk.Errf(sdk.ErrBadRequest, "compress: every item must be in the same folder")
			}
			abs = append(abs, a)
		}
		// One item → named after it; several → named after the folder
		// holding them, which is the only name they have in common.
		stem := filepath.Base(dest)
		if len(abs) == 1 {
			stem = archiveStem(filepath.Base(abs[0]))
		}
		name := freeFileName(dest, stem, ext)
		log.Printf("fm: compress n=%d dest=%q name=%q", len(abs), dest, name)
		if err := c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
			"kind": "enqueue", "op": "compress", "paths": abs, "dest": dest, "names": []string{name},
		}); err != nil {
			return compressResp{}, err
		}
		return compressResp{Name: name, Dest: dest}, nil
	})
}

// archiveStem strips a recognised archive suffix from a name, so
// "proj.tar.gz" extracts into "proj" rather than "proj.tar". A name with
// no archive suffix loses only its final extension.
func archiveStem(name string) string {
	for _, suffix := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tar", ".zip"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return name[:len(name)-len(suffix)]
		}
	}
	if ext := filepath.Ext(name); ext != "" && ext != name {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

// freeDirName / freeFileName reuse the Duplicate naming rule so a second
// "Extract here" makes "proj (copy)" rather than merging into the first
// one's folder or failing.
func freeDirName(dir, stem string) string {
	if stem == "" {
		stem = "archive"
	}
	return filepath.Join(dir, freeName(dir, stem, true))
}

func freeFileName(dir, stem, ext string) string {
	if stem == "" {
		stem = "archive"
	}
	return freeName(dir, stem+ext, false)
}

func freeName(dir, name string, isDir bool) string {
	taken := func(n string) bool {
		_, err := os.Lstat(filepath.Join(dir, n))
		return err == nil
	}
	if !taken(name) {
		return name
	}
	return bulkops.UniqueCopyName(name, isDir, taken)
}
