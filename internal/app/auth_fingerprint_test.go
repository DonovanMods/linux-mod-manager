package app

// #79: what a status surface is allowed to say about a STORED credential.
// AuthStatus used to decrypt every token just to mask it; now the stored
// half of the report is assembled from presence + fingerprint alone, and
// only a key that lmm legitimately already holds in the clear - one in the
// environment - is still shown masked.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSandboxedService points HOME and every XDG variable at t.TempDir() and
// returns a service rooted there, with the token key where app.Open would
// put it.
func newSandboxedService(t *testing.T) (svc *core.Service, dataDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	dataDir = filepath.Join(home, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0700))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: filepath.Join(home, "config"),
		DataDir:   dataDir,
		CacheDir:  filepath.Join(home, "cache"),
		KeyPath:   filepath.Join(dataDir, db.TokenKeyFileName),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, dataDir
}

func TestAuthStatus_StoredRowsCarryAFingerprintAndNoMask(t *testing.T) {
	svc, _ := newSandboxedService(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	ctx := context.Background()
	const stored = "storedbuiltinkey12345"
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", stored))

	report, err := AuthStatus(ctx, svc)
	require.NoError(t, err)
	require.Len(t, report.Sources, 1)

	row := report.Sources[0]
	assert.True(t, row.Authenticated)
	assert.Equal(t, "stored", row.Via)
	assert.Equal(t, db.TokenFingerprint(stored), row.KeyFingerprint)
	assert.Empty(t, row.KeyMasked, "a stored key is never decrypted for display")
	assert.False(t, row.CreatedAt.IsZero())
	assert.False(t, row.UpdatedAt.IsZero())
}

func TestAuthStatus_EnvRowsKeepTheirMaskAndGainAFingerprint(t *testing.T) {
	svc, _ := newSandboxedService(t)
	src, err := custom.NewManifest(custom.SourceDefinition{
		ID: "my-repo", Name: "My Repo", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://repo.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)
	t.Setenv("LMM_MY_REPO_API_KEY", "supersecretkey123456")

	report, err := AuthStatus(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, report.Sources, 1)

	row := report.Sources[0]
	assert.Equal(t, "env", row.Via)
	assert.Equal(t, MaskAPIKey("supersecretkey123456"), row.KeyMasked)
	assert.Equal(t, db.TokenFingerprint("supersecretkey123456"), row.KeyFingerprint)
	assert.True(t, row.CreatedAt.IsZero(), "an environment key has no stored-at time")
}

func TestAuthStatus_OrphanedTokensCarryAFingerprint(t *testing.T) {
	svc, _ := newSandboxedService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "ghost-repo", "orphaned-key-123456"))

	report, err := AuthStatus(ctx, svc)
	require.NoError(t, err)
	require.Len(t, report.Orphaned, 1)
	assert.Equal(t, "ghost-repo", report.Orphaned[0].ID)
	assert.Equal(t, "not_registered", report.Orphaned[0].Reason)
	assert.Equal(t, db.TokenFingerprint("orphaned-key-123456"), report.Orphaned[0].KeyFingerprint)
}

// TestAuthStatus_UnreadableStoredRowFallsBackToTheEnvironment: an
// undecryptable credential is not an authenticated one, and a key in the
// environment must still win rather than being masked by a dead row.
func TestAuthStatus_UnreadableStoredRowFallsBackToTheEnvironment(t *testing.T) {
	svc, dataDir := newSandboxedService(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", "stored-but-doomed"))
	corruptToken(t, dataDir, "nexusmods")

	report, err := AuthStatus(ctx, svc)
	require.NoError(t, err, "a damaged row must not fail the whole status")
	require.Len(t, report.Sources, 1)
	assert.True(t, report.Sources[0].Unreadable)
	assert.False(t, report.Sources[0].Authenticated)
	assert.Empty(t, report.Sources[0].Via)

	t.Setenv("NEXUSMODS_API_KEY", "env-key-takes-over-1234")
	report, err = AuthStatus(ctx, svc)
	require.NoError(t, err)
	assert.True(t, report.Sources[0].Authenticated)
	assert.Equal(t, "env", report.Sources[0].Via)
	assert.True(t, report.Sources[0].Unreadable, "the damaged row is still worth reporting")
}

// corruptToken flips the last byte of sourceID's stored ciphertext.
func corruptToken(t *testing.T, dataDir, sourceID string) {
	t.Helper()
	raw, err := db.New(filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, raw.Close()) }()

	ctx := context.Background()
	var blob []byte
	require.NoError(t, raw.QueryRowContext(ctx, "SELECT token_data FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&blob))
	blob[len(blob)-1] ^= 0xff
	_, err = raw.ExecContext(ctx, "UPDATE auth_tokens SET token_data = ? WHERE source_id = ?", blob, sourceID)
	require.NoError(t, err)
}
