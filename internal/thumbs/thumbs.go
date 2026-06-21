// Package thumbs decodes an image, downscales it, and caches the result
// on disk — the image half of wash's file preview / image viewer. It is a
// plain library (not a service): each consuming app BE imports it and
// streams the bytes to its own FE over a raw channel (see docs/IMAGES.md
// for why a shared library, not a central service).
//
// Zero external dependencies: stdlib decodes JPEG/PNG/GIF and encodes
// JPEG. Other formats (e.g. WebP) return an error and the caller falls
// back to a file-type icon; adding golang.org/x/image later is the
// upgrade path. Scaling is area-averaging (a decent low-pass that looks
// clean at thumbnail size) — no x/image needed.
package thumbs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// Decoder registrations. jpeg is imported non-blank for Encode; png
	// and gif are blank imports purely to register their decoders with
	// image.Decode. This set is mirrored by THUMBABLE_EXTS in
	// web/fs-client/src/thumbable.ts — keep the two in sync if a format is
	// added or dropped (the FE uses it to decide which entries get a thumb).
	_ "image/gif"
	_ "image/png"
)

// DefaultDim is the thumbnail's max edge when the caller passes maxDim<=0.
const DefaultDim = 160

// Cache-GC tuning. The on-disk cache is bounded by a least-recently-used
// sweep (see maybeGC): when a write pushes the cache over the byte
// budget, the oldest-mtime entries are deleted down to gcTargetRatio of
// it. Without this the cache grows unbounded across every folder ever
// browsed.
const (
	// DefaultCacheBudget is the soft cap on ~/.cache/wash/thumbs.
	// Override per-process with WASH_THUMB_CACHE_MB. At a typical
	// ~5–10 KB per 160px JPEG this is tens of thousands of thumbnails.
	DefaultCacheBudget = 256 << 20 // 256 MiB
	gcTargetRatio      = 0.8
	// gcMinInterval rate-limits the sweep: at most one dir scan per
	// process per interval, regardless of write rate.
	gcMinInterval = 5 * time.Minute
)

// cacheDir is ~/.cache/wash/thumbs (honouring XDG_CACHE_HOME via
// os.UserCacheDir), matching the cache convention used elsewhere (e.g.
// apps/vscode). Falls back to ~/.cache if UserCacheDir is unset.
func cacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "wash", "thumbs")
}

// key derives the cache filename from the source identity. mtime+size key
// the content so the thumbnail invalidates implicitly when the file
// changes — no watching needed. dim is included so different requested
// sizes don't collide.
func key(absPath string, mtime, size int64, dim int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", absPath, mtime, size, dim)))
	return hex.EncodeToString(h[:]) + ".jpg"
}

// Get returns the path to a cached JPEG thumbnail of absPath whose longest
// edge is at most maxDim, generating (and caching) it on a miss. mtime/size
// are the source's stat fields (the caller already has them from the
// listing) and key the cache entry. absPath must already be confined by
// the caller — thumbs does no path validation of its own.
func Get(absPath string, mtime, size int64, maxDim int) (string, error) {
	if maxDim <= 0 {
		maxDim = DefaultDim
	}
	dir := cacheDir()
	out := filepath.Join(dir, key(absPath, mtime, size, maxDim))
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		// Cache hit — we never re-read the source. Refresh the cache
		// file's mtime as a coarse LRU signal for the sweep, but only
		// when it's already stale: this avoids a Chtimes syscall on
		// every tile of a rapidly re-rendered grid while still keeping
		// a frequently-served-but-old thumbnail from being evicted.
		if time.Since(fi.ModTime()) > time.Hour {
			now := time.Now()
			_ = os.Chtimes(out, now, now)
		}
		return out, nil
	}
	src, err := decode(absPath)
	if err != nil {
		return "", err
	}
	thumb := downscale(src, maxDim)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := atomicWriteJPEG(out, thumb); err != nil {
		return "", err
	}
	// A new entry was written — the only point the cache grows. Kick a
	// rate-limited background sweep so the cache stays under budget.
	maybeGC(dir, cacheBudget())
	return out, nil
}

// cacheBudget is the LRU sweep's byte target: WASH_THUMB_CACHE_MB if set
// to a positive integer, else DefaultCacheBudget.
func cacheBudget() int64 {
	if v := os.Getenv("WASH_THUMB_CACHE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			return int64(mb) << 20
		}
	}
	return DefaultCacheBudget
}

var (
	gcMu      sync.Mutex
	gcRunning bool
	gcLast    time.Time
)

// maybeGC starts a background cache sweep, but at most one at a time and
// no more than once per gcMinInterval per process — so a folder of
// thousands of fresh thumbnails triggers a single scan, not thousands.
// gcLast's zero value lets the first miss after startup sweep immediately,
// bounding a cache left large by a previous run. No dedicated reaper
// goroutine and nothing for consumers to wire up.
func maybeGC(dir string, budget int64) {
	gcMu.Lock()
	if gcRunning || time.Since(gcLast) < gcMinInterval {
		gcMu.Unlock()
		return
	}
	gcRunning = true
	gcLast = time.Now()
	gcMu.Unlock()
	go func() {
		defer func() {
			gcMu.Lock()
			gcRunning = false
			gcMu.Unlock()
		}()
		gcOnce(dir, budget)
	}()
}

// gcOnce deletes least-recently-used (oldest-mtime) thumbnails until the
// cache is under budget*gcTargetRatio, if it exceeds budget. Best-effort:
// any error (incl. a racing process removing the same file) is ignored —
// os.Remove of an already-gone file just fails harmlessly. Only the temp
// files and .jpg entries thumbs itself writes are considered.
func gcOnce(dir string, budget int64) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		path  string
		size  int64
		mtime int64
	}
	items := make([]entry, 0, len(ents))
	var total int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, entry{filepath.Join(dir, e.Name()), fi.Size(), fi.ModTime().UnixNano()})
		total += fi.Size()
	}
	if total <= budget {
		return
	}
	target := int64(float64(budget) * gcTargetRatio)
	sort.Slice(items, func(i, j int) bool { return items[i].mtime < items[j].mtime })
	for _, it := range items {
		if total <= target {
			break
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

// downscale returns src scaled so its longest edge is <= maxDim, by area
// averaging (each destination pixel is the mean of the source box that
// maps onto it). An already-small image is just copied to RGBA. Averaging
// happens in the 16-bit space color.RGBA() reports, then shifted to 8-bit.
func downscale(src image.Image, maxDim int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dw, dh := sw, sh
	if sw > maxDim || sh > maxDim {
		if sw >= sh {
			dw, dh = maxDim, max1(sh*maxDim/sw)
		} else {
			dw, dh = max1(sw*maxDim/sh), maxDim
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	if dw == sw && dh == sh {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		return dst
	}
	for dy := 0; dy < dh; dy++ {
		sy0 := b.Min.Y + dy*sh/dh
		sy1 := b.Min.Y + (dy+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0 := b.Min.X + dx*sw/dw
			sx1 := b.Min.X + (dx+1)*sw/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.SetRGBA(dx, dy, color.RGBA{
				R: uint8((r / n) >> 8),
				G: uint8((g / n) >> 8),
				B: uint8((bl / n) >> 8),
				A: uint8((a / n) >> 8),
			})
		}
	}
	return dst
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// atomicWriteJPEG encodes img to a temp file in the destination dir and
// renames it into place, so a concurrent reader never sees a half-written
// thumbnail and two generators racing the same key resolve to one file.
func atomicWriteJPEG(path string, img image.Image) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thumb-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once renamed
	if err := jpeg.Encode(tmp, img, &jpeg.Options{Quality: 82}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
