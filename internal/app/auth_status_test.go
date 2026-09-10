package app

// Tests for AuthStatus (#309: `lmm auth status --json`). cmd/lmm's own
// auth_status_test.go covers doAuthStatus's PLAIN-TEXT rendering (unchanged
// wording, now rebuilt from this report); these pin the report's data
// assembly directly - sources sorted by ID, stored-vs-env precedence, the
// two orphaned-token reasons, and that no rendered form of a key - masked
// or fingerprinted - ever carries the raw secret.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAuthStatusTestService(t *testing.T) *core.Service {
	t.Helper()
	// Sandboxed HOME/XDG: these services write a token-encryption key file
	// into their data directory (#79), so nothing may resolve to the
	// developer's real installation.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
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

	// The dev shell exports a real NEXUSMODS_API_KEY; blank it so this row
	// is the STORED case it means to be (the env outranks it, #356).
	t.Setenv("NEXUSMODS_API_KEY", "")
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
	// EnvVar is filled for every row (#333 Minor #5), even one
	// authenticated via a STORED token rather than the environment.
	assert.Equal(t, "NEXUSMODS_API_KEY", nx.EnvVar)
	// A stored key is identified by fingerprint and never masked (#79) -
	// masking it would mean decrypting it just to print part of it.
	assert.NotEmpty(t, nx.KeyFingerprint)
	assert.Empty(t, nx.KeyMasked)
	assert.NotContains(t, nx.KeyFingerprint, "storedbuiltinkey12345")

	repo := byID["my-repo"]
	assert.True(t, repo.Authenticated)
	assert.Equal(t, "env", repo.Via)
	assert.Equal(t, "LMM_MY_REPO_API_KEY", repo.EnvVar)
	assert.NotContains(t, repo.KeyMasked, "supersecretkey123456")

	keyless := byID["keyless-repo"]
	assert.False(t, keyless.Authenticated)
	assert.Empty(t, keyless.Via)
	assert.Equal(t, "LMM_KEYLESS_REPO_API_KEY", keyless.EnvVar, "EnvVar names the hint even for a never-authenticated row")
	assert.Empty(t, keyless.KeyMasked)
	assert.Empty(t, keyless.KeyFingerprint)
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
	assert.NotEmpty(t, byID["local-mods"].KeyFingerprint)
	assert.Equal(t, "not_registered", byID["ghost-repo"].Reason)
	assert.NotEmpty(t, byID["ghost-repo"].KeyFingerprint)
}

// TestCredentialPrecedence covers the four states the one shared
// precedence function has to answer for (#356): env only, stored only,
// both, neither. credentialVia is read by ResolveAPIKey (what the source
// clients are handed) and by AuthStatus (what every "which key am I
// using?" surface reports), which is what keeps `lmm auth status` from
// naming a credential the HTTP clients are not sending.
//
// The both-set case is also the #79 shape check: a shadowed stored key is
// reported as present, by fingerprint, and the key itself - raw or masked -
// appears nowhere in the marshalled report.
func TestCredentialPrecedence(t *testing.T) {
	src := nexusmods.New(nil, "")

	tests := []struct {
		name         string
		env, stored  string
		wantVia      string
		wantKey      string
		wantShadowed bool
	}{
		{name: "neither"},
		{name: "stored only", stored: "storedkey1234567890", wantVia: "stored", wantKey: "storedkey1234567890"},
		{name: "env only", env: "envkey1234567890", wantVia: "env", wantKey: "envkey1234567890"},
		{
			name: "both - the environment wins and the stored key is shadowed",
			env:  "envkey1234567890", stored: "storedkey1234567890",
			wantVia: "env", wantKey: "envkey1234567890", wantShadowed: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAuthStatusTestService(t)
			svc.RegisterSource(src)
			t.Setenv("NEXUSMODS_API_KEY", tc.env)
			if tc.stored != "" {
				require.NoError(t, svc.SaveSourceToken(context.Background(), "nexusmods", tc.stored))
			}

			// The precedence itself, before either caller applies it.
			via, shadowed := credentialVia(tc.env != "", tc.stored != "")
			assert.Equal(t, tc.wantVia, via)
			assert.Equal(t, tc.wantShadowed, shadowed)

			// What the source clients are actually given.
			key, err := ResolveAPIKey(context.Background(), svc, src)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, key)

			// What `lmm auth status` / GET /api/v1/auth report.
			report, err := AuthStatus(context.Background(), svc)
			require.NoError(t, err)
			require.Len(t, report.Sources, 1)
			row := report.Sources[0]
			assert.Equal(t, tc.wantVia != "", row.Authenticated)
			assert.Equal(t, tc.wantVia, row.Via, "via must name the credential lmm actually sends")
			assert.Equal(t, tc.wantShadowed, row.StoredKeyShadowed,
				"a stored key that exists but is not in use must still be reported")
			if tc.wantShadowed {
				assert.Equal(t, db.TokenFingerprint(tc.stored), row.StoredKeyFingerprint,
					"the shadowed stored key is identified by fingerprint, never by the key")
				assert.Equal(t, MaskAPIKey(tc.env), row.KeyMasked)
				assert.False(t, row.CreatedAt.IsZero(), "a shadowed row has a real stored credential behind it")
			} else {
				assert.Empty(t, row.StoredKeyFingerprint,
					"stored_key_fingerprint is only for a stored key that is NOT the active one")
			}

			if tc.stored != "" {
				blob, err := json.Marshal(report)
				require.NoError(t, err)
				assert.NotContains(t, string(blob), tc.stored, "#79: a stored key never reaches the wire")
				assert.NotContains(t, string(blob), MaskAPIKey(tc.stored), "#79: not even masked")
			}
		})
	}
}

// TestResolveAPIKeyDoesNotReadTheStoreWhenTheEnvironmentSuppliesTheKey is
// review N8: ResolveAPIKey runs on the registration path, once per source
// at every app.Open, and since #79 reading the store means decrypting a
// credential. The environment wins outright (credentialVia), so the store
// must not be touched at all when it answers.
//
// A row that will NOT decrypt is the proof: reading it produces D1's
// error, so a call that returns the environment key with no error cannot
// have read it.
func TestResolveAPIKeyDoesNotReadTheStoreWhenTheEnvironmentSuppliesTheKey(t *testing.T) {
	svc, dataDir := newSandboxedService(t)
	src := nexusmods.New(nil, "")
	svc.RegisterSource(src)
	ctx := context.Background()
	require.NoError(t, svc.SaveSourceToken(ctx, "nexusmods", "stored-but-doomed"))
	corruptToken(t, dataDir, "nexusmods")

	_, err := ResolveAPIKey(ctx, svc, src)
	require.Error(t, err, "with no environment key, the damaged row is what it finds")

	t.Setenv("NEXUSMODS_API_KEY", "envkey1234567890")
	key, err := ResolveAPIKey(ctx, svc, src)
	require.NoError(t, err, "the store is not consulted once the environment has answered")
	assert.Equal(t, "envkey1234567890", key)
}
