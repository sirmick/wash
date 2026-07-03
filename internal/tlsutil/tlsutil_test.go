package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGenerateSelfSigned proves the minted cert parses, is a usable TLS
// keypair, and carries the requested names as SANs (DNS vs IP split).
func TestGenerateSelfSigned(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned([]string{"localhost", "127.0.0.1", "::1", "example.test"})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// DNS SANs
	wantDNS := map[string]bool{"localhost": false, "example.test": false}
	for _, d := range leaf.DNSNames {
		if _, ok := wantDNS[d]; ok {
			wantDNS[d] = true
		}
	}
	for d, seen := range wantDNS {
		if !seen {
			t.Errorf("missing DNS SAN %q (got %v)", d, leaf.DNSNames)
		}
	}
	// IP SANs
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("cert does not validate for 127.0.0.1: %v", err)
	}
	// Long validity so a long-running box doesn't surprise-expire.
	if leaf.NotAfter.Before(time.Now().Add(365 * 24 * time.Hour)) {
		t.Errorf("cert expires too soon: %v", leaf.NotAfter)
	}
}

// TestLoadOrGenerateCachesAndReuses proves the first call mints+persists
// a keypair (key at 0600) and a second call reuses the exact same cert
// instead of minting a fresh one — so a restart keeps the browser's
// one-time trust decision valid.
func TestLoadOrGenerateCachesAndReuses(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "sub", "cert.pem")
	keyPath := filepath.Join(dir, "sub", "key.pem")

	first, err := LoadOrGenerate(certPath, keyPath, t.Logf)
	if err != nil {
		t.Fatalf("first LoadOrGenerate: %v", err)
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("cert not persisted: %v", err)
	}
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key not persisted: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %o, want 0600", perm)
	}

	second, err := LoadOrGenerate(certPath, keyPath, t.Logf)
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Error("second call minted a new cert instead of reusing the cached one")
	}
}

// TestLoadOrGenerateServes proves the cached cert actually terminates a
// TLS handshake for a loopback address it covers.
func TestLoadOrGenerateServes(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrGenerate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), t.Logf)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName: "127.0.0.1",
		RootCAs:    poolFrom(t, cert),
	})
	if err != nil {
		t.Fatalf("tls.Dial validating self-signed cert: %v", err)
	}
	_ = conn.Close()
}

func poolFrom(t *testing.T, cert tls.Certificate) *x509.CertPool {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool
}
