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

	// Decoder registrations. jpeg is imported non-blank for Encode; png
	// and gif are blank imports purely to register their decoders with
	// image.Decode.
	_ "image/gif"
	_ "image/png"
)

// DefaultDim is the thumbnail's max edge when the caller passes maxDim<=0.
const DefaultDim = 160

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
		return out, nil // cache hit — note we never re-read the source
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
	return out, nil
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
