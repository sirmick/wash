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

// curated free/open stations — SomaFM + Radio Paradise + BassDrive
// (Icecast/HTTP, direct streams; classic Shoutcast "ICY 200" servers need
// special handling and are out of scope). The Codec field carries a short
// genre label (handier than the bitrate for browsing). docs/RADIO.md §2.
var curated = []station{
	// electronic
	{Name: "SomaFM — Groove Salad", URL: "https://ice1.somafm.com/groovesalad-128-mp3", Codec: "chill", Genre: "Electronic", Subtype: "Downtempo / Chill", Source: "SomaFM", Description: "Ambient and downtempo beats."},
	{Name: "SomaFM — Groove Salad 2", URL: "https://ice1.somafm.com/groovesalad2-128-mp3", Codec: "chill", Genre: "Electronic", Subtype: "Downtempo / Chill", Source: "SomaFM", Description: "A different plate of ambient/downtempo beats."},
	{Name: "SomaFM — Beat Blender", URL: "https://ice1.somafm.com/beatblender-128-mp3", Codec: "deep house", Genre: "Electronic", Subtype: "Deep House", Source: "SomaFM", Description: "Deep-house and downtempo electronic."},
	{Name: "SomaFM — The Trip", URL: "https://ice1.somafm.com/thetrip-128-mp3", Codec: "progressive / trance", Genre: "Electronic", Subtype: "Progressive / Trance", Source: "SomaFM", Description: "Progressive house and trance."},
	{Name: "SomaFM — cliqhop idm", URL: "https://ice1.somafm.com/cliqhop-128-mp3", Codec: "IDM", Genre: "Electronic", Subtype: "IDM", Source: "SomaFM", Description: "Blips, bleeps, and intelligent dance music."},
	{Name: "BassDrive", URL: "http://ice.bassdrive.net/stream", Codec: "drum & bass", Genre: "Electronic", Subtype: "Drum & Bass", Source: "BassDrive", Description: "Drum and bass radio."},
	{Name: "SomaFM — Fluid", URL: "https://ice1.somafm.com/fluid-128-mp3", Codec: "dnb / future soul", Genre: "Electronic", Subtype: "Drum & Bass", Source: "SomaFM", Description: "Drum and bass, future soul, and liquid beats."},
	{Name: "SomaFM — Dub Step Beyond", URL: "https://ice1.somafm.com/dubstep-128-mp3", Codec: "dubstep", Genre: "Electronic", Subtype: "Dubstep / Bass", Source: "SomaFM", Description: "Dubstep and bass-forward electronic."},
	{Name: "SomaFM — PopTron", URL: "https://ice1.somafm.com/poptron-128-mp3", Codec: "electropop", Genre: "Electronic", Subtype: "Electropop / Synthpop", Source: "SomaFM", Description: "Electropop and indie electronic."},
	{Name: "SomaFM — Underground 80s", URL: "https://ice1.somafm.com/u80s-128-mp3", Codec: "synthpop / new wave", Genre: "Electronic", Subtype: "Electropop / Synthpop", Source: "SomaFM", Description: "Early alternative, synthpop, and new wave."},
	{Name: "SomaFM — Vaporwaves", URL: "https://ice1.somafm.com/vaporwaves-128-mp3", Codec: "vaporwave", Genre: "Electronic", Subtype: "Vaporwave", Source: "SomaFM", Description: "Vaporwave and late-night retrofuturism."},
	{Name: "SomaFM — Space Station Soma", URL: "https://ice1.somafm.com/spacestation-128-mp3", Codec: "space electronica", Genre: "Electronic", Subtype: "Ambient / Space", Source: "SomaFM", Description: "Ambient and mid-tempo space electronica."},
	{Name: "Sunshine Live", URL: "http://stream.sunshine-live.de/live/mp3-192/stream.sunshine-live.de/", Codec: "techno / house", Genre: "Electronic", Subtype: "Techno", Source: "Sunshine Live", Description: "Electronic, house, and techno."},
	// ambient
	{Name: "SomaFM — Drone Zone", URL: "https://ice1.somafm.com/dronezone-128-mp3", Codec: "ambient", Genre: "Ambient", Subtype: "Drone", Source: "SomaFM", Description: "Atmospheric ambient space music."},
	{Name: "SomaFM — Deep Space One", URL: "https://ice1.somafm.com/deepspaceone-128-mp3", Codec: "deep ambient", Genre: "Ambient", Subtype: "Deep Ambient", Source: "SomaFM", Description: "Deep ambient electronic and space music."},
	{Name: "SomaFM — Synphaera", URL: "https://ice1.somafm.com/synphaera-128-mp3", Codec: "ambient space", Genre: "Ambient", Subtype: "Space Ambient", Source: "SomaFM", Description: "Ambient and electronic space music."},
	{Name: "SomaFM — The Dark Zone", URL: "https://ice1.somafm.com/darkzone-128-mp3", Codec: "dark ambient", Genre: "Ambient", Subtype: "Dark Ambient", Source: "SomaFM", Description: "Dark ambient music."},
	// rock
	{Name: "SomaFM — BAGeL Radio", URL: "https://ice1.somafm.com/bagel-128-mp3", Codec: "alt / modern rock", Genre: "Rock", Subtype: "Alternative / Modern", Source: "SomaFM", Description: "Alternative and modern rock."},
	{Name: "KEXP", URL: "https://kexp-mp3-128.streamguys1.com/kexp128.mp3", Codec: "indie / alternative", Genre: "Rock", Subtype: "Alternative / Modern", Source: "KEXP", Description: "Seattle listener-powered indie and alternative radio."},
	{Name: "SomaFM — Indie Pop Rocks", URL: "https://ice1.somafm.com/indiepop-128-mp3", Codec: "indie pop", Genre: "Rock", Subtype: "Indie Rock", Source: "SomaFM", Description: "Indie pop and indie rock."},
	{Name: "Rock Antenne — Punk Rock", URL: "http://mp3channels.webradio.rockantenne.de/punkrock.aac", Codec: "punk rock", Genre: "Rock", Subtype: "Punk Rock", Source: "Rock Antenne", Description: "Punk rock stream."},
	{Name: "Hard Rock Heaven", URL: "http://hydra.cdnstream.com/1521_128", Codec: "hard rock", Genre: "Rock", Subtype: "Hard Rock", Source: "Hard Rock Heaven", Description: "Hard rock and heavy rock."},
	{Name: "Radio BOB — Nu Metal", URL: "https://streams.radiobob.de/numetal/mp3-192/", Codec: "nu-metal", Genre: "Rock", Subtype: "Nu-Metal", Source: "Radio BOB", Description: "Dedicated nu-metal stream."},
	{Name: "NRJ — Linkin Park", URL: "https://streaming.nrjaudio.fm/ouvfbfoarp52", Codec: "nu-metal", Genre: "Rock", Subtype: "Nu-Metal", Source: "NRJ", Description: "Linkin Park-focused nu-metal stream."},
	// other popular broad genres
	{Name: "SWR3", URL: "https://liveradio.swr.de/sw282p3/swr3/play.mp3", Codec: "pop / rock", Genre: "Pop", Subtype: "Pop Rock", Source: "SWR3", Description: "German pop and rock radio."},
	{Name: "181.FM — Old School HipHop/RnB", URL: "http://listen.181fm.com/181-oldschool_128k.mp3", Codec: "old school", Genre: "Hip-Hop / R&B", Subtype: "Old School", Source: "181.FM", Description: "Old-school hip-hop and R&B."},
	{Name: ".977 Country", URL: "http://26343.live.streamtheworld.com/977_COUNTRY_SC", Codec: "country", Genre: "Country / Americana", Subtype: "Country", Source: ".977 Music", Description: "Country radio."},
	{Name: "SomaFM — Boot Liquor", URL: "https://ice1.somafm.com/bootliquor-128-mp3", Codec: "americana", Genre: "Country / Americana", Subtype: "Americana", Source: "SomaFM", Description: "Americana roots music."},
	{Name: "LOS40 Spain", URL: "https://playerservices.streamtheworld.com/api/livestream-redirect/Los40.mp3", Codec: "latin pop", Genre: "Latin / World", Subtype: "Latin Pop", Source: "LOS40", Description: "Spanish top-40 and Latin pop."},
	{Name: "SomaFM — Suburbs of Goa", URL: "https://ice1.somafm.com/suburbsofgoa-128-mp3", Codec: "worldbeat", Genre: "Latin / World", Subtype: "Worldbeat", Source: "SomaFM", Description: "Desi-influenced Asian world beats."},
	{Name: "SomaFM — Bossa Beyond", URL: "https://ice1.somafm.com/bossa-128-mp3", Codec: "bossa nova", Genre: "Latin / World", Subtype: "Bossa Nova", Source: "SomaFM", Description: "Bossa nova, samba, and beyond."},
	{Name: "SomaFM — Sonic Universe", URL: "https://ice1.somafm.com/sonicuniverse-128-mp3", Codec: "avant jazz", Genre: "Jazz", Subtype: "Avant Jazz", Source: "SomaFM", Description: "Avant-garde jazz and exploratory sounds."},
	{Name: "101 Smooth Jazz", URL: "http://jking.cdnstream1.com/b22139_128mp3", Codec: "smooth jazz", Genre: "Jazz", Subtype: "Smooth Jazz", Source: "101 Smooth Jazz", Description: "Smooth jazz radio."},
	{Name: "Classic FM UK", URL: "http://ice-the.musicradio.com/ClassicFMMP3", Codec: "classical", Genre: "Classical", Subtype: "Classical", Source: "Classic FM", Description: "Classical music radio."},
	{Name: "SomaFM — Heavyweight Reggae", URL: "https://ice1.somafm.com/reggae-128-mp3", Codec: "reggae / ska", Genre: "Reggae / Ska", Subtype: "Reggae / Ska", Source: "SomaFM", Description: "Reggae, dub, ska, and rocksteady."},
	{Name: "SomaFM — Metal Detector", URL: "https://ice1.somafm.com/metal-128-mp3", Codec: "metal", Genre: "Metal", Subtype: "Metal", Source: "SomaFM", Description: "Classic and current metal."},
	{Name: "Rock Antenne — Heavy Metal", URL: "http://mp3channels.webradio.rockantenne.de/heavy-metal", Codec: "heavy metal", Genre: "Metal", Subtype: "Heavy Metal", Source: "Rock Antenne", Description: "Heavy metal stream."},
	{Name: "SomaFM — Folk Forward", URL: "https://ice1.somafm.com/folkfwd-128-mp3", Codec: "folk", Genre: "Folk", Subtype: "Indie Folk", Source: "SomaFM", Description: "Indie folk, alt-folk, and folk classics."},
	{Name: "SomaFM — Seven Inch Soul", URL: "https://ice1.somafm.com/7soul-128-mp3", Codec: "soul / oldies", Genre: "Oldies / Soul", Subtype: "Soul", Source: "SomaFM", Description: "Vintage soul from original 45 RPM vinyl."},
	{Name: "SomaFM — The In-Sound", URL: "https://ice1.somafm.com/insound-128-mp3", Codec: "60s / 70s pop", Genre: "Oldies / Soul", Subtype: "Oldies", Source: "SomaFM", Description: "60s and 70s hipster Euro-pop."},
	{Name: "SomaFM — Secret Agent", URL: "https://ice1.somafm.com/secretagent-128-mp3", Codec: "spy lounge", Genre: "Lounge", Subtype: "Spy Lounge", Source: "SomaFM", Description: "Spy jazz and cinematic lounge."},
	{Name: "SomaFM — Illinois Street Lounge", URL: "https://ice1.somafm.com/illstreet-128-mp3", Codec: "lounge", Genre: "Lounge", Subtype: "Exotica", Source: "SomaFM", Description: "Classic bachelor-pad exotica and lounge."},
	{Name: "Radio Paradise — Main", URL: "https://stream.radioparadise.com/mp3-128", Codec: "eclectic", Genre: "Eclectic", Subtype: "Main Mix", Source: "Radio Paradise", Description: "Eclectic listener-supported radio."},
	{Name: "Radio Paradise — Rock", URL: "https://stream.radioparadise.com/rock-128", Codec: "rock", Genre: "Eclectic", Subtype: "Rock Mix", Source: "Radio Paradise", Description: "Radio Paradise rock mix."},
}

// streamClient has no overall timeout (the body is an endless stream); the
// transport's dial timeout still bounds connect.
var streamClient = &http.Client{Timeout: 0}

type svc struct {
	mu     sync.Mutex
	fixed  []station // env + curated (immutable)
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
	// env test stations first, then curated.
	s.fixed = append(envStations(), curated...)

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
