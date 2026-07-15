package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
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

	assert.Contains(t, out, "my-repo: authenticated via LMM_MY_REPO_API_KEY")
	assert.NotContains(t, out, "supersecretkey")
	assert.Contains(t, out, "keyless-repo: not authenticated")
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

	alpha, mid, zeta := strings.Index(out, "alpha-repo:"), strings.Index(out, "mid-repo:"), strings.Index(out, "zeta-repo:")
	require.True(t, alpha >= 0 && mid >= 0 && zeta >= 0, "all three sources must be reported")
	assert.Less(t, alpha, mid, "custom sources must be reported in ID order")
	assert.Less(t, mid, zeta, "custom sources must be reported in ID order")
}

// TestDoAuthStatusListsOrphanedTokens pins finding 3a: a token stored for a
// source that is no longer registered (built-in or custom) is otherwise
// invisible — it must get a final stanza pointing at how to remove it.
// Registered sources with stored tokens must NOT be reported as orphaned.
func TestDoAuthStatusListsOrphanedTokens(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

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
