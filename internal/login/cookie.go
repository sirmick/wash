package login

// HMAC-signed session cookies for wash-login.
//
// Cookie payload encodes the authenticated identity (uid + name)
// plus an expiry timestamp; the signing key lives in
// /etc/wash/secret.key on production deployments, or wherever
// --secret-key points for dev / tests.
//
// Format: `<base64url(payload)>.<base64url(hmac-sha256)>`.  The
// payload is a tiny JSON object so we can add fields later without
// breaking the on-wire shape. Constant-time verification on every
// request — never trust the unsigned payload.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CookieName is the HTTP cookie name wash-login mints + reads.
const CookieName = "wash_session"

// defaultSecretPath is where Linux installs keep the HMAC key.
// Mode 0600, owned by the wash-system uid. Tests / dev override
// via --secret-key.
const defaultSecretPath = "/etc/wash/secret.key"

// MinSecretLen is the smallest acceptable key size. Anything shorter
// than 256 bits gets refused; generated keys are 256 bits exactly.
const MinSecretLen = 32

// DefaultCookieTTL is how long a freshly-minted cookie is valid for.
// The browser stores it until expiry; on each request the server
// verifies the embedded expiry hasn't elapsed.
const DefaultCookieTTL = 24 * time.Hour

// Payload is what the cookie carries between requests. Encoded as
// JSON inside the cookie body, then HMAC-signed.
type Payload struct {
	UID     uint32 `json:"uid"`
	Name    string `json:"name"`
	Expires int64  `json:"exp"` // unix seconds
}

// Expired returns true if the payload's expiry is in the past.
func (p Payload) Expired() bool {
	return time.Now().Unix() > p.Expires
}

// Signer turns a Payload into a signed cookie value and back.
// One instance per wash-login process, key loaded at startup.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer whose HMAC key is read from path. If
// path is empty, defaultSecretPath is used. If the file doesn't
// exist and allowGenerate is true, a fresh key is written there
// (mode 0600, parent dir created with 0755). Returns an error if
// the loaded key is too short.
func NewSigner(path string, allowGenerate bool) (*Signer, error) {
	if path == "" {
		path = defaultSecretPath
	}
	key, err := loadOrGenerateKey(path, allowGenerate)
	if err != nil {
		return nil, err
	}
	if len(key) < MinSecretLen {
		return nil, fmt.Errorf("secret key at %s is too short (%d bytes; need >= %d)", path, len(key), MinSecretLen)
	}
	return &Signer{key: key}, nil
}

func loadOrGenerateKey(path string, allowGenerate bool) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !allowGenerate {
		return nil, fmt.Errorf("secret key %s missing and --secret-generate not set", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	key := make([]byte, MinSecretLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}

// Sign returns a string cookie value carrying the payload, signed
// with the Signer's key. The result is `<base64url(payload)>.<base64url(hmac)>`.
func (s *Signer) Sign(p Payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	encBody := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encBody))
	encSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encBody + "." + encSig, nil
}

// Verify parses and validates a signed cookie. Returns the decoded
// Payload on success, or an error describing the failure (malformed,
// bad signature, expired). Errors do NOT distinguish "bad signature"
// from "expired" in their message text — callers should treat all
// failures the same way ("invalid session, please log in") to avoid
// leaking timing / oracle distinctions about why a cookie was rejected.
func (s *Signer) Verify(cookie string) (Payload, error) {
	var zero Payload
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return zero, errors.New("malformed cookie")
	}
	encBody, encSig := parts[0], parts[1]
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encBody))
	expected := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil {
		return zero, errors.New("malformed cookie")
	}
	if !hmac.Equal(expected, got) {
		return zero, errors.New("invalid signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(encBody)
	if err != nil {
		return zero, errors.New("malformed cookie")
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return zero, errors.New("malformed cookie")
	}
	if p.Expired() {
		return zero, errors.New("expired cookie")
	}
	return p, nil
}
