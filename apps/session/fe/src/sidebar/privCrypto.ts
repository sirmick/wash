// Password-handshake crypto for the priv unlock modal. The wire shape
// is fixed: the priv BE's deriveKey + AES-GCM decode expects exactly
// these primitive choices and parameters.
//
// We deliberately do NOT use Web Crypto. Its subtle interface is
// gated to "secure contexts" (HTTPS or http://localhost), and wash is
// routinely accessed via a LAN hostname over plain HTTP. The threat
// model in apps/priv/be/crypto.go calls out "misconfigured non-
// loopback exposure" as something this encryption must defend
// against; noble's pure-JS primitives run anywhere, only depending
// on crypto.getRandomValues (which IS available in insecure contexts).

import { p256 } from '@noble/curves/nist.js';
import { ecdh } from '@noble/curves/abstract/weierstrass.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

// HKDF_INFO MUST match the BE's PrivAEADInfo constant. If you bump
// this on one side, bump it on the other or the handshake silently
// fails with "decrypt_failed".
const HKDF_INFO = 'wash-priv/password/v1';

const p256ecdh = ecdh(p256.Point);

export function b64encode(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s);
}

export function b64decode(s: string): Uint8Array {
  const raw = atob(s);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// encryptPassword runs the FE side of the password handshake:
//   1. Generate an ephemeral FE P-256 keypair.
//   2. Derive the shared secret via ECDH with the BE's pubkey.
//   3. HKDF-SHA256 to a 32-byte AES-256-GCM key with the shared info.
//   4. Encrypt the password bytes with a random 12-byte nonce.
//
// Returns the three wire fields the BE needs to decrypt. Plaintext
// bytes are zeroed before return; JS strings are immutable so the
// password string itself is out of our hands, but the TextEncoder
// copy + derived secrets are all we can touch.
export function encryptPassword(
  password: string,
  bePubRaw: Uint8Array,
): { ciphertext: Uint8Array; fePubKey: Uint8Array; nonce: Uint8Array } {
  // Fresh ephemeral keypair per unlock — nonce reuse across
  // handshakes is structurally impossible because the AES key changes
  // with every keygen.
  const { secretKey } = p256ecdh.keygen();
  const fePubKey = p256ecdh.getPublicKey(secretKey, false); // uncompressed 65b
  // ECDH shared secret. noble emits SEC1 (33b compressed = prefix||X);
  // the BE's crypto/ecdh.ECDH returns just X (32 bytes), so we strip
  // the 1-byte prefix.
  const sharedSEC1 = p256ecdh.getSharedSecret(secretKey, bePubRaw, true);
  const shared = sharedSEC1.slice(1);
  const aesKey = hkdf(sha256, shared, new Uint8Array(0), new TextEncoder().encode(HKDF_INFO), 32);
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const pwBytes = new TextEncoder().encode(password);
  const ct = gcm(aesKey, nonce).encrypt(pwBytes);
  // Best-effort scrub.
  pwBytes.fill(0);
  shared.fill(0);
  aesKey.fill(0);
  secretKey.fill(0);
  return { ciphertext: ct, fePubKey, nonce };
}
