// Package inferenceapp provides com.wash.inference, wash's optional one-shot
// inference service. It owns provider credentials and presents one small,
// provider-neutral contract to other trusted wash apps.
package inferenceapp

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/apps/registry"
	shared "github.com/sirmick/wash/pkg/inference"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

const AppID = shared.ServiceAppID
const settingsAppID = "com.wash.settings"
const maxInputBytes = 1 << 20

//go:embed all:assets
var assetsFS embed.FS

var def *sdk.AppDef
var service *server

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err)
	}
	def = &sdk.AppDef{Manifest: sdk.Manifest{
		ID: AppID, Name: "AI Provider", Version: version.Version, ProtocolVersion: sdk.ProtocolVersion,
		Surface: sdk.SurfaceBackground, Instancing: sdk.InstancingSingleton,
		SettingsPanel: &sdk.SettingsPanel{Section: "AI Provider", Element: "wash-settings-panel-inference"},
	}, Assets: sub, OnReady: onReady, OnInstanceGone: onInstanceGone}
	registry.Register(&registry.App{Name: "wash-inference", Manifest: def.Manifest, Assets: def.Assets, Run: run})
}
func Def() *sdk.AppDef              { return def }
func run(ctx context.Context) error { return sdk.Run(ctx, def) }

type publicConnection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Adapter       string `json:"adapter"`
	BaseURL       string `json:"base_url,omitempty"`
	Model         string `json:"model,omitempty"`
	HasCredential bool   `json:"has_credential,omitempty"`
	Available     bool   `json:"available"`
	Detail        string `json:"detail,omitempty"`
}
type state struct {
	Default     string             `json:"default,omitempty"`
	Connections []publicConnection `json:"connections"`
}
type saveReq struct {
	ID      string  `json:"connection_id"`
	BaseURL string  `json:"base_url,omitempty"`
	Model   string  `json:"model,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
}
type selectReq struct {
	ID string `json:"connection_id"`
}
type testReq struct {
	ID string `json:"connection_id"`
}
type cancelReq struct {
	ID string `json:"id"`
}
type job struct {
	id     string
	from   wire.Sender
	req    shared.Request
	conn   connection
	ctx    context.Context
	cancel context.CancelFunc
}

type server struct {
	conn           *sdk.Conn
	bus            *sdk.Bus
	states         *sdk.StateService[state]
	mu             sync.Mutex
	cfg            config
	jobs           map[string]*job
	activeBySource map[string]bool
	queue          chan *job
}

var allowedCallers = map[string]bool{
	"com.wash.settings": true, "com.wash.session": true, "com.wash.ai": true, "com.wash.agents": true,
	"com.wash.agentd": true, "com.wash.term": true, "com.wash.edit": true,
	"com.wash.session-summary": true,
}

func onReady(c *sdk.Conn, _ string, _ uint32) {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("wash-inference: config: %v; using defaults", err)
		cfg = defaultConfig()
	}
	b := sdk.NewBus(c)
	s := &server{conn: c, bus: b, cfg: cfg, jobs: map[string]*job{}, activeBySource: map[string]bool{}, queue: make(chan *job, 16)}
	s.states = sdk.NewStateService(b, s.publicState())
	s.register()
	service = s
	for i := 0; i < 2; i++ {
		go s.worker()
	}
}

func (s *server) register() {
	sdk.HandleFromVoid[shared.Request](s.bus, "inference.start", func(_ *sdk.Conn, id string, req shared.Request, from wire.Sender) error {
		return s.start(id, req, from)
	})
	sdk.HandleFromVoid[cancelReq](s.bus, "inference.cancel", func(_ *sdk.Conn, _ string, req cancelReq, from wire.Sender) error { s.cancel(req.ID, from); return nil })
	sdk.HandleFromVoid[saveReq](s.bus, "config.save", func(_ *sdk.Conn, id string, req saveReq, from wire.Sender) error { return s.save(id, req, from) })
	sdk.HandleFromVoid[selectReq](s.bus, "config.select", func(_ *sdk.Conn, id string, req selectReq, from wire.Sender) error {
		return s.selectDefault(id, req, from)
	})
	sdk.HandleFromVoid[testReq](s.bus, "provider.test", func(_ *sdk.Conn, id string, req testReq, from wire.Sender) error { return s.test(id, req, from) })
}

func (s *server) publicStateLocked() state {
	out := state{Default: s.cfg.Default}
	for _, c := range s.cfg.Connections {
		ok, detail := executableStatus(c)
		out.Connections = append(out.Connections, publicConnection{ID: c.ID, Name: c.Name, Adapter: c.Adapter, BaseURL: c.BaseURL, Model: c.Model, HasCredential: c.APIKey != "", Available: ok, Detail: detail})
	}
	return out
}
func (s *server) publicState() state            { s.mu.Lock(); defer s.mu.Unlock(); return s.publicStateLocked() }
func (s *server) publish()                      { st := s.publicState(); s.states.Mutate(func(v *state) { *v = st }) }
func recipient(from wire.Sender) wire.Recipient { return wire.Recipient{InstanceID: from.InstanceID} }
func (s *server) emit(from wire.Sender, kind, id string, payload map[string]any, bulk bool) {
	payload["id"] = id
	if bulk {
		_ = s.bus.EmitToBulk(recipient(from), kind, payload)
	} else {
		_ = s.bus.EmitTo(recipient(from), kind, payload)
	}
}
func (s *server) fail(from wire.Sender, id, code, msg string) {
	s.emit(from, "inference.error", id, map[string]any{"code": code, "msg": msg}, false)
}

func (s *server) start(id string, req shared.Request, from wire.Sender) error {
	if !allowedCallers[from.AppID] {
		s.fail(from, id, "forbidden", "app is not allowed to use inference")
		return nil
	}
	if id == "" {
		s.fail(from, id, "bad_request", "request id is required")
		return nil
	}
	if req.MaxOutputTokens < 0 || req.MaxOutputTokens > 8192 {
		s.fail(from, id, "bad_request", "max_output_tokens must be between 0 and 8192")
		return nil
	}
	n := 0
	for _, p := range req.Input {
		if p.Type != "text" {
			s.fail(from, id, "bad_request", "only text input parts are supported")
			return nil
		}
		n += len(p.Text)
	}
	if n == 0 {
		s.fail(from, id, "bad_request", "input is empty")
		return nil
	}
	if n > maxInputBytes {
		s.fail(from, id, "too_large", "input exceeds 1 MiB")
		return nil
	}
	s.mu.Lock()
	if s.cfg.Default == "" {
		s.mu.Unlock()
		s.fail(from, id, "not_configured", "choose an AI provider in Settings")
		return nil
	}
	var selected connection
	for _, c := range s.cfg.Connections {
		if c.ID == s.cfg.Default {
			selected = c
		}
	}
	key := from.InstanceID + "\x00" + id
	if selected.ID == "" {
		s.mu.Unlock()
		s.fail(from, id, "not_configured", "selected provider is unavailable")
		return nil
	}
	if s.activeBySource[from.InstanceID] {
		s.mu.Unlock()
		s.fail(from, id, "busy", "this app already has an inference request")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	j := &job{id: id, from: from, req: req, conn: selected, ctx: ctx, cancel: cancel}
	select {
	case s.queue <- j:
		s.jobs[key] = j
		s.activeBySource[from.InstanceID] = true
		s.mu.Unlock()
		s.emit(from, "inference.start_ok", id, map[string]any{}, false)
	default:
		s.mu.Unlock()
		cancel()
		s.fail(from, id, "busy", "inference queue is full")
	}
	return nil
}
func (s *server) worker() {
	for j := range s.queue {
		r, err := runProvider(j.ctx, j.conn, j.req)
		key := j.from.InstanceID + "\x00" + j.id
		s.mu.Lock()
		delete(s.jobs, key)
		delete(s.activeBySource, j.from.InstanceID)
		s.mu.Unlock()
		j.cancel()
		if err == nil && len(r.Text) > 64*1024 {
			err = providerError{"too_large", "provider output exceeds 64 KiB"}
		}
		if err != nil {
			pe := providerError{"internal", err.Error()}
			if v, ok := err.(providerError); ok {
				pe = v
			}
			s.fail(j.from, j.id, pe.code, pe.msg)
			continue
		}
		s.emit(j.from, "inference.result", j.id, map[string]any{"text": r.Text, "provider": r.Provider, "model": r.Model, "elapsed_ms": r.ElapsedMS, "usage": r.Usage}, true)
	}
}
func (s *server) cancel(id string, from wire.Sender) {
	key := from.InstanceID + "\x00" + id
	s.mu.Lock()
	j := s.jobs[key]
	s.mu.Unlock()
	if j != nil {
		j.cancel()
	}
	s.emit(from, "inference.cancel_ok", id, map[string]any{}, false)
}

func (s *server) save(id string, req saveReq, from wire.Sender) error {
	if from.AppID != settingsAppID {
		return nil
	}
	s.mu.Lock()
	found := false
	for i := range s.cfg.Connections {
		if s.cfg.Connections[i].ID == req.ID {
			found = true
			c := &s.cfg.Connections[i]
			c.BaseURL = strings.TrimSpace(req.BaseURL)
			c.Model = strings.TrimSpace(req.Model)
			if req.APIKey != nil {
				c.APIKey = *req.APIKey
			}
		}
	}
	if !found {
		s.mu.Unlock()
		s.emit(from, "config.error", id, map[string]any{"msg": "unknown connection"}, false)
		return nil
	}
	err := saveConfig(s.cfg)
	s.mu.Unlock()
	if err != nil {
		s.emit(from, "config.error", id, map[string]any{"msg": err.Error()}, false)
		return nil
	}
	s.emit(from, "config.saved", id, map[string]any{}, false)
	s.publish()
	return nil
}
func (s *server) selectDefault(id string, req selectReq, from wire.Sender) error {
	if from.AppID != settingsAppID {
		return nil
	}
	s.mu.Lock()
	if req.ID != "" {
		found := false
		for _, c := range s.cfg.Connections {
			if c.ID == req.ID {
				found = true
			}
		}
		if !found {
			s.mu.Unlock()
			return nil
		}
	}
	s.cfg.Default = req.ID
	err := saveConfig(s.cfg)
	s.mu.Unlock()
	if err != nil {
		s.emit(from, "config.error", id, map[string]any{"msg": err.Error()}, false)
		return nil
	}
	s.emit(from, "config.saved", id, map[string]any{}, false)
	s.publish()
	return nil
}
func (s *server) test(id string, req testReq, from wire.Sender) error {
	if from.AppID != settingsAppID {
		return nil
	}
	s.mu.Lock()
	var c connection
	for _, v := range s.cfg.Connections {
		if v.ID == req.ID {
			c = v
		}
	}
	s.mu.Unlock()
	if c.ID == "" {
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := runProvider(ctx, c, shared.Request{Instructions: "Reply with exactly: OK", Input: []shared.Part{{Type: "text", Text: "Connection test"}}, MaxOutputTokens: 16})
		if err != nil {
			s.emit(from, "provider.test_result", id, map[string]any{"connection_id": c.ID, "ok": false, "error": err.Error()}, false)
		} else {
			s.emit(from, "provider.test_result", id, map[string]any{"connection_id": c.ID, "ok": true, "detail": strings.TrimSpace(r.Text)}, false)
		}
	}()
	return nil
}
func onInstanceGone(_ *sdk.Conn, _ string, instanceID string) {
	if service == nil {
		return
	}
	service.states.ForgetSubscriber(instanceID)
	service.mu.Lock()
	var jobs []*job
	for _, j := range service.jobs {
		if j.from.InstanceID == instanceID {
			jobs = append(jobs, j)
		}
	}
	service.mu.Unlock()
	for _, j := range jobs {
		j.cancel()
	}
}
