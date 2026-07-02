package login

import (
	"net/http"
	"testing"
)

// TestRootDeadSessionRedirects — (REVIEW-RECONNECT H4) a "/?s=<dead>" request
// must not serve the shell (which would loop "reconnecting…" forever against
// a 404ing /ws/s/<dead> while /auth/check stays 204). handleRoot validates
// the named session against the live list and bounces a dead one to the
// picker with a legible message.
func TestRootDeadSessionRedirects(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha", Name: "mick", Sock: "/run/wash/1000/sessions/s-alpha.sock"},
	}
	ts, client, _ := newLogoutTestServer(t, sessions)

	resp, err := client.Get(ts.URL + "/?s=bogus")
	if err != nil {
		t.Fatalf("GET /?s=bogus: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: got %d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/sessions?err=session+ended" {
		t.Errorf("redirect: got %q want /sessions?err=session+ended", loc)
	}
}

// TestRootLiveSessionServesShell — a "/?s=<live>" request serves the shell
// page (200), so the validation doesn't over-redirect a healthy attach.
func TestRootLiveSessionServesShell(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-alpha", Name: "mick", Sock: "/run/wash/1000/sessions/s-alpha.sock"},
	}
	ts, client, _ := newLogoutTestServer(t, sessions)

	resp, err := client.Get(ts.URL + "/?s=s-alpha")
	if err != nil {
		t.Fatalf("GET /?s=s-alpha: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200 (shell served for a live session)", resp.StatusCode)
	}
}
