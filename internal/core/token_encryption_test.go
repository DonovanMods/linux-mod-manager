package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// sandboxDirs points HOME and every XDG variable at t.TempDir() and returns
// a ServiceConfig rooted there, so nothing here can reach the developer's
// real installation.
func sandboxDirs(t *testing.T) core.ServiceConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	dataDir := filepath.Join(home, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0700))
	return core.ServiceConfig{
		ConfigDir: filepath.Join(home, "config"),
		DataDir:   dataDir,
		CacheDir:  filepath.Join(home, "cache"),
		KeyPath:   filepath.Join(dataDir, db.TokenKeyFileName),
	}
}

// TestService_KeyPathFromTheCompositionRootIsWhereTheKeyLands pins that the
// key path travels from app through ServiceConfig into the db layer: core
// resolves nothing itself.
func TestService_KeyPathFromTheCompositionRootIsWhereTheKeyLands(t *testing.T) {
	cfg := sandboxDirs(t)
	elsewhere := filepath.Join(t.TempDir(), "custom-key")
	cfg.KeyPath = elsewhere

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", "a-stored-key"))

	info, err := os.Stat(elsewhere)
	require.NoError(t, err, "the key must land where the composition root said")
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	got, err := svc.GetSourceToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "a-stored-key", got.APIKey)
}

// TestService_ListSourceTokensNeverCarriesThePlaintext pins the shape every
// status surface consumes: presence, timestamps, fingerprint.
func TestService_ListSourceTokensNeverCarriesThePlaintext(t *testing.T) {
	cfg := sandboxDirs(t)
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", "the-secret-key"))

	infos, err := svc.ListSourceTokens(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "nexusmods", infos[0].SourceID)
	assert.True(t, infos[0].Readable)
	assert.Equal(t, db.TokenFingerprint("the-secret-key"), infos[0].Fingerprint)
	assert.False(t, infos[0].CreatedAt.IsZero())
}

// TestService_MissingKeyFileIsATypedTokenKeyError pins the failure a user
// who deleted <DataDir>/key actually hits, and the details the --json
// envelope carries for it.
func TestService_MissingKeyFileIsATypedTokenKeyError(t *testing.T) {
	cfg := sandboxDirs(t)
	svc, err := core.NewService(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", "stored-key"))
	require.NoError(t, svc.Close())
	require.NoError(t, os.Remove(cfg.KeyPath))

	reopened, err := core.NewService(cfg)
	require.NoError(t, err, "the service must still open without its token key")
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	_, err = reopened.GetSourceToken(ctx, "nexusmods")
	var keyErr *core.TokenKeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, cfg.KeyPath, keyErr.KeyPath)
	assert.Equal(t, "missing", keyErr.Reason)
	assert.Contains(t, err.Error(), cfg.KeyPath)
	assert.Contains(t, err.Error(), "lmm auth login")

	_, err = reopened.ListSourceTokens(ctx)
	require.ErrorAs(t, err, &keyErr)
}

// TestService_UndecryptableRowNamesTheSource pins the per-source failure: a
// corrupt row is reported for that source, not as a global outage.
func TestService_UndecryptableRowNamesTheSource(t *testing.T) {
	cfg := sandboxDirs(t)
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "good-repo", "good-key"))
	require.NoError(t, svc.SaveSourceToken(ctx, "bad-repo", "bad-key"))
	corruptStoredToken(t, filepath.Join(cfg.DataDir, "lmm.db"), "bad-repo")

	_, err = svc.GetSourceToken(ctx, "bad-repo")
	var keyErr *core.TokenKeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, "undecryptable", keyErr.Reason)
	assert.Equal(t, []string{"bad-repo"}, keyErr.Sources)

	good, err := svc.GetSourceToken(ctx, "good-repo")
	require.NoError(t, err, "one damaged row must not take down the others")
	assert.Equal(t, "good-key", good.APIKey)
}

// corruptStoredToken flips the last byte of one row's ciphertext through a
// second connection to the same database file.
func corruptStoredToken(t *testing.T, dbPath, sourceID string) {
	t.Helper()
	raw, err := db.New(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, raw.Close()) }()

	ctx := context.Background()
	var blob []byte
	require.NoError(t, raw.QueryRowContext(ctx, "SELECT token_data FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&blob))
	blob[len(blob)-1] ^= 0xff
	_, err = raw.ExecContext(ctx, "UPDATE auth_tokens SET token_data = ? WHERE source_id = ?", blob, sourceID)
	require.NoError(t, err)
}

// TestTokenKeyError_WithoutACauseDoesNotPanic: TokenKeyError is exported and
// its Error method used to dereference Err unconditionally. Every production
// value comes from asTokenKeyError, which always sets it, but a test or a
// future frontend building one by hand would have panicked (review, Minor 7).
func TestTokenKeyError_WithoutACauseDoesNotPanic(t *testing.T) {
	err := &core.TokenKeyError{KeyPath: "/data/lmm/key", Reason: "missing"}
	assert.Contains(t, err.Error(), "/data/lmm/key")
	assert.Contains(t, err.Error(), "missing")
	assert.Nil(t, errors.Unwrap(err))
}
