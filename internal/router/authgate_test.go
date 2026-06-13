package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gatedFront builds an HTTPServer whose listener requires token.
func gatedFront(t *testing.T, token string) *httptest.Server {
	t.Helper()
	r := NewRouter(Config{AuthToken: token}, nil, func(string, ...any) {})
	front := httptest.NewServer(NewHTTPServer(r, nil))
	t.Cleanup(front.Close)
	return front
}

// noRedirectClient returns a client that surfaces 3xx instead of
// following them, so we can assert on the redirect itself.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestTokenGateRejectsWithoutToken(t *testing.T) {
	front := gatedFront(t, "s3cret")
	c := noRedirectClient()

	// Root → 401 token-prompt page.
	resp, err := c.Get(front.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET / status = %d, want 401", resp.StatusCode)
	}

	// /ws upgrade without token → 401, no upgrade.
	resp, err = c.Get(front.URL + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /ws status = %d, want 401", resp.StatusCode)
	}
}

func TestTokenGateStampsCookieAndStrips(t *testing.T) {
	front := gatedFront(t, "s3cret")
	c := noRedirectClient()

	resp, err := c.Get(front.URL + "/?token=s3cret")
	if err != nil {
		t.Fatalf("GET /?token=: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET /?token= status = %d, want 302", resp.StatusCode)
	}
	// Redirect target must have dropped the token query.
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "token") {
		t.Errorf("redirect Location %q still carries the token", loc)
	}
	// And it must have set the wash_router cookie to the token.
	var set *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == routerCookieName {
			set = ck
		}
	}
	if set == nil {
		t.Fatalf("no %s cookie set; got %+v", routerCookieName, resp.Cookies())
	}
	if set.Value != "s3cret" || !set.HttpOnly || set.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie wrong: value=%q HttpOnly=%v SameSite=%v", set.Value, set.HttpOnly, set.SameSite)
	}
}

func TestTokenGateAcceptsCookie(t *testing.T) {
	front := gatedFront(t, "s3cret")
	c := noRedirectClient()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: routerCookieName, Value: "s3cret"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET / with cookie: %v", err)
	}
	resp.Body.Close()
	// assets is nil ⇒ handleRoot serves the fallback index (200).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / with valid cookie = %d, want 200", resp.StatusCode)
	}
}

func TestTokenGateWrongTokenRejected(t *testing.T) {
	front := gatedFront(t, "s3cret")
	c := noRedirectClient()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: routerCookieName, Value: "wrong"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong cookie = %d, want 401", resp.StatusCode)
	}
}

func TestTokenGateDisabledWhenEmpty(t *testing.T) {
	front := gatedFront(t, "") // empty token ⇒ gate off
	resp, err := noRedirectClient().Get(front.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ungated GET / = %d, want 200", resp.StatusCode)
	}
}

func TestAuthCheckPreflight(t *testing.T) {
	front := gatedFront(t, "s3cret")
	c := noRedirectClient()

	// No token → 401 with JSON body.
	resp, err := c.Get(front.URL + "/auth/check")
	if err != nil {
		t.Fatalf("GET /auth/check: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed /auth/check = %d, want 401", resp.StatusCode)
	}

	// With cookie → 204.
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/auth/check", nil)
	req.AddCookie(&http.Cookie{Name: routerCookieName, Value: "s3cret"})
	resp, err = c.Do(req)
	if err != nil {
		t.Fatalf("GET /auth/check with cookie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("authed /auth/check = %d, want 204", resp.StatusCode)
	}
}
