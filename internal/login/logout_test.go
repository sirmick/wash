package login

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fakeRegistry is a SessionRegistry whose List returns a fixed set.
type fakeRegistry struct {
	uidSessions map[uint32][]Session
}

func (f *fakeRegistry) List(uid uint32) ([]Session, error) {
	return f.uidSessions[uid], nil
}

// killTrap is a Killer impl that records pid arguments instead of
// signalling.
type killTrap struct {
	mu  sync.Mutex
	got []int
}

func (k *killTrap) kill(pid int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.got = append(k.got, pid)
	return nil
}

func (k *killTrap) pids() []int {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]int, len(k.got))
	copy(out, k.got)
	return out
}

// newLogoutTestServer wires a Server with a fake SessionRegistry and
// a kill-trap. The trap pids are exposed for assertion.
func newLogoutTestServer(t *testing.T, sessions []Session) (*httptest.Server, *http.Client, *killTrap) {
	t.Helper()
	auth, _ := NewTestAuth("alice:hunter2", 1000, 1000, "/bin/sh")
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{1000: sessions}}
	trap := &killTrap{}
	srv, err := NewServer(Config{
		Auth:     auth,
		Signer:   signerForTest(t),
		Logger:   log.New(io.Discard, "", 0),
		Sessions: reg,
		// No Spawner — /ws is 503, but /logout doesn't need it.
		Killer: trap.kill,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar := newSimpleJar(ts.URL)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Authenticate.
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	resp.Body.Close()
	return ts, client, trap
}

func TestLogoutEndSessionTermsTargetedPid(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha", Name: "mick", Sock: "/run/wash/1000/sessions/s-alpha.sock"},
		{Pid: 2222, UID: 1000, SessID: "s-beta", Name: "scratch", Sock: "/run/wash/1000/sessions/s-beta.sock"},
	}
	ts, client, trap := newLogoutTestServer(t, sessions)

	resp, err := client.Get(ts.URL + "/logout?end_session=s-alpha")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
		t.Errorf("status/redirect: got %d %q want 302→/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	pids := trap.pids()
	if len(pids) != 1 || pids[0] != 1111 {
		t.Errorf("kill targets: got %v want [1111]", pids)
	}
}

func TestLogoutEndAllTermsEveryPid(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha", Name: "mick"},
		{Pid: 2222, UID: 1000, SessID: "s-beta", Name: "scratch"},
	}
	ts, client, trap := newLogoutTestServer(t, sessions)

	resp, err := client.Get(ts.URL + "/logout?end_all=true")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()
	pids := trap.pids()
	if len(pids) != 2 {
		t.Fatalf("end_all should signal every session; got %v", pids)
	}
	if !(pids[0] == 1111 && pids[1] == 2222) && !(pids[0] == 2222 && pids[1] == 1111) {
		t.Errorf("kill targets: got %v want both 1111 and 2222", pids)
	}
}

func TestLogoutEndSessionIgnoresUnknownSessid(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha"},
	}
	ts, client, trap := newLogoutTestServer(t, sessions)

	resp, err := client.Get(ts.URL + "/logout?end_session=s-no-such-sess")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()
	if pids := trap.pids(); len(pids) != 0 {
		t.Errorf("unknown sessid should not signal anything; got %v", pids)
	}
	// Cookie should still be cleared regardless.
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("unknown sessid should still clear cookie")
	}
}

func TestLogoutUnauthedDoesNothing(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha"},
	}
	// Build a test server without authenticating first.
	auth, _ := NewTestAuth("alice:hunter2", 1000, 1000, "/bin/sh")
	reg := &fakeRegistry{uidSessions: map[uint32][]Session{1000: sessions}}
	trap := &killTrap{}
	srv, _ := NewServer(Config{
		Auth: auth, Signer: signerForTest(t), Logger: log.New(io.Discard, "", 0),
		Sessions: reg, Killer: trap.kill,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/logout?end_session=s-alpha")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	resp.Body.Close()
	if pids := trap.pids(); len(pids) != 0 {
		t.Errorf("unauthed logout must not signal anything; got %v", pids)
	}
}
