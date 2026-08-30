package app

// Tests for AuthStatus (#309: `lmm auth status --json`). cmd/lmm's own
// auth_status_test.go covers doAuthStatus's PLAIN-TEXT rendering (unchanged
// wording, now rebuilt from this report); these pin the report's data
// assembly directly - sources sorted by ID, stored-vs-env precedence, the
// two orphaned-token reasons, and that a masked key never carries the raw
// secret.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAuthStatusTestService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// TestAuthStatus_StoredVsEnvVsUnauthenticated covers the three source
// states AuthSourceStatus can carry, and that a directory source (no auth
// capability) is excluded entirely.
func TestAuthStatus_StoredVsEnvVsUnauthenticated(t *testing.T) {
	svc := newAuthStatusTestService(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	withEnv, err := custom.NewManifest(custom.SourceDefinition{
		ID: "my-repo", Name: "My Repo", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://repo.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(withEnv)

	noKey, err := custom.NewManifest(custom.SourceDefinition{
		ID: "keyless-repo", Name: "Keyless", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://other.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(noKey)

	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "local-mods", Name: "Local", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)

	require.NoError(t, svc.SaveSourceToken(context.Background(), "nexusmods", "storedbuiltinkey12345"))
	t.Setenv("LMM_MY_REPO_API_KEY", "supersecretkey123456")

	report, err := AuthStatus(context.Background(), svc)
	require.NoError(t, err)
	require.Empty(t, report.Orphaned)

	byID := map[string]AuthSourceStatus{}
	for _, s := range report.Sources {
		byID[s.ID] = s
	}
	require.Len(t, report.Sources, 3, "directory source has no auth capability and must be excluded")
	assert.NotContains(t, byID, "local-mods")

	nx := byID["nexusmods"]
	assert.True(t, nx.Authenticated)
	assert.Equal(t, "stored", nx.Via)
	assert.Empty(t, nx.EnvVar)
	assert.NotEmpty(t, nx.KeyMasked)
	assert.NotContains(t, nx.KeyMasked, "storedbuiltinkey12345")

	repo := byID["my-repo"]
	assert.True(t, repo.Authenticated)
	assert.Equal(t, "env", repo.Via)
	assert.Equal(t, "LMM_MY_REPO_API_KEY", repo.EnvVar)
	assert.NotContains(t, repo.KeyMasked, "supersecretkey123456")

	keyless := byID["keyless-repo"]
	assert.False(t, keyless.Authenticated)
	assert.Empty(t, keyless.Via)
	assert.Empty(t, keyless.EnvVar)
	assert.Empty(t, keyless.KeyMasked)
}

// TestAuthStatus_SourcesSortedByID pins that the source rows are sorted by
// ID, not registry map order (Go randomizes it).
func TestAuthStatus_SourcesSortedByID(t *testing.T) {
	svc := newAuthStatusTestService(t)
	for _, id := range []string{"zeta-repo", "alpha-repo", "mid-repo"} {
		src, err := custom.NewManifest(custom.SourceDefinition{
			ID: id, Name: id, Type: custom.TypeManifest,
			Manifest: &custom.ManifestConfig{
				URL:  "https://" + id + ".test/mods.yaml",
				Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
			},
		})
		require.NoError(t, err)
		svc.RegisterSource(src)
	}

	report, err := AuthStatus(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, report.Sources, 3)
	assert.Equal(t, []string{"alpha-repo", "mid-repo", "zeta-repo"}, []string{
		report.Sources[0].ID, report.Sources[1].ID, report.Sources[2].ID,
	})
}

// TestMaskAPIKey pins MaskAPIKey's masking rule, moved here from cmd/lmm
// (#309) since it now belongs to app alongside AuthStatus, its only
// production caller.
func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "normal key", input: "abcdefghijklmnop", expected: "abc...nop"},
		{name: "exactly 9 chars reveals both ends", input: "123456789", expected: "123...789"},
		{name: "exactly 8 chars fully masked", input: "12345678", expected: "***"},
		{name: "exactly 7 chars fully masked", input: "1234567", expected: "***"},
		{name: "6 chars or less returns ***", input: "123456", expected: "***"},
		{name: "short key", input: "abc", expected: "***"},
		{name: "empty key", input: "", expected: "***"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, MaskAPIKey(tt.input))
		})
	}
}

// TestAuthStatus_OrphanedTokenReasons covers the two distinct causes: a
// token for a still-registered source that no longer declares auth
// ("auth_not_declared") versus one for nothing registered at all
// ("not_registered"). A registered source's own token must never appear
// here.
func TestAuthStatus_OrphanedTokenReasons(t *testing.T) {
	svc := newAuthStatusTestService(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "local-mods", Name: "Local", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)

	require.NoError(t, svc.SaveSourceToken(context.Background(), "nexusmods", "built-in-key-1234567"))
	require.NoError(t, svc.SaveSourceToken(context.Background(), "local-mods", "stale-auth-key-123456"))
	require.NoError(t, svc.SaveSourceToken(context.Background(), "ghost-repo", "leftover-secret-key12"))

	report, err := AuthStatus(context.Background(), svc)
	require.NoError(t, err)

	byID := map[string]OrphanedToken{}
	for _, o := range report.Orphaned {
		byID[o.ID] = o
	}
	require.Len(t, report.Orphaned, 2, "a registered source's own token must not be reported as orphaned")
	assert.Equal(t, "auth_not_declared", byID["local-mods"].Reason)
	assert.NotEmpty(t, byID["local-mods"].KeyMasked)
	assert.Equal(t, "not_registered", byID["ghost-repo"].Reason)
	assert.NotEmpty(t, byID["ghost-repo"].KeyMasked)
}
