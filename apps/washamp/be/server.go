package washamp

import (
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/sdk"
)

// skinsFS holds the bundled classic-skin .wsz files, served over ingress
// and offered to Webamp as initialSkin + availableSkins (the Options →
// Skins menu). These are the webamp project's own demo skins.
//
//go:embed skins/*.wsz
var skinsFS embed.FS

// bundledSkins maps each .wsz to a display name. Order matters: index 0
// is the default skin the player opens with; all appear in the skins
// menu so the user can switch.
var bundledSkins = []struct{ Name, File string }{
	{"Winamp5 Classified", "Winamp5-Classified.wsz"},
	{"HP-49G Calculator", "HPCalc-HP49G.wsz"},
	{"Internet Archive", "Internet-Archive.wsz"},
	{"Green Dimension V2", "Green-Dimension-V2.wsz"},
	{"TopazAmp", "TopazAmp1-2.wsz"},
	{"Mac OSX Aqua", "MacOSXAqua1-5.wsz"},
	{"Vizor", "Vizor1-01.wsz"},
}

// skin is one entry in the FE's skin list (resolved ingress URL).
type skin struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// maxTracks caps a library scan so a huge music folder can't blow up the
// tracks message (or the playlist). The cap is logged when hit.
const maxTracks = 500

// audioExts are the file extensions we surface from the library. The
// browser's <audio> decodes them (Case 1, docs/AUDIO.md §1) — the BE
// never decodes, so this is just "what we hand to the FE".
var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".ogg": true, ".oga": true, ".opus": true,
	".flac": true, ".m4a": true, ".aac": true, ".webm": true,
}

// track is one playlist entry handed to the FE. URL is fully resolved
// (ingress path for local files, absolute for streams); the FE feeds it
// straight to webamp.
type track struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

// player holds the per-instance ingress state. The FE may ask for the
// track list before the BE has finished publishing ingress, so the
// "tracks" reply waits on ready.
type player struct {
	mu     sync.Mutex
	tracks []track
	skins  []skin
	ready  chan struct{}
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-washamp ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	p := &player{ready: make(chan struct{})}

	// FE → BE: hand back the resolved track list once the file server is
	// published. Reply from a goroutine because ingress may not be ready
	// yet and bus handlers run on the read goroutine — blocking here
	// would stall dispatch.
	sdk.HandleVoid(bus, "tracks", func(conn *sdk.Conn, id string, _ struct{}) error {
		go func() {
			select {
			case <-p.ready:
			case <-conn.Done():
				return
			}
			p.mu.Lock()
			tracks := p.tracks
			skins := p.skins
			p.mu.Unlock()
			_ = conn.SendAppMsg(map[string]any{"kind": "tracks_ok", "id": id, "tracks": tracks, "skins": skins})
		}()
		return nil
	})

	// Bridge the FE's playback state ↔ the com.wash.audio control plane.
	registerAudioRelay(bus)

	go serveAndPublish(c, instanceID, p)
}

