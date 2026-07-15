package main

import (
	"io"
	"os"
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
	require.NoError(t, fn())
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
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
