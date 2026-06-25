package login

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signerForTest builds a Signer over a freshly-generated key in a
// temp dir; no /etc filesystem permissions required.
func signerForTest(t *testing.T) *Signer {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	s, err := NewSigner(path, true)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestCookieRoundTrip(t *testing.T) {
	s := signerForTest(t)
	original := Payload{UID: 1000, Name: "mick", Expires: time.Now().Add(1 * time.Hour).Unix()}
	cookie, err := s.Sign(original)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := s.Verify(cookie)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != original {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, original)
	}
}

func TestCookieRejectsExpired(t *testing.T) {
	s := signerForTest(t)
	expired := Payload{UID: 1000, Name: "mick", Expires: time.Now().Add(-1 * time.Hour).Unix()}
	cookie, err := s.Sign(expired)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := s.Verify(cookie); err == nil {
		t.Fatalf("Verify accepted expired cookie")
	}
}

func TestCookieRejectsTampered(t *testing.T) {
	s := signerForTest(t)
	cookie, _ := s.Sign(Payload{UID: 1000, Name: "mick", Expires: time.Now().Add(1 * time.Hour).Unix()})
	// Flip a character in the payload (body part), keep signature.
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed cookie: %s", cookie)
	}
	tampered := parts[0] + "X." + parts[1]
	if _, err := s.Verify(tampered); err == nil {
		t.Fatalf("Verify accepted tampered cookie")
	}
}

func TestCookieRejectsWrongKey(t *testing.T) {
	s1 := signerForTest(t)
	s2 := signerForTest(t)
	cookie, _ := s1.Sign(Payload{UID: 1000, Name: "mick", Expires: time.Now().Add(1 * time.Hour).Unix()})
	if _, err := s2.Verify(cookie); err == nil {
		t.Fatalf("Verify accepted cookie signed by a different key")
	}
}

// newTestServer wires a Server with a testAuth + a fresh Signer and
// returns an httptest.Server you can drive with a cookie-jar client.
func newTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	auth, err := NewTestAuth("alice:hunter2", 1000, 1000, "/bin/sh")
	if err != nil {
		t.Fatalf("NewTestAuth: %v", err)
	}
	srv, err := NewServer(Config{
		Auth:   auth,
		Signer: signerForTest(t),
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := newCookieJar(ts.URL)
	client := &http.Client{
		Jar: jar,
		// Disable auto-redirect so we can assert on 302s.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return ts, client
}

func TestRootRedirectsWhenUnauthed(t *testing.T) {
	ts, client := newTestServer(t)
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status: got %d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location: got %q want /login", loc)
	}
}

func TestLoginFormRenders(t *testing.T) {
	ts, client := newTestServer(t)
	resp, err := client.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<input id=\"u\"") {
		t.Errorf("login form missing username input:\n%s", body)
	}
}

func TestAuthSuccess(t *testing.T) {
	ts, client := newTestServer(t)
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: got %d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location: got %q want /", loc)
	}
	// Cookie should be set.
	var sessCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			sessCookie = c
		}
	}
	if sessCookie == nil {
		t.Fatalf("auth did not set %s cookie; got %+v", CookieName, resp.Cookies())
	}
	if !sessCookie.HttpOnly || sessCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags wrong: HttpOnly=%v SameSite=%v", sessCookie.HttpOnly, sessCookie.SameSite)
	}

	// Follow-up GET / should serve the shell index.html for authed
	// users. The asset only exists when the Makefile has staged the
	// shared shell bundle under internal/shellassets/assets — in test
	// builds it may be missing, in which case handleRoot falls
	// back to a /sessions redirect. Accept either outcome.
	resp2, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / after auth: %v", err)
	}
	defer resp2.Body.Close()
	switch resp2.StatusCode {
	case http.StatusOK:
		// shell index.html served (full build).
	case http.StatusFound:
		if loc := resp2.Header.Get("Location"); loc != "/sessions" {
			t.Errorf("after auth: 302 → %q want /sessions", loc)
		}
	default:
		t.Errorf("after auth: got %d (expected 200 or 302→/sessions)", resp2.StatusCode)
	}
}

func TestAuthFailureRedirectsBack(t *testing.T) {
	ts, client := newTestServer(t)
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"wrong"},
	})
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: got %d want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login?err=") {
		t.Errorf("Location: got %q want /login?err=...", loc)
	}
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			t.Errorf("rejection should not have set a session cookie")
		}
	}
}

