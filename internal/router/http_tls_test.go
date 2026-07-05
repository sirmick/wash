package router

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/tlsutil"
)

// freePort grabs an ephemeral loopback port and releases it, returning
// the address for a listener to rebind. The router uses SO_REUSEADDR, so
// the brief gap is safe for a test.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// getWithRetry polls url until the just-started server answers or the
// deadline passes.
func getWithRetry(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never succeeded: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// selfSignedTLS mints a throwaway self-signed cert and returns a server
// tls.Config for it, as the router's zero-config HTTPS default does.
func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	certPEM, keyPEM, err := tlsutil.GenerateSelfSigned([]string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// TestHTTPServerServesTLS is the regression for issue #11: when a TLS
// config is attached, HTTPServer.Run terminates HTTPS itself (self-signed
// by default) so browser secure-context features work with no proxy. A
// plain-HTTP GET to the same port must NOT succeed.
func TestHTTPServerServesTLS(t *testing.T) {
	addr := freePort(t)
	r := NewRouter(Config{Listen: addr, NoSession: true}, NewRegistry(),
		func(f string, a ...any) { t.Logf("router: "+f, a...) })
	s := NewHTTPServer(r, nil) // nil assets ⇒ fallback index at "/"
	s.TLS = selfSignedTLS(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after ctx cancel")
		}
	}()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp := getWithRetry(t, client, "https://"+addr+"/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("https GET / status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "wash") {
		t.Errorf("unexpected body: %q", body)
	}

	// A plain-HTTP request to a TLS listener must NOT be served the shell
	// — it gets the issue-#18 redirect to https:// (which this client then
	// fails to follow: the self-signed cert doesn't verify). Either way
	// it's never a 200, proving we're really speaking HTTPS.
	plain := &http.Client{Timeout: time.Second}
	if presp, err := plain.Get("http://" + addr + "/"); err == nil {
		presp.Body.Close()
		if presp.StatusCode == http.StatusOK {
			t.Error("plain HTTP GET was served (200) by the TLS listener")
		}
	}
}

// TestHTTPRedirectsToHTTPS is the regression for issue #18: a plain-HTTP
// request to the HTTPS listener redirects to the https:// URL of the
// same host — path and query (the logged ?token=… URL) intact — instead
// of Go's opaque 400 "Client sent an HTTP request to an HTTPS server".
func TestHTTPRedirectsToHTTPS(t *testing.T) {
	addr := freePort(t)
	r := NewRouter(Config{Listen: addr, NoSession: true}, NewRegistry(),
		func(f string, a ...any) { t.Logf("router: "+f, a...) })
	s := NewHTTPServer(r, nil)
	s.TLS = selfSignedTLS(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after ctx cancel")
		}
	}()

	// Don't follow the redirect — the response itself is under test.
	client := &http.Client{
		Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp := getWithRetry(t, client, "http://"+addr+"/?token=sesame")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("plain-HTTP GET status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	want := "https://" + addr + "/?token=sesame"
	if loc := resp.Header.Get("Location"); loc != want {
		t.Errorf("Location = %q, want %q", loc, want)
	}

	// And the same port still terminates HTTPS as before.
	tlsClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	sresp := getWithRetry(t, tlsClient, "https://"+addr+"/")
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("https GET / status = %d, want 200", sresp.StatusCode)
	}
}

// TestHTTPServerServesPlainHTTP locks the --http path: with no TLS config
// the listener speaks plain HTTP.
func TestHTTPServerServesPlainHTTP(t *testing.T) {
	addr := freePort(t)
	r := NewRouter(Config{Listen: addr, NoSession: true}, NewRegistry(),
		func(f string, a ...any) { t.Logf("router: "+f, a...) })
	s := NewHTTPServer(r, nil) // no s.TLS ⇒ plain HTTP

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	resp := getWithRetry(t, &http.Client{Timeout: time.Second}, "http://"+addr+"/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http GET / status = %d, want 200", resp.StatusCode)
	}
}
