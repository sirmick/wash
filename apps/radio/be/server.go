package radio

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/audiorelay"
	"github.com/sirmick/wash/internal/sdk"
)

// station is one entry. URL is the upstream stream (kept BE-side; the FE
// addresses a station by its index via /stream?i=N, so we never expose
// upstream URLs to the page and the proxy is allowlisted by construction).
type station struct {
	Name  string `json:"name"`
	URL   string `json:"url,omitempty"`
	Codec string `json:"codec,omitempty"`
}

// pubStation is what the FE sees (no upstream URL).
type pubStation struct {
	Name  string `json:"name"`
	Codec string `json:"codec"`
}

// curated free/open stations — SomaFM + Radio Paradise (Icecast, proper
// HTTP, direct streams; classic Shoutcast "ICY 200" servers need special
// handling and are out of scope for v1). docs/RADIO.md §2.
var curated = []station{
	{Name: "SomaFM — Groove Salad", URL: "https://ice1.somafm.com/groovesalad-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — Drone Zone", URL: "https://ice1.somafm.com/dronezone-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — Indie Pop Rocks", URL: "https://ice1.somafm.com/indiepop-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — Lush", URL: "https://ice1.somafm.com/lush-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — DEF CON Radio", URL: "https://ice1.somafm.com/defcon-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — Secret Agent", URL: "https://ice1.somafm.com/secretagent-128-mp3", Codec: "mp3 128k"},
	{Name: "SomaFM — Beat Blender", URL: "https://ice1.somafm.com/beatblender-128-mp3", Codec: "mp3 128k"},
	{Name: "Radio Paradise — Main", URL: "https://stream.radioparadise.com/mp3-128", Codec: "mp3 128k"},
}

// streamClient has no overall timeout (the body is an endless stream); the
// transport's dial timeout still bounds connect.
var streamClient = &http.Client{Timeout: 0}

type svc struct {
	mu       sync.Mutex
	stations []station
	base     string
	ready    chan struct{}
}

func (s *svc) snapshot() ([]station, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]station(nil), s.stations...), s.base
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-radio ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	audiorelay.Register(bus)

	s := &svc{ready: make(chan struct{})}
	// env test stations first, then curated.
	s.stations = append(envStations(), curated...)

	reply := func(conn *sdk.Conn, id string) {
		<-s.ready
		sts, base := s.snapshot()
		pub := make([]pubStation, len(sts))
		for i, st := range sts {
			pub[i] = pubStation{Name: st.Name, Codec: st.Codec}
		}
		_ = conn.SendAppMsg(map[string]any{"kind": "stations_ok", "id": id, "base": base, "stations": pub})
	}

	// FE → BE: hand back the station list + ingress base.
	sdk.HandleVoid(bus, "stations", func(conn *sdk.Conn, id string, _ struct{}) error {
		go reply(conn, id)
		return nil
	})
	// FE → BE: add a user-pasted stream URL.
	sdk.HandleVoid(bus, "add", func(conn *sdk.Conn, id string, req addReq) error {
		if req.URL == "" {
			return nil
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = req.URL
		}
		s.mu.Lock()
		s.stations = append(s.stations, station{Name: name, URL: req.URL, Codec: "stream"})
		s.mu.Unlock()
		go reply(conn, id)
		return nil
	})

	go serveAndPublish(c, instanceID, s)
}

