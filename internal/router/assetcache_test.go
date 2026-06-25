package router

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func gunzipBytes(t *testing.T, gz []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func TestGzipIfSmaller(t *testing.T) {
	raw := []byte(strings.Repeat("wash wallpaper svg path data ", 500))
	gz := gzipIfSmaller(raw)
	if gz == nil {
		t.Fatalf("expected a gzip win for highly-compressible input")
	}
	if len(gz) >= len(raw) {
		t.Fatalf("gz not smaller: %d >= %d", len(gz), len(raw))
	}
	if !bytes.Equal(gunzipBytes(t, gz), raw) {
		t.Fatalf("gzip round-trip mismatch")
	}
	// Too small to bother.
	if gzipIfSmaller([]byte("hi")) != nil {
		t.Fatalf("tiny input should not be compressed")
	}
	// Already-compressed bytes barely shrink → identity (nil).
	if gzipIfSmaller(gz) != nil {
		t.Fatalf("incompressible input should return nil")
	}
}

func TestLoadAssetCachesAndCompresses(t *testing.T) {
	body := []byte(strings.Repeat("<path d='M0 0 L1 1'/>", 400))
	fsys := fstest.MapFS{
		"wallpapers/test.svg": &fstest.MapFile{Data: body},
		"tiny.txt":            &fstest.MapFile{Data: []byte("hi")},
	}
	r := &Router{assets: http.FS(fsys)}

	e, err := r.loadAsset("/wallpapers/test.svg")
	if err != nil {
		t.Fatalf("loadAsset: %v", err)
	}
	if !bytes.Equal(e.raw, body) {
		t.Fatalf("raw bytes mismatch")
	}
	if e.gz == nil {
		t.Fatalf("expected svg to be gzipped")
	}
	if !bytes.Equal(gunzipBytes(t, e.gz), body) {
		t.Fatalf("cached gzip does not round-trip to raw")
	}

	// Second call is a cache hit — same pointer, no rebuild.
	if e2, _ := r.loadAsset("/wallpapers/test.svg"); e2 != e {
		t.Fatalf("expected cache hit (identical pointer)")
	}

	// Tiny file: cached but not gzipped.
	te, err := r.loadAsset("/tiny.txt")
	if err != nil {
		t.Fatalf("tiny: %v", err)
	}
	if te.gz != nil {
		t.Fatalf("tiny file should not be gzipped")
	}

	// Missing file maps to not-exist.
	if _, err := r.loadAsset("/nope.svg"); err == nil || !isNotExist(err) {
		t.Fatalf("missing asset err = %v, want fs.ErrNotExist", err)
	}
}
