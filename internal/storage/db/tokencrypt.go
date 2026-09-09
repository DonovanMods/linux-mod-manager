package db

// tokencrypt.go is the cipher half of #79: how a stored API key is turned
// into the bytes that land in auth_tokens.token_data, and back.
//
// ENVELOPE. A row's token_data is
//
//	"lmm1" || nonce(12) || AES-256-GCM ciphertext(+16-byte tag)
//
// The ASCII magic is a version marker AND the legacy discriminator: a row
// that does not start with it was written by a pre-#79 lmm and is plaintext
// (migrateTokenEncryption re-encrypts those in place on open). Bumping the
// magic is how a future format change stays distinguishable from both.
//
// The nonce is fresh crypto/rand bytes for EVERY write - GCM's one
// non-negotiable requirement - and the associated data is the source id, so
// a ciphertext lifted from one row into another fails to open rather than
// silently authenticating the wrong source with the wrong key.
//
// THREAT MODEL. This protects a database that leaves the machine: copied,
// synced, backed up, or attached to a bug report - the WAL included. It
// does NOT protect against a same-user local attacker, who can read the key
// file just as easily as the database. See docs/security.md.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	// tokenEnvelopeMagic prefixes every encrypted token_data blob. Four
	// ASCII bytes no plaintext API key realistically starts with, and a
	// version number for the format that follows it.
	tokenEnvelopeMagic = "lmm1"
	// tokenKeySize is AES-256's key length, and therefore the exact size of
	// the key file.
	tokenKeySize = 32
	// tokenNonceSize is GCM's standard nonce length.
	tokenNonceSize = 12
	// tokenFingerprintLen is how much of a key's SHA-256 a status surface
	// shows: enough to tell two keys apart, far too little to attack.
	tokenFingerprintLen = 8
)

// newTokenKey returns a fresh 32-byte key from the system CSPRNG.
func newTokenKey() ([]byte, error) {
	key := make([]byte, tokenKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating a token key: %w", err)
	}
	return key, nil
}

// aeadFor builds the AES-256-GCM AEAD for key.
func aeadFor(key []byte) (cipher.AEAD, error) {
	if len(key) != tokenKeySize {
		return nil, fmt.Errorf("token key must be %d bytes, got %d", tokenKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building the token cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("building the token cipher: %w", err)
	}
	return aead, nil
}

// sealToken encrypts plaintext for sourceID and returns the envelope to
// store. sourceID is the associated data, not part of the ciphertext: it is
// already the row's primary key, and binding it means a row's bytes only
// open under the source they were written for.
func sealToken(key []byte, sourceID, plaintext string) ([]byte, error) {
	aead, err := aeadFor(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, tokenNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating a token nonce: %w", err)
	}
	envelope := make([]byte, 0, len(tokenEnvelopeMagic)+tokenNonceSize+len(plaintext)+aead.Overhead())
	envelope = append(envelope, tokenEnvelopeMagic...)
	envelope = append(envelope, nonce...)
	return aead.Seal(envelope, nonce, []byte(plaintext), []byte(sourceID)), nil
}

// errNotAnEnvelope reports a blob that is not in the lmm1 format at all -
// i.e. a legacy plaintext row. Callers that can handle plaintext branch on
// it; openToken never returns it for a blob that HAS the magic.
var errNotAnEnvelope = errors.New("not an encrypted token envelope")

// isTokenEnvelope reports whether blob was written by sealToken. A blob
// that fails this is a pre-#79 plaintext row.
func isTokenEnvelope(blob []byte) bool {
	return len(blob) >= len(tokenEnvelopeMagic)+tokenNonceSize && string(blob[:len(tokenEnvelopeMagic)]) == tokenEnvelopeMagic
}

// openToken decrypts an envelope written by sealToken for sourceID.
//
// Every failure below the magic check - a wrong key, a tampered ciphertext,
// a row copied from another source - is the same GCM authentication failure
// and is deliberately not distinguished: nothing useful can be said about
// which, and saying more would only help an attacker probe.
func openToken(key []byte, sourceID string, blob []byte) (string, error) {
	if !isTokenEnvelope(blob) {
		return "", errNotAnEnvelope
	}
	aead, err := aeadFor(key)
	if err != nil {
		return "", err
	}
	nonce := blob[len(tokenEnvelopeMagic) : len(tokenEnvelopeMagic)+tokenNonceSize]
	ciphertext := blob[len(tokenEnvelopeMagic)+tokenNonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(sourceID))
	if err != nil {
		return "", fmt.Errorf("decrypting the stored credential: %w", err)
	}
	return string(plaintext), nil
}

// TokenFingerprint returns the first 8 hex digits of a key's SHA-256 - the
// only thing a status surface (`lmm auth status`, GET /api/v1/auth, the web
// UI's Setup page) is allowed to show of a stored credential. It identifies
// a key well enough to answer "is this still the one I pasted?" and to tell
// a stored key apart from one in the environment, and reveals nothing that
// helps recover it. An empty key has no fingerprint.
func TokenFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:tokenFingerprintLen]
}
