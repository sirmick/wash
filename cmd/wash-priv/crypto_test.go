package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// TestHandshakeRoundtrip is the canonical confidence check for the
// password handshake: encrypt FE-side with a fresh ephemeral key,
// decrypt BE-side via the published Decrypt method. The FE side here
// mirrors what the browser will do — ECDH-P256, HKDF-SHA256 with
// info "wash-priv/password/v1", AES-256-GCM with 12-byte nonce.
//
// If this test fails after a Decrypt-side edit, the FE bundle in
// web/apps/priv/ likely needs a parallel change.
func TestHandshakeRoundtrip(t *testing.T) {
	hs, bePubBytes, err := NewHandshake()
	if err != nil {
		t.Fatalf("NewHandshake: %v", err)
	}
	// Simulated FE side.
	curve := ecdh.P256()
	bePub, err := curve.NewPublicKey(bePubBytes)
	if err != nil {
		t.Fatalf("import BE pub: %v", err)
	}
	fePriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("FE keygen: %v", err)
	}
	shared, err := fePriv.ECDH(bePub)
	if err != nil {
		t.Fatalf("FE ECDH: %v", err)
	}
	key, err := deriveKey(shared)
	if err != nil {
		t.Fatalf("FE HKDF: %v", err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	pw := []byte("hunter2")
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	ct := aead.Seal(nil, nonce, pw, nil)

	// BE-side decrypt.
	out, err := hs.Decrypt(ct, fePriv.PublicKey().Bytes(), nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(out, pw) {
		t.Fatalf("decrypted password mismatch: got %q want %q", out, pw)
	}
}

func TestDecryptRejectsBadNonceLen(t *testing.T) {
	hs, _, _ := NewHandshake()
	_, err := hs.Decrypt([]byte("x"), []byte("y"), []byte("toolong-nonce"))
	if err == nil {
		t.Fatal("expected error for bad nonce length")
	}
}
