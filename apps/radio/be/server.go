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
	"github.com/sirmick/wash/pkg/sdk"
)

// station is one entry. URL is the upstream stream (kept BE-side; the FE
// addresses a station by its index via /stream?i=N, so we never expose
// upstream URLs to the page and the proxy is allowlisted by construction).
type station struct {
	Name        string `json:"name"`
	URL         string `json:"url,omitempty"`
	Codec       string `json:"codec,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Subtype     string `json:"subtype,omitempty"`
	Source      string `json:"source,omitempty"`
	Description string `json:"description,omitempty"`
}

// pubStation is what the FE sees (no upstream URL).
type pubStation struct {
	Name        string `json:"name"`
	Codec       string `json:"codec"`
	Genre       string `json:"genre,omitempty"`
	Subtype     string `json:"subtype,omitempty"`
	Source      string `json:"source,omitempty"`
	Description string `json:"description,omitempty"`
}

type streamInfo struct {
	ContentType    string `json:"content_type,omitempty"`
	Bitrate        string `json:"bitrate,omitempty"`
	IcyName        string `json:"icy_name,omitempty"`
	IcyGenre       string `json:"icy_genre,omitempty"`
	IcyURL         string `json:"icy_url,omitempty"`
	IcyDescription string `json:"icy_description,omitempty"`
	MetaInterval   int    `json:"meta_interval,omitempty"`
}

// streamClient has no overall timeout (the body is an endless stream); the
// transport's dial timeout still bounds connect.
var streamClient = &http.Client{Timeout: 0}

type svc struct {
	mu     sync.Mutex
	fixed  []station // env + configured stations (immutable for this app run)
	custom []station // user-pasted; replaced wholesale by set_custom
	base   string
	ready  chan struct{}
}

// all returns the full station list (index space the FE addresses via
// /stream?i=N): fixed first, then custom.
func (s *svc) all() []station {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(append([]station(nil), s.fixed...), s.custom...)
}

func (s *svc) snapshot() ([]station, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(append([]station(nil), s.fixed...), s.custom...), s.base
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-radio ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	audiorelay.Register(bus)

	s := &svc{ready: make(chan struct{})}
	// Env test stations first, then the disk-backed user-configurable list.
	s.fixed = append(envStations(), configuredStations()...)

	reply := func(conn *sdk.Conn, id string) {
		<-s.ready
		sts, base := s.snapshot()
		pub := make([]pubStation, len(sts))
		for i, st := range sts {
			pub[i] = pubStation{
				Name:        st.Name,
				Codec:       st.Codec,
				Genre:       st.Genre,
				Subtype:     st.Subtype,
				Source:      st.Source,
				Description: st.Description,
			}
		}
		_ = conn.SendAppMsg(map[string]any{"kind": "stations_ok", "id": id, "base": base, "stations": pub})
	}

	// FE → BE: hand back the station list + ingress base.
	sdk.HandleVoid(bus, "stations", func(conn *sdk.Conn, id string, _ struct{}) error {
		go reply(conn, id)
		return nil
	})
	// FE → BE: replace the user's custom (pasted) stations wholesale. The
	// FE owns the canonical list (persisted in app_state) and re-sends it
	// on mount, so this is idempotent — no duplicates across reload.
	sdk.HandleVoid(bus, "set_custom", func(conn *sdk.Conn, id string, req setCustomReq) error {
		cs := make([]station, 0, len(req.Stations))
		for _, c := range req.Stations {
			if c.URL == "" {
				continue
			}
			name := strings.TrimSpace(c.Name)
			if name == "" {
				name = c.URL
			}
			cs = append(cs, station{Name: name, URL: c.URL, Codec: "stream", Genre: "Custom", Source: "Custom"})
		}
		s.mu.Lock()
		s.custom = cs
		s.mu.Unlock()
		go reply(conn, id)
		return nil
	})
	// Persist the FE's small state blob (favorites + pasted stations +
	// last-tuned), redelivered as wash:state on the next mount.
	sdk.HandlePersist(bus)

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
	// ICY metadata arrives on the stream path; keep it tagged by BE index so
	// stale responses from a just-cancelled stream cannot overwrite the UI.
	onTitle := func(i int, title string) {
		_ = c.SendAppMsg(map[string]any{"kind": "now_playing", "station": i, "title": title})
	}
	onInfo := func(i int, info streamInfo) {
		_ = c.SendAppMsg(map[string]any{"kind": "stream_info", "station": i, "info": info})
	}
	mux := http.NewServeMux()
	// /stream?i=N reverse-proxies station N. Same origin as the shell, so
	// the browser's <audio> has no mixed-content/CORS problem.
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		i, err := strconv.Atoi(r.URL.Query().Get("i"))
		all := s.all()
		ok := err == nil && i >= 0 && i < len(all)
		if !ok {
			http.NotFound(w, r)
			return
		}
		proxyStream(w, r, all[i].URL, func(title string) { onTitle(i, title) }, func(info streamInfo) { onInfo(i, info) })
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
	log.Printf("wash-radio: %d station(s), serving at %s", len(s.all()), base)

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
func proxyStream(w http.ResponseWriter, r *http.Request, upstream string, onTitle func(string), onInfo func(streamInfo)) {
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
	metaint, _ := strconv.Atoi(resp.Header.Get("Icy-Metaint"))
	onInfo(headerStreamInfo(resp, metaint))
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

func headerStreamInfo(resp *http.Response, metaint int) streamInfo {
	return streamInfo{
		ContentType:    resp.Header.Get("Content-Type"),
		Bitrate:        resp.Header.Get("Icy-Br"),
		IcyName:        resp.Header.Get("Icy-Name"),
		IcyGenre:       resp.Header.Get("Icy-Genre"),
		IcyURL:         resp.Header.Get("Icy-Url"),
		IcyDescription: resp.Header.Get("Icy-Description"),
		MetaInterval:   metaint,
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
		out = append(out, station{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url), Codec: "stream", Genre: "Custom", Source: "Custom"})
	}
	return out
}

type setCustomReq struct {
	Stations []customStation `json:"stations"`
}

type customStation struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