// rateLimitedServer builds a test server whose /auth throttle trips
// after maxFails failures, so the lockout path is reachable in a unit
// test without thousands of requests.
func rateLimitedServer(t *testing.T, maxFails int) (*httptest.Server, *http.Client) {
	t.Helper()
	auth, err := NewTestAuth("alice:hunter2", 1000, 1000, "/bin/sh")
	if err != nil {
		t.Fatalf("NewTestAuth: %v", err)
	}
	srv, err := NewServer(Config{
		Auth:         auth,
		Signer:       signerForTest(t),
		Logger:       log.New(io.Discard, "", 0),
		MaxAuthFails: maxFails,
		AuthWindow:   time.Hour,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := newCookieJar(ts.URL)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return ts, client
}

func TestAuthRateLimitLocksOut(t *testing.T) {
	ts, client := rateLimitedServer(t, 3)
	post := func(pw string) *http.Response {
		resp, err := client.PostForm(ts.URL+"/auth", url.Values{
			"user":     []string{"alice"},
			"password": []string{pw},
		})
		if err != nil {
			t.Fatalf("POST /auth: %v", err)
		}
		return resp
	}
	// 3 wrong attempts are each accepted-and-rejected (302 to /login?err).
	for i := 0; i < 3; i++ {
		resp := post("wrong")
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if !strings.HasPrefix(loc, "/login?err=Invalid") {
			t.Fatalf("attempt %d: Location %q, want invalid-credentials redirect", i, loc)
		}
	}
	// 4th attempt is throttled BEFORE auth — even the correct password
	// is rejected with the too-many-attempts redirect + Retry-After.
	resp := post("hunter2")
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "Too+many+attempts") {
		t.Fatalf("4th attempt Location %q, want too-many-attempts redirect", loc)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("throttled response missing Retry-After header")
	}
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			t.Error("throttled attempt must not mint a session cookie")
		}
	}
}

func TestAuthRateLimitResetOnSuccess(t *testing.T) {
	ts, client := rateLimitedServer(t, 3)
	post := func(pw string) *http.Response {
		resp, err := client.PostForm(ts.URL+"/auth", url.Values{
			"user":     []string{"alice"},
			"password": []string{pw},
		})
		if err != nil {
			t.Fatalf("POST /auth: %v", err)
		}
		return resp
	}
	// Two failures, then a success clears the counter.
	post("wrong").Body.Close()
	post("wrong").Body.Close()
	ok := post("hunter2")
	ok.Body.Close()
	// After the reset, two more failures still shouldn't lock out (we'd
	// need 3 consecutive). The next correct login must succeed.
	post("wrong").Body.Close()
	post("wrong").Body.Close()
	resp := post("hunter2")
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("post-reset login Location %q, want / (success)", loc)
	}
}

func TestAuthCheckPreflight(t *testing.T) {
	ts, client := newTestServer(t)

	// Unauthed: 401 with JSON pointing at /login.
	resp, err := client.Get(ts.URL + "/auth/check")
	if err != nil {
		t.Fatalf("GET /auth/check: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed /auth/check = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"login_url":"/login"`) {
		t.Errorf("body %q missing login_url", body)
	}

	// Authenticate, then the preflight should 204.
	authResp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	authResp.Body.Close()
	resp2, err := client.Get(ts.URL + "/auth/check")
	if err != nil {
		t.Fatalf("GET /auth/check after auth: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("authed /auth/check = %d, want 204", resp2.StatusCode)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	ts, client := newTestServer(t)
	// Authenticate first.
	resp, err := client.PostForm(ts.URL+"/auth", url.Values{
		"user":     []string{"alice"},
		"password": []string{"hunter2"},
	})
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	resp.Body.Close()
	// Logout.
	resp2, err := client.PostForm(ts.URL+"/logout", nil)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		t.Errorf("logout status: got %d want 302", resp2.StatusCode)
	}
	// Cookie should be cleared (MaxAge=-1 → SetCookie sends an expired
	// past-date cookie).
	var cleared bool
	for _, c := range resp2.Cookies() {
		if c.Name == CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("logout did not clear %s cookie; got cookies=%v", CookieName, resp2.Cookies())
	}
	// Follow-up GET / should redirect to /login again.
	resp3, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / after logout: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusFound || resp3.Header.Get("Location") != "/login" {
		t.Errorf("after logout: status=%d location=%q want 302→/login",
			resp3.StatusCode, resp3.Header.Get("Location"))
	}
}

// newCookieJar wraps cookiejar.New with our test URL so the client
// re-sends cookies on subsequent requests to the same origin.
func newCookieJar(baseURL string) (http.CookieJar, error) {
	return newSimpleJar(baseURL), nil
}

// simpleJar is a minimal CookieJar implementation that ignores URL
// matching and just stores all cookies for any URL. Plenty for our
// tests which only hit one origin.
type simpleJar struct {
	base    string
	cookies []*http.Cookie
}

func newSimpleJar(base string) *simpleJar { return &simpleJar{base: base} }

func (j *simpleJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	for _, c := range cookies {
		// Replace existing cookie with the same name; drop on MaxAge<0.
		next := make([]*http.Cookie, 0, len(j.cookies))
		for _, ex := range j.cookies {
			if ex.Name != c.Name {
				next = append(next, ex)
			}
		}
		j.cookies = next
		if c.MaxAge < 0 {
			continue
		}
		j.cookies = append(j.cookies, c)
	}
}

func (j *simpleJar) Cookies(u *url.URL) []*http.Cookie {
	out := make([]*http.Cookie, len(j.cookies))
	copy(out, j.cookies)
	return out
}
