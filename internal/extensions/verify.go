// Package extensions is grid-cli's extension manager (ARCH §3.2/§3.3,
// Phase 2): it caches official grid-ext-* binaries under the extension
// cache directory, downloads them from the release mirror on demand,
// verifies every binary's detached Ed25519 release signature against the
// embedded root public key before making it executable, and speaks the
// frozen extcommon IPC contract to the signed subprocess.
package extensions

import (
	"crypto/ed25519"
	"errors"

	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// ReleasePublicKeyB64 is the dev root release public key — pasted verbatim
// from grid-extensions/keys/release-dev.pub (whose private half signs every
// dist binary via `make sign`; CI's production key replaces it through the
// same release-signing path).
const ReleasePublicKeyB64 = "QW7UB3fcnHZBv78JJtwtKkslFNwqGlN2G7Z/ZZr001g="

// ErrBadSignature marks a binary whose detached signature did not verify
// (tampered, corrupted, or unsigned) — it is never executed and never
// cached.
var ErrBadSignature = errors.New("extension signature verification failed")

// releasePublicKey parses the embedded root key once.
var releasePublicKey = func() ed25519.PublicKey {
	pub, err := attest.PublicKeyFromB64(ReleasePublicKeyB64)
	if err != nil {
		panic("extensions: embedded release public key is not valid base64 Ed25519: " + err.Error())
	}
	return pub
}()

// ReleasePub returns the embedded dev root release public key — the same
// key VerifyBinary checks release signatures with, and the one the ext
// response's extSignature must verify against (official extensions sign
// telemetry with the release private half).
func ReleasePub() ed25519.PublicKey { return releasePublicKey }

// VerifyBinary verifies a downloaded binary against its detached .sig
// (raw 64-byte Ed25519 over the file bytes, as `make sign` writes it) using
// the embedded root release key.
func VerifyBinary(bin, sig []byte) error {
	return VerifySignature(releasePublicKey, bin, sig)
}

// VerifySignature is VerifyBinary against an explicit key (the seam the
// tests sign fixtures with).
func VerifySignature(pub ed25519.PublicKey, bin, sig []byte) error {
	if len(sig) != ed25519.SignatureSize || !attest.Verify(pub, bin, sig) {
		return ErrBadSignature
	}
	return nil
}