// serveAndPublish stands up a Range-capable HTTP file server (rooted at
// the music library) on a per-instance unix socket, publishes it through
// the router's ingress proxy, then resolves the playlist URLs against the
// returned base path.
func serveAndPublish(c *sdk.Conn, instanceID string, p *player) {
	root := musicDir()
	lib := scanLibrary(root)

	sock := filepath.Join(os.TempDir(), "wash-washamp-"+instanceID+".sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Printf("wash-washamp: listen %s: %v", sock, err)
		return
	}

	mux := http.NewServeMux()
	// /lib/<relpath> serves library files. http.FileServer handles Range
	// + path cleaning; os.DirFS confines reads to the library root.
	if root != "" {
		mux.Handle("/lib/", http.StripPrefix("/lib/", http.FileServer(http.FS(os.DirFS(root)))))
	}
	// /sample.wav is the always-present fallback so the player is never
	// empty (and the M1 e2e stays valid when no library is configured).
	wav := sampleWAV()
	mux.HandleFunc("/sample.wav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		http.ServeContent(w, r, "sample.wav", time.Unix(0, 0), bytes.NewReader(wav))
	})
	// /skins/<name>.wsz serves the embedded classic skins (Range handled
	// by http.FileServer). Same origin as the shell, so Webamp's fetch of
	// the .wsz has no CORS issue.
	if sub, err := fs.Sub(skinsFS, "skins"); err == nil {
		mux.Handle("/skins/", http.StripPrefix("/skins/", http.FileServer(http.FS(sub))))
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, err := c.PublishIngress(ctx, "unix", sock)
	if err != nil {
		log.Printf("wash-washamp: publish ingress: %v", err)
		_ = srv.Close()
		_ = os.Remove(sock)
		return
	}

	tracks := resolveTracks(base, lib)
	skins := make([]skin, len(bundledSkins))
	for i, s := range bundledSkins {
		skins[i] = skin{Name: s.Name, URL: base + "skins/" + s.File}
	}
	p.mu.Lock()
	p.tracks = tracks
	p.skins = skins
	p.mu.Unlock()
	close(p.ready)
	log.Printf("wash-washamp: %d track(s) from %q, serving at %s", len(tracks), root, base)

	go func() {
		<-c.Done()
		_ = c.UnpublishIngress(base)
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
}

// musicDir resolves the library root: $WASH_MUSIC_DIR, else ~/Music.
// Returns "" if neither resolves to an existing directory (→ the player
// falls back to the sample track).
func musicDir() string {
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
	d := filepath.Join(home, "Music")
	if isDir(d) {
		return d
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// libEntry is a scanned file before its URL is resolved against ingress.
type libEntry struct {
	rel   string // forward-slash relative path under root
	title string
}

// scanLibrary walks root for audio files (recursive, hidden dirs
// skipped), capped at maxTracks, sorted by path for deterministic order.
func scanLibrary(root string) []libEntry {
	if root == "" {
		return nil
	}
	var out []libEntry
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !audioExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		out = append(out, libEntry{rel: filepath.ToSlash(rel), title: stem(d.Name())})
		if len(out) >= maxTracks {
			log.Printf("wash-washamp: library scan hit cap (%d); ignoring the rest", maxTracks)
			return fs.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// resolveTracks turns scanned entries + configured streams into the FE
// track list. Local files get an escaped ingress URL; streams pass
// through verbatim. Falls back to the sample when there's nothing.
func resolveTracks(base string, lib []libEntry) []track {
	var tracks []track
	for _, e := range lib {
		tracks = append(tracks, track{
			URL:   base + "lib/" + escapePath(e.rel),
			Title: e.title,
		})
	}
	for _, s := range streams() {
		tracks = append(tracks, track{URL: s, Title: streamLabel(s)})
	}
	if len(tracks) == 0 {
		tracks = append(tracks, track{URL: base + "sample.wav", Title: "Wash Test Tone", Artist: "wash-audio"})
	}
	return tracks
}

// streams reads $WASH_MUSIC_STREAMS (comma-separated absolute URLs) —
// web radio / Icecast endpoints the <audio> element plays natively.
func streams() []string {
	raw := os.Getenv("WASH_MUSIC_STREAMS")
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			out = append(out, s)
		}
	}
	return out
}

func streamLabel(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Host
	}
	return s
}

// escapePath percent-escapes each path segment while keeping the
// separators, so filenames with spaces/unicode resolve through ingress.
func escapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return path.Join(parts...)
}

func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// sampleWAV synthesizes a 3-second 440 Hz sine as a 16-bit mono PCM
// WAV. License-clean placeholder so the player is never empty when no
// library is configured. A short linear fade in/out avoids the click an
// abrupt start/stop would produce.
func sampleWAV() []byte {
	const (
		sampleRate = 44100
		seconds    = 3
		freq       = 440.0
		amp        = 0.25
		fade       = 0.05
	)
	n := sampleRate * seconds
	dataLen := n * 2 // 16-bit mono

	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, le, uint32(36+dataLen))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, le, uint32(16))           // PCM fmt chunk size
	_ = binary.Write(&buf, le, uint16(1))            // audio format: PCM
	_ = binary.Write(&buf, le, uint16(1))            // channels: mono
	_ = binary.Write(&buf, le, uint32(sampleRate))   // sample rate
	_ = binary.Write(&buf, le, uint32(sampleRate*2)) // byte rate
	_ = binary.Write(&buf, le, uint16(2))            // block align
	_ = binary.Write(&buf, le, uint16(16))           // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, le, uint32(dataLen))

	for i := 0; i < n; i++ {
		t := float64(i) / float64(sampleRate)
		env := 1.0
		if t < fade {
			env = t / fade
		} else if t > seconds-fade {
			env = (seconds - t) / fade
		}
		v := amp * env * math.Sin(2*math.Pi*freq*t)
		_ = binary.Write(&buf, le, int16(v*32767))
	}
	return buf.Bytes()
}
