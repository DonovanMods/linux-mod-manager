package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()
	defer r.Close()

	fnErr := fn()
	require.NoError(t, w.Close(), "closing write end of the pipe")
	out, readErr := io.ReadAll(r)

	require.NoError(t, fnErr)
	require.NoError(t, readErr)
	return string(out)
}

func TestDoAuthStatusIncludesCustomSources(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// Auth-capable manifest source, key provided via env.
	withAuth, err := custom.NewManifest(custom.SourceDefinition{
		ID: "my-repo", Name: "My Repo", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://repo.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(withAuth)

	// Auth-capable manifest source with no key anywhere.
	noKey, err := custom.NewManifest(custom.SourceDefinition{
		ID: "keyless-repo", Name: "Keyless", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://other.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(noKey)

	// Directory source: no auth capability, must not be listed.
	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "local-mods", Name: "Local", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)

	t.Setenv("LMM_MY_REPO_API_KEY", "supersecretkey")

	out := captureStdout(t, func() error { return doAuthStatus(svc) })

	// "(<id>): ..." — doAuthStatus renders "<Name> (<id>): ..." for every
	// auth-capable source, built-in or custom (see
	// TestAuthStatus_RendersDisplayNameAlongsideID for the literal-line pin).
	assert.Contains(t, out, "My Repo (my-repo): authenticated via LMM_MY_REPO_API_KEY")
	assert.NotContains(t, out, "supersecretkey")
	assert.Contains(t, out, "Keyless (keyless-repo): not authenticated")
	assert.NotContains(t, out, "local-mods")
}

// TestDoAuthStatusListsCustomSourcesInSortedOrder pins finding 3b: the
// custom-sources stanza must not depend on registry map iteration order,
// which Go randomizes.
func TestDoAuthStatusListsCustomSourcesInSortedOrder(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

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

	out := captureStdout(t, func() error { return doAuthStatus(svc) })

	// Each source's Name equals its ID above, so the rendered "<Name>
	// (<id>): ..." line contains "(<id>):" once per source.
	alpha := strings.Index(out, "(alpha-repo):")
	mid := strings.Index(out, "(mid-repo):")
	zeta := strings.Index(out, "(zeta-repo):")
	require.True(t, alpha >= 0 && mid >= 0 && zeta >= 0, "all three sources must be reported")
	assert.Less(t, alpha, mid, "custom sources must be reported in ID order")
	assert.Less(t, mid, zeta, "custom sources must be reported in ID order")
}

// TestDoAuthStatusListsOrphanedTokens pins finding 3a: a token stored for a
// source that is no longer registered (built-in or custom) is otherwise
// invisible — it must get a final stanza pointing at how to remove it.
// Registered sources with stored tokens must NOT be reported as orphaned.
//
// Registers nexusmods explicitly: post-Task-3, doAuthStatus's "registered"
// set comes purely from authCapableSources' registry query, with no more
// hardcoded built-in-ID special case — a *core.Service built directly here
// (bypassing root.go's registerSources, which registers built-ins in
// production) must register nexusmods itself for "still-registered
// built-in" to mean anything.
func TestDoAuthStatusListsOrphanedTokens(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(nil, ""))

	// A token for a source ID that matches nothing registered (built-in or
	// custom) — e.g. its definition file was deleted after login.
	require.NoError(t, svc.SaveSourceToken("ghost-repo", "leftover-secret-key"))
	// A token for a still-registered built-in must not be reported as orphaned.
	require.NoError(t, svc.SaveSourceToken("nexusmods", "built-in-key-1234567"))

	out := captureStdout(t, func() error { return doAuthStatus(svc) })

	assert.Contains(t, out, "ghost-repo: stored token with no matching source (key:")
	assert.Contains(t, out, "remove with: lmm auth logout ghost-repo")
	assert.NotContains(t, out, "leftover-secret-key")
	assert.NotContains(t, out, "nexusmods: stored token with no matching source")
}

// TestDoAuthStatusDistinguishesAuthRemovedFromUnregistered pins the carry-in
// fix: a stored token whose source is STILL registered but no longer
// declares auth (e.g. a custom source's manifest dropped its `auth:` block)
// must be labeled differently from a token whose source isn't registered at
// all (TestDoAuthStatusListsOrphanedTokens above) - the two have different
// remedies (re-declare auth vs. the token is simply stale) and must not
// share wording.
func TestDoAuthStatusDistinguishesAuthRemovedFromUnregistered(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// Registered, but declares no auth capability at all (a directory
	// source never does) - simulates a source whose auth was removed from
	// its declaration while a token from an earlier `auth login` lingers.
	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "local-mods", Name: "Local", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)

	require.NoError(t, svc.SaveSourceToken("local-mods", "stale-auth-key-123456"))
	// Truly-unregistered case must still render its own distinct wording.
	require.NoError(t, svc.SaveSourceToken("ghost-repo", "leftover-secret-key"))

	out := captureStdout(t, func() error { return doAuthStatus(svc) })

	assert.Contains(t, out, "local-mods: stored token for source without auth declared (key:")
	assert.Contains(t, out, "stale token? remove with: lmm auth logout local-mods")
	assert.NotContains(t, out, "local-mods: stored token with no matching source")

	assert.Contains(t, out, "ghost-repo: stored token with no matching source (key:")
	assert.NotContains(t, out, "ghost-repo: stored token for source without auth declared")
}
