// Package tlsutil mints and caches the self-signed certificate wash's
// HTTP fronts (internal/router, internal/login) serve by default.
//
// It exists because both fronts want the same behaviour: HTTPS out of
// the box with no operator setup, so browser secure-context features —
// clipboard read/write, service workers — work without a reverse proxy.
// The certificate is self-signed, so a browser shows a one-time trust
// warning; that is the accepted trade for zero-config TLS on a LAN /
// tunnelled box. Operators who front wash with a real terminator (nginx,
// Caddy, Tailscale-serve) turn TLS off with --http.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// certValidity is how long a freshly-minted self-signed cert is good
// for. Ten years: the cert is regenerated only if the operator deletes
// the cache, so a long life avoids a surprise expiry on a long-running
// box, while a self-signed cert already carries no CA trust to protect.
const certValidity = 10 * 365 * 24 * time.Hour

// HTTP1Only returns the protocol set wash's HTTP fronts must serve:
// HTTP/1.1 with HTTP/2 left off. Go's net/http enables h2 automatically
// on any TLS listener, but an h2 stream's ResponseWriter does not
// implement http.Hijacker — and every wash front hijacks: the router's
// WebSocket upgrades and ingress proxying, wash-login's /ws fd-handoff
// and /app ingress. A browser that negotiated h2 via ALPN then hit
// "hijacker not supported" on all of those paths.
func HTTP1Only() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	return p
}

// LoadOrGenerate returns a TLS certificate for a wash HTTP front.
//
// If both certPath and keyPath exist and parse as a valid keypair, that
// pair is loaded and returned (so a restart keeps the same cert and the
// browser's one-time trust decision sticks). Otherwise a fresh
// self-signed cert covering localhost + the machine's hostname and
// routable IPs is minted and persisted — the key at mode 0600, the cert
// at 0644 — under the parent dir (created on demand). A persist failure
// is non-fatal: the in-memory cert is still returned so the server can
// start, and logf (if non-nil) reports it.
func LoadOrGenerate(certPath, keyPath string, logf func(string, ...any)) (tls.Certificate, error) {
	if certPath != "" && keyPath != "" {
		if fileExists(certPath) && fileExists(keyPath) {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err == nil {
				return cert, nil
			}
			// A corrupt/mismatched cached pair shouldn't wedge startup:
			// fall through and regenerate over it.
			if logf != nil {
				logf("tls: cached keypair %s/%s unusable (%v); regenerating", certPath, keyPath, err)
			}
		}
	}

	certPEM, keyPEM, err := GenerateSelfSigned(defaultHosts())
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	if certPath != "" && keyPath != "" {
		if perr := persist(certPath, keyPath, certPEM, keyPEM); perr != nil && logf != nil {
			logf("tls: could not persist self-signed cert to %s (%v); using in-memory cert", certPath, perr)
		}
	}
	return cert, nil
}

// GenerateSelfSigned mints an in-memory self-signed certificate and
// private key (PEM-encoded) covering hosts. hosts entries that parse as
// IPs become IP SANs; the rest become DNS SANs. An empty hosts slice is
// valid but yields a cert good for no name — callers pass defaultHosts().
func GenerateSelfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("serial: %w", err)
	}
	// NotBefore a minute in the past absorbs small clock skew between the
	// server and a connecting browser (a fresh cert that isn't yet valid
	// trips NET::ERR_CERT_DATE_INVALID).
	notBefore := time.Now().Add(-time.Minute)
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "wash self-signed"},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// defaultHosts is the SAN set for a freshly-minted cert: the loopback
// names/addresses every local access uses, plus the machine's hostname
// and its routable IPs so a LAN peer reaching the box by name or address
// still validates against the SANs.
func defaultHosts() []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				hosts = append(hosts, ipnet.IP.String())
			}
		}
	}
	return hosts
}

// persist writes the cert (0644) and key (0600) to disk, creating the
// parent dir(s) on demand. The key is written first-to-a-temp then
// renamed so a concurrent reader never sees a half-written key.
func persist(certPath, keyPath string, certPEM, keyPEM []byte) error {
	for _, dir := range []string{filepath.Dir(certPath), filepath.Dir(keyPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tls-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
