package thumbs

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG writes a w×h test image (a simple two-tone split so averaging
// has something non-trivial to chew on) and returns its path + stat.
func writePNG(t *testing.T, dir string, w, h int) (string, int64, int64) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.RGBA{200, 40, 40, 255})
			} else {
				img.Set(x, y, color.RGBA{40, 40, 200, 255})
			}
		}
	}
	p := filepath.Join(dir, "src.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, fi.ModTime().Unix(), fi.Size()
}

func TestGetDownscalesAndPreservesAspect(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	src, mt, sz := writePNG(t, dir, 400, 200)

	out, err := Get(src, mt, sz, 160)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open thumb: %v", err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode thumb: %v", err)
	}
	// 400×200, longest edge clamped to 160 → 160×80.
	if cfg.Width != 160 || cfg.Height != 80 {
		t.Fatalf("dims = %dx%d, want 160x80", cfg.Width, cfg.Height)
	}
}

func TestGetCacheHitDoesNotReadSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	src, mt, sz := writePNG(t, dir, 300, 300)

	out1, err := Get(src, mt, sz, 128)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// Remove the source; a cache hit must still resolve since it never
	// re-reads the original.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	out2, err := Get(src, mt, sz, 128)
	if err != nil {
		t.Fatalf("cached Get after source removed: %v", err)
	}
	if out1 != out2 {
		t.Fatalf("cache path changed: %q vs %q", out1, out2)
	}
}

func TestGetInvalidatesOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	src, mt, sz := writePNG(t, dir, 100, 100)
	if _, err := Get(src, mt, sz, 64); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// A different mtime is a different key → cache miss. With the source
	// gone, that miss must surface as an error (proving it didn't reuse
	// the prior entry).
	os.Remove(src)
	if _, err := Get(src, mt+1, sz, 64); err == nil {
		t.Fatal("expected miss/error for changed mtime, got nil")
	}
}

func TestGetRejectsUndecodable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	p := filepath.Join(dir, "notimage.bin")
	if err := os.WriteFile(p, []byte("this is not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if _, err := Get(p, fi.ModTime().Unix(), fi.Size(), 160); err == nil {
		t.Fatal("expected decode error for non-image, got nil")
	}
}
