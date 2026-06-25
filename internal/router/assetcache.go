// Router-side asset cache + compression (docs/QOS.md steps c+d). The
// asset.read path used to io.ReadAll the file on every connection and
// stream it uncompressed on the interactive class — so a 3.3 MB wallpaper
// was re-read and re-sent raw on every connect/reload. This caches each
// asset's bytes (and a gzip copy when it helps) keyed by path + stat
// signature, so the read + compress happen once per process and the wire
// carries far fewer bytes. The cached buffers are immutable, which also
// makes the async-drain slicing safe (no per-request ReadAll needed).

package router

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"mime"
	"path"
	"time"
)

// errAssetIsDir is returned by loadAsset when the path resolves to a
// directory — mapped to a bad-request reply by the caller.
var errAssetIsDir = errors.New("asset is a directory")

// assetEntry is a cached asset: its identity bytes, an optional gzip copy
// (nil when compression doesn't meaningfully help — already-compressed
// media, or too small to bother), and the source stat signature used to
// detect a changed disk-overlay file.
type assetEntry struct {
	mime    string
	raw     []byte // identity bytes (immutable once cached)
	gz      []byte // gzip bytes, or nil if not worth sending compressed
	size    int64
	modTime time.Time
}

// gzipMinSize skips compression for tiny files where the ~18-byte gzip
// header + framing overhead and the FE inflate cost aren't worth it.
const gzipMinSize = 512

// gzipWinNumer/Denom: only keep the gzip copy if it's at least 8% smaller
// than the identity bytes. Already-compressed assets (woff2, png, webp)
// barely shrink, so this auto-selects identity for them without a
// content-type allowlist.
const gzipWinNumer, gzipWinDenom = 92, 100

// gzipIfSmaller returns a gzip encoding of raw if it's a worthwhile win,
// else nil (meaning: send identity).
func gzipIfSmaller(raw []byte) []byte {
	if len(raw) < gzipMinSize {
		return nil
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := w.Write(raw); err != nil {
		return nil
	}
	if err := w.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(raw)*gzipWinNumer/gzipWinDenom {
		return nil
	}
	return buf.Bytes()
}

// loadAsset returns the cached entry for an already-cleaned asset path,
// building (read + gzip) it on a miss or when the disk-overlay file's
// stat signature changed. The expensive build runs without the cache lock
// held; two concurrent first-requests may both build (harmless,
// last-writer-wins) but never corrupt. The returned entry's buffers are
// immutable and safe to slice for async streaming.
func (r *Router) loadAsset(clean string) (*assetEntry, error) {
	f, err := r.assets.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errAssetIsDir
	}
	size, mod := st.Size(), st.ModTime()

	r.assetCacheMu.Lock()
	if e := r.assetCache[clean]; e != nil && e.size == size && e.modTime.Equal(mod) {
		r.assetCacheMu.Unlock()
		return e, nil
	}
	r.assetCacheMu.Unlock()

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	e := &assetEntry{
		mime:    mime.TypeByExtension(path.Ext(clean)),
		raw:     raw,
		gz:      gzipIfSmaller(raw),
		size:    size,
		modTime: mod,
	}
	r.assetCacheMu.Lock()
	if r.assetCache == nil {
		r.assetCache = make(map[string]*assetEntry)
	}
	r.assetCache[clean] = e
	r.assetCacheMu.Unlock()
	return e, nil
}

// isNotExist reports whether err is a filesystem "no such file" — used to
// map loadAsset failures onto the not-found reply code.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