// serveAndPublish stands up the /stream proxy on a per-instance unix
// socket and publishes it through the ingress proxy.
func serveAndPublish(c *sdk.Conn, instanceID string, s *svc) {
	sock := filepath.Join(os.TempDir(), "wash-radio-"+instanceID+".sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Printf("wash-radio: listen %s: %v", sock, err)
		return
	}
	// onTitle pushes a live ICY track title to this instance's FE.
	onTitle := func(title string) {
		_ = c.SendAppMsg(map[string]any{"kind": "now_playing", "title": title})
	}
	mux := http.NewServeMux()
	// /stream?i=N reverse-proxies station N. Same origin as the shell, so
	// the browser's <audio> has no mixed-content/CORS problem.
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		i, err := strconv.Atoi(r.URL.Query().Get("i"))
		s.mu.Lock()
		ok := err == nil && i >= 0 && i < len(s.stations)
		var upstream string
		if ok {
			upstream = s.stations[i].URL
		}
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		proxyStream(w, r, upstream, onTitle)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, err := c.PublishIngress(ctx, "unix", sock)
	if err != nil {
		log.Printf("wash-radio: publish ingress: %v", err)
		_ = srv.Close()
		_ = os.Remove(sock)
		return
	}
	s.mu.Lock()
	s.base = base
	s.mu.Unlock()
	close(s.ready)
	log.Printf("wash-radio: %d station(s), serving at %s", len(s.stations), base)

	go func() {
		<-c.Done()
		_ = c.UnpublishIngress(base)
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
}

// proxyStream connects to upstream (requesting ICY metadata) and streams
// the audio to w, flushing each chunk. If the upstream interleaves ICY
// metadata (icy-metaint), we strip those blocks from the forwarded bytes
// (the browser's <audio> can't parse them) and push each new StreamTitle
// to the FE via onTitle. The browser cancelling (pause / station switch)
// closes r.Context() → the copy unwinds.
func proxyStream(w http.ResponseWriter, r *http.Request, upstream string, onTitle func(string)) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "wash-radio/0.1")
	req.Header.Set("Icy-MetaData", "1")
	resp, err := streamClient.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	metaint, _ := strconv.Atoi(resp.Header.Get("Icy-Metaint"))
	buf := make([]byte, 32*1024)

	// No ICY metadata → straight streaming copy.
	if metaint <= 0 {
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				flush()
			}
			if rerr != nil {
				return
			}
		}
	}

	// ICY: forward `metaint` audio bytes, then read+strip a metadata block.
	var last string
	remaining := metaint
	lenb := make([]byte, 1)
	for {
		if remaining > 0 {
			toRead := remaining
			if toRead > len(buf) {
				toRead = len(buf)
			}
			n, rerr := resp.Body.Read(buf[:toRead])
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				flush()
				remaining -= n
			}
			if rerr != nil {
				return
			}
			continue
		}
		if _, err := io.ReadFull(resp.Body, lenb); err != nil {
			return
		}
		if mlen := int(lenb[0]) * 16; mlen > 0 {
			mbuf := make([]byte, mlen)
			if _, err := io.ReadFull(resp.Body, mbuf); err != nil {
				return
			}
			if title := parseStreamTitle(string(bytes.TrimRight(mbuf, "\x00"))); title != "" && title != last {
				last = title
				onTitle(title)
			}
		}
		remaining = metaint
	}
}

// parseStreamTitle pulls the track out of an ICY metadata block, e.g.
// `StreamTitle='Artist - Track';StreamUrl='…';`.
func parseStreamTitle(meta string) string {
	const key = "StreamTitle='"
	i := strings.Index(meta, key)
	if i < 0 {
		return ""
	}
	rest := meta[i+len(key):]
	if j := strings.Index(rest, "';"); j >= 0 {
		return rest[:j]
	}
	if j := strings.LastIndex(rest, "'"); j >= 0 {
		return rest[:j]
	}
	return ""
}

// envStations parses $WASH_RADIO_STATIONS — comma-separated "Name|url"
// entries — prepended to the list (used by tests + power users).
func envStations() []station {
	raw := os.Getenv("WASH_RADIO_STATIONS")
	if raw == "" {
		return nil
	}
	var out []station
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		name, url, ok := strings.Cut(e, "|")
		if !ok {
			name, url = e, e
		}
		if strings.TrimSpace(url) == "" {
			continue
		}
		out = append(out, station{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url), Codec: "stream"})
	}
	return out
}

type addReq struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
