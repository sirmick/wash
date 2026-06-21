package login

import (
	"net/http"
	"testing"
)

// The /app/ ingress route forwards to the user's per-router with the
// same SCM_RIGHTS handoff as /ws. These tests cover the routing
// decisions that happen BEFORE the hijack (auth + session count); the
// single-session happy path hijacks the conn (exercised end-to-end by
// the router's TestUnixListenerIngressHandoff and the handoff
// integration test), so it isn't driven through an httptest client here.

// TestAppIngressUnauthed401 — a request without a valid session cookie
// is rejected with 401, never reaching a per-user router. An <iframe>
// can't follow a redirect to the login page, so 401 (not 302) is the
// contract.
func TestAppIngressUnauthed401(t *testing.T) {
	sessions := []Session{{Pid: 1111, UID: 1000, SessID: "s-a", Sock: "/tmp/x.sock"}}
	ts, _, _, _ := pickerTestSetup(t, sessions, nil)

	// A fresh client with no auth cookie.
	resp, err := http.Get(ts.URL + "/app/sometoken/page")
	if err != nil {
		t.Fatalf("GET /app/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthed /app/, got %d", resp.StatusCode)
	}
}

// TestAppIngressNoSession502 — an authed user with no live session has
// no router to proxy to.
func TestAppIngressNoSession502(t *testing.T) {
	ts, client, _, _ := pickerTestSetup(t, nil, nil)
	resp, err := client.Get(ts.URL + "/app/sometoken/page")
	if err != nil {
		t.Fatalf("GET /app/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 for no-session /app/, got %d", resp.StatusCode)
	}
}

// TestAppIngressMultiSession503 — v1 can't map an opaque ingress token
// to one of several sessions, so it declines rather than guess.
func TestAppIngressMultiSession503(t *testing.T) {
	sessions := []Session{
		{Pid: 1111, UID: 1000, SessID: "s-a", Sock: "/tmp/a.sock"},
		{Pid: 2222, UID: 1000, SessID: "s-b", Sock: "/tmp/b.sock"},
	}
	ts, client, _, _ := pickerTestSetup(t, sessions, nil)
	resp, err := client.Get(ts.URL + "/app/sometoken/page")
	if err != nil {
		t.Fatalf("GET /app/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for multi-session /app/, got %d", resp.StatusCode)
	}
}
