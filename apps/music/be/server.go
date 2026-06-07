package music

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/sdk"
)

// player holds the per-instance ingress state. The FE may ask for the
// track list before the BE has finished publishing ingress, so the
// "tracks" reply waits on ready.
type player struct {
	mu    sync.Mutex
	base  string
	ready chan struct{}
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-music ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	p := &player{ready: make(chan struct{})}

	// FE → BE: hand back the ingress base + track list once the file
	// server is published. Reply from a goroutine because ingress may
	// not be ready yet and bus handlers run on the read goroutine —
	// blocking here would stall dispatch.
	sdk.HandleVoid(bus, "tracks", func(conn *sdk.Conn, id string, _ struct{}) error {
		go func() {
			select {
			case <-p.ready:
			case <-conn.Done():
				return
			}
			p.mu.Lock()
			base := p.base
			p.mu.Unlock()
			_ = conn.SendAppMsg(map[string]any{
				"kind":   "tracks_ok",
				"id":     id,
				"base":   base,
				"tracks": []map[string]any{{"file": "sample.wav", "title": "Wash Test Tone", "artist": "wash-audio"}},
			})
		}()
		return nil
	})

	// Bridge the FE's playback state ↔ the com.wash.audio control plane
	// (now-playing in the sidebar, transport/volume from the sidebar).
	registerAudioRelay(bus)

	go serveAndPublish(c, instanceID, p)
}

// serveAndPublish stands up a tiny Range-capable HTTP file server on a
// per-instance unix socket and publishes it through the router's
// ingress proxy. Webamp fetches tracks at the returned base path.
func serveAndPublish(c *sdk.Conn, instanceID string, p *player) {
	sock := filepath.Join(os.TempDir(), "wash-music-"+instanceID+".sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Printf("wash-music: listen %s: %v", sock, err)
		return
	}

	wav := sampleWAV()
	mux := http.NewServeMux()
	mux.HandleFunc("/sample.wav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		// ServeContent handles Range/If-Range and sets Accept-Ranges,
		// which is what the <audio> element wants for seeking.
		http.ServeContent(w, r, "sample.wav", time.Unix(0, 0), bytes.NewReader(wav))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, err := c.PublishIngress(ctx, "unix", sock)
	if err != nil {
		log.Printf("wash-music: publish ingress: %v", err)
		_ = srv.Close()
		_ = os.Remove(sock)
		return
	}
	p.mu.Lock()
	p.base = base
	p.mu.Unlock()
	close(p.ready)
	log.Printf("wash-music: serving audio at %s (sock=%s)", base, sock)

	// Tear down with the connection: drop the route, stop the server,
	// remove the socket.
	go func() {
		<-c.Done()
		_ = c.UnpublishIngress(base)
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
}

// sampleWAV synthesizes a 3-second 440 Hz sine as a 16-bit mono PCM
// WAV. License-clean placeholder so M1 can prove the audio pipeline
// without shipping any copyrighted track; M2 serves the real library.
// A short linear fade in/out avoids the click an abrupt start/stop
// would produce.
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
