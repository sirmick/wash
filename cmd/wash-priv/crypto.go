package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/hkdf"
)

// The password-handshake crypto module.
//
// Threat model: the password travels FE→BE inside an app_msg frame
// that flows over the WebSocket and through the router. Encrypting
// the payload with a session key shared between BE and FE makes the
// router and the WS frame opaque to anyone reading them — DevTools,
// extensions, accidental WS logging, a misconfigured non-loopback
// exposure. It does NOT defend against a browser-side attacker with
// JS execution in the FE (they have the FE private key).
//
// Cipher choice: ECDH-P256 → HKDF-SHA256 → AES-256-GCM. Picked for
// universal Web Crypto support (X25519 + ChaCha20-Poly1305 would be
// nicer but Safari lacks both). Per-handshake fresh BE keypair, so
// nonce reuse across handshakes is structurally impossible. We
// transmit the BE public key in the SubjectPublicKeyInfo format
// because Web Crypto's importKey for ECDH only accepts SPKI or JWK.

// PrivAEADInfo is the HKDF context separator — bind the derived key
// to wash-priv's password channel so the same X25519 key reused in
// another context (it shouldn't be, but defense in depth) doesn't
// also unlock here.
const PrivAEADInfo = "wash-priv/password/v1"

// aeadKeySize and aeadNonceSize are AES-256-GCM's key and nonce
// sizes. Defined as constants for readability.
const (
	aeadKeySize   = 32 // AES-256
	aeadNonceSize = 12 // GCM standard
)

// Handshake holds the BE side of one password unlock. We generate a
// fresh ephemeral P-256 keypair per unlock and discard it as soon
// as the unlock completes (success or fail).
type Handshake struct {
	priv *ecdh.PrivateKey
}

// NewHandshake mints a fresh BE keypair. Returns the keypair carrier
// and the encoded public key the FE should consume. The wire format
// is the X9.62 uncompressed point form — what ecdh.PublicKey.Bytes()
// returns for P-256 — which the FE imports via Web Crypto as "raw".
func NewHandshake() (*Handshake, []byte, error) {
	curve := ecdh.P256()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("p256 keygen: %w", err)
	}
	return &Handshake{priv: priv}, priv.PublicKey().Bytes(), nil
}

// Decrypt opens the FE-encrypted password ciphertext using the
// handshake's BE private key plus the FE public key sent alongside.
// fePubKey is uncompressed-point P-256 (Web Crypto exportKey "raw").
// Nonce must be 12 bytes.
//
// Returns a []byte the caller is responsible for zeroing once it's
// no longer needed.
func (h *Handshake) Decrypt(ciphertext, fePubKey, nonce []byte) ([]byte, error) {
	if h == nil || h.priv == nil {
		return nil, fmt.Errorf("handshake not initialized")
	}
	if len(fePubKey) == 0 {
		return nil, fmt.Errorf("fe_pubkey is empty")
	}
	if len(nonce) != aeadNonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes (got %d)", aeadNonceSize, len(nonce))
	}
	curve := ecdh.P256()
	fePub, err := curve.NewPublicKey(fePubKey)
	if err != nil {
		return nil, fmt.Errorf("fe_pubkey: %w", err)
	}
	shared, err := h.priv.ECDH(fePub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	key, err := deriveKey(shared)
	if err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	defer wipe(key)
	defer wipe(shared)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	pt, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("aead open: %w", err)
	}
	return pt, nil
}

// deriveKey runs HKDF-SHA256 over the ECDH shared secret with our
// fixed info string. Salt is nil — we don't have a prior shared
// secret to use as salt, and HKDF without salt is still safe.
//
// MUST match the FE's HKDF parameters exactly: hash SHA-256, salt
// empty, info bytes-of("wash-priv/password/v1"), length 32.
func deriveKey(shared []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, shared, nil, []byte(PrivAEADInfo))
	key := make([]byte, aeadKeySize)
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// wipe zeroes a byte slice. Best-effort — Go's GC may have moved a
// copy elsewhere by the time we wipe — but it shrinks the in-memory
// dwell time of secrets to the minimum we can express in source.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
