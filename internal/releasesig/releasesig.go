// Package releasesig signs and verifies an lmm release's checksums.txt with
// ed25519 (#569). The release workflow signs with the private key held in
// an Actions secret; lmm self-update verifies with the public key compiled
// into the binary. Standard library only.
//
// Keys and signatures travel as standard base64: a private key is its
// 32-byte seed, a public key its 32 bytes, a signature its 64 bytes.
package releasesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// signingContext is prepended to everything signed, so a release signature can
// never be replayed as a signature over anything else made with the key.
const signingContext = "lmm release checksums v1\n"

// ErrBadSignature is returned when a signature does not verify.
var ErrBadSignature = errors.New("release signature does not verify")

// Generate returns a new key pair, base64-encoded: the private key's seed
// and the public key.
func Generate() (privateB64, publicB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating key: %w", err)
	}
	enc := base64.StdEncoding
	return enc.EncodeToString(priv.Seed()), enc.EncodeToString(pub), nil
}

// PublicKeyOf returns the base64 public key of a base64 private key.
func PublicKeyOf(privateB64 string) (string, error) {
	priv, err := parsePrivate(privateB64)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

// Sign returns the base64 signature of data under a base64 private key.
func Sign(privateB64 string, data []byte) (string, error) {
	priv, err := parsePrivate(privateB64)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, message(data))), nil
}

// Verify reports whether sigB64 is a signature of data under publicB64. A
// malformed key or signature is an error; a well-formed signature that does
// not match is ErrBadSignature.
func Verify(publicB64 string, data []byte, sigB64 string) error {
	pub, err := decode("public key", publicB64, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	sig, err := decode("signature", sigB64, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), message(data), sig) {
		return ErrBadSignature
	}
	return nil
}

func parsePrivate(privateB64 string) (ed25519.PrivateKey, error) {
	seed, err := decode("private key", privateB64, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func decode(what, s string, size int) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("%s is not base64: %w", what, err)
	}
	if len(b) != size {
		return nil, fmt.Errorf("%s is %d bytes, want %d", what, len(b), size)
	}
	return b, nil
}

func message(data []byte) []byte {
	return append([]byte(signingContext), data...)
}
