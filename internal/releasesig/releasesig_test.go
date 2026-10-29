package releasesig_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignVerify_RoundTrip(t *testing.T) {
	priv, pub, err := releasesig.Generate()
	require.NoError(t, err)
	derived, err := releasesig.PublicKeyOf(priv)
	require.NoError(t, err)
	assert.Equal(t, pub, derived)

	data := []byte("abc123  lmm_2.2.0_linux_amd64.tar.gz\n")
	sig, err := releasesig.Sign(priv, data)
	require.NoError(t, err)
	require.NoError(t, releasesig.Verify(pub, data, sig))
	require.NoError(t, releasesig.Verify(pub+"\n", data, sig+"\n"), "surrounding whitespace is ignored")
}

func TestVerify_RefusesTamperingAndTheWrongKey(t *testing.T) {
	priv, pub, err := releasesig.Generate()
	require.NoError(t, err)
	_, otherPub, err := releasesig.Generate()
	require.NoError(t, err)
	data := []byte("abc123  lmm_2.2.0_linux_amd64.tar.gz\n")
	sig, err := releasesig.Sign(priv, data)
	require.NoError(t, err)

	assert.ErrorIs(t, releasesig.Verify(pub, []byte("evil  lmm_2.2.0_linux_amd64.tar.gz\n"), sig), releasesig.ErrBadSignature)
	assert.ErrorIs(t, releasesig.Verify(otherPub, data, sig), releasesig.ErrBadSignature)
}

// TestVerify_SignsInContext: a bare ed25519 signature of the same bytes,
// made without the release context, does not verify.
func TestVerify_SignsInContext(t *testing.T) {
	priv, pub, err := releasesig.Generate()
	require.NoError(t, err)
	seed, err := base64.StdEncoding.DecodeString(priv)
	require.NoError(t, err)
	data := []byte("checksums")
	bare := base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), data))
	assert.ErrorIs(t, releasesig.Verify(pub, data, bare), releasesig.ErrBadSignature)
}

func TestVerify_MalformedInputIsAnError(t *testing.T) {
	_, pub, err := releasesig.Generate()
	require.NoError(t, err)
	err = releasesig.Verify(pub, []byte("x"), "not base64!")
	require.Error(t, err)
	assert.NotErrorIs(t, err, releasesig.ErrBadSignature)
	assert.Error(t, releasesig.Verify("c2hvcnQ=", []byte("x"), base64.StdEncoding.EncodeToString(make([]byte, 64))))
	_, err = releasesig.Sign("c2hvcnQ=", []byte("x"))
	assert.Error(t, err)
}
