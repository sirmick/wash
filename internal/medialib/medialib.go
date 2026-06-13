// Package medialib is the shared local-media backend for wash's native
// players (Music now; Washamp/video can adopt it): a recursive audio-file
// scan and a Range-capable ingress file server whose root can be swapped
// at runtime (folder selection). Filename-stem titles for now; richer tag
// reading is a later addition.
package medialib

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/sdk"
)

// MaxTracks caps a scan so a huge folder can't blow up the message/list.
const MaxTracks = 500

// audioExts are the extensions surfaced from a folder. The browser's
// <audio>/<video> decodes them; the BE never decodes.
var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".ogg": true, ".oga": true, ".opus": true,
	".flac": true, ".m4a": true, ".aac": true, ".webm": true,
}

// Entry is one scanned file: a forward-slash path relative to the scan
// root, plus a display title (the filename stem).
type Entry struct {
	Rel   string `json:"rel"`
	Title string `json:"title"`
}

// AudioExt reports whether name has a recognised audio extension.
func AudioExt(name string) bool {
	return audioExts[strings.ToLower(filepath.Ext(name))]
}

// Stem is the filename without its extension — the display title.
func Stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// DefaultDir resolves the default library root: $WASH_MUSIC_DIR, else
// ~/Music. Empty if neither is a directory.
func DefaultDir() string {
	if d := os.Getenv("WASH_MUSIC_DIR"); d != "" {
		if isDir(d) {
			return d
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if d := filepath.Join(home, "Music"); isDir(d) {
		return d
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Scan walks root recursively (skipping dot-dirs), returns audio entries
// sorted by path, capped at MaxTracks.
func Scan(root string) []Entry {
	if root == "" {
		return nil
	}
	var out []Entry
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip but say so — a library that scans to zero tracks
			// because the root is unreadable should explain itself.
			log.Printf("medialib: scan %s: %v (skipped)", p, err)
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !AudioExt(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		out = append(out, Entry{Rel: filepath.ToSlash(rel), Title: Stem(d.Name())})
		if len(out) >= MaxTracks {
			log.Printf("medialib: scan hit cap (%d); ignoring the rest", MaxTracks)
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// EscapePath URL-escapes each path segment so a rel path with spaces /
// reserved chars becomes a valid ingress URL tail.
func EscapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return path.Join(parts...)
}

// Serve stands up a Range-capable file server on a per-instance unix
// socket whose /lib/<rel> reads from rootFn() (re-read per request, so the
// caller can swap the folder at runtime), publishes it via PublishIngress,
// and returns the base path (e.g. "/app/<token>/"). Track URLs are then
// base + "lib/" + EscapePath(rel). The socket + ingress are torn down on
// c.Done().
func Serve(c *sdk.Conn, instanceID, sockPrefix string, rootFn func() string) (string, error) {
	sock := filepath.Join(os.TempDir(), sockPrefix+instanceID+".sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/lib/", func(w http.ResponseWriter, r *http.Request) {
		root := rootFn()
		if root == "" {
			http.NotFound(w, r)
			return
		}
		// os.DirFS(root) confines reads to the current root; http.FileServer
		// handles Range + path cleaning. Cheap to build per request.
		http.StripPrefix("/lib/", http.FileServer(http.FS(os.DirFS(root)))).ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("medialib: file server stopped: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, err := c.PublishIngress(ctx, "unix", sock)
	if err != nil {
		_ = srv.Close()
		_ = os.Remove(sock)
		return "", err
	}
	go func() {
		<-c.Done()
		_ = c.UnpublishIngress(base)
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
	return base, nil
}
