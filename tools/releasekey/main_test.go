package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSignVerify(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "release.key")

	var out bytes.Buffer
	require.NoError(t, run([]string{"generate", "-out", keyFile}, &out))
	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	var pub bytes.Buffer
	require.NoError(t, run([]string{"public", "-key", keyFile}, &pub))
	pubKey := strings.TrimSpace(pub.String())
	assert.Contains(t, out.String(), pubKey, "generate prints the public key")

	key, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	t.Setenv(envKey, string(key))
	sums := filepath.Join(dir, "checksums.txt")
	require.NoError(t, os.WriteFile(sums, []byte("abc  lmm.tar.gz\n"), 0o644))
	sig := sums + ".sig"
	require.NoError(t, run([]string{"sign", "-in", sums, "-out", sig}, &out))

	var ok bytes.Buffer
	require.NoError(t, run([]string{"verify", "-pub", pubKey, "-in", sums, "-sig", sig}, &ok))
	assert.Equal(t, "OK\n", ok.String())

	require.NoError(t, os.WriteFile(sums, []byte("evil  lmm.tar.gz\n"), 0o644))
	assert.Error(t, run([]string{"verify", "-pub", pubKey, "-in", sums, "-sig", sig}, &ok))
}

func TestGenerate_NeverOverwritesAKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "release.key")
	require.NoError(t, os.WriteFile(keyFile, []byte("the only copy\n"), 0o600))
	assert.Error(t, run([]string{"generate", "-out", keyFile}, &bytes.Buffer{}))
	got, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	assert.Equal(t, "the only copy\n", string(got))
}

func TestSign_NeedsTheKeyInTheEnvironment(t *testing.T) {
	t.Setenv(envKey, "")
	in := filepath.Join(t.TempDir(), "checksums.txt")
	require.NoError(t, os.WriteFile(in, []byte("x"), 0o644))
	err := run([]string{"sign", "-in", in, "-out", in + ".sig"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envKey)
}
