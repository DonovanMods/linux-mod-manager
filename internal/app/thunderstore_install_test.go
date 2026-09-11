package app

// Installing a Thunderstore package, end to end (#409).
//
// This is the one test in the tree where all four pieces meet: the real
// internal/source/thunderstore source, the real core.Service install flow,
// the real Downloader, and #358's BepInEx normaliser. It lives here because
// internal/app is the composition root - internal/core is forbidden from
// importing a concrete source (its own boundary ratchet), and this test has
// to use the actual one, not a double.
//
// It reaches NO network. An httptest server serves both halves of what
// Thunderstore serves: the community listing, and the package zip at the
// exact path GetDownloadURL builds.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// thunderstoreFixture is the world a Thunderstore install happens in: a
// server with one community and one downloadable package, a Service with
// the real source registered against it, and a game root.
type thunderstoreFixture struct {
	svc  *core.Service
	game *domain.Game
	root string
}

// newThunderstoreFixture builds it. loader is the game's `loader:` block -
// nil for a game that declares none, which is what #359's precondition
// refuses.
func newThunderstoreFixture(t *testing.T, loader *domain.GameLoader) thunderstoreFixture {
	t.Helper()
	sandboxHome(t)

	// The archive shape is the one a Valheim-era BepInEx plugin actually
	// ships: the payload rooted at `plugins/`, BepInEx-RELATIVE, with the
	// package metadata Thunderstore requires at the root beside it. #358's
	// normaliser prefixes the payload with BepInEx/ and drops the metadata;
	// without it, the DLL would deploy to <game root>/plugins/ where
	// nothing loads it, and manifest.json and icon.png would be scattered
	// into the Steam install directory.
	archive := zipBytes(t, map[string]string{
		"plugins/ShipLoot.dll": "assembly bytes",
		"manifest.json":        `{"name":"ShipLoot","version_number":"1.1.0"}`,
		"icon.png":             "png bytes",
		"README.md":            "# ShipLoot",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/c/lethal-company/api/v1/package/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(communityListing(len(archive)))
	})
	mux.HandleFunc("/package/download/tinyhoot/ShipLoot/1.1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: cacheDir,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: srv.URL}))

	// A BepInEx game: mod_path IS the install path, which is how lmm
	// expresses "this game's mods live in the game root".
	root := t.TempDir()
	game := &domain.Game{
		ID: "lethal-company", Name: "Lethal Company",
		InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, Loader: loader,
		SourceIDs: map[string]string{"thunderstore": "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return thunderstoreFixture{svc: svc, game: game, root: root}
}

// TestThunderstoreInstall_EndToEnd is the claim T2 exists to make: `lmm
// install tinyhoot-ShipLoot --source thunderstore` works, with no key, no
// second round trip for a download URL, and the plugin in the place BepInEx
// reads.
func TestThunderstoreInstall_EndToEnd(t *testing.T) {
	f := newThunderstoreFixture(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.2100"})

	plan, err := f.svc.PlanInstall(t.Context(), f.game, "default", "thunderstore", "tinyhoot-ShipLoot", false)
	require.NoError(t, err)
	assert.Equal(t, "ShipLoot", plan.Mod.Name)
	require.Len(t, plan.Files, 1, "a version IS a file")
	assert.Equal(t, "1.1.0", plan.Files[0].Version)
	assert.Positive(t, plan.TotalDownloadBytes, "file_size is exact, so the plan can state the transfer")

	_, err = f.svc.ApplyInstall(t.Context(), f.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	// The headline: the payload landed where BepInEx loads it, under the
	// GAME ROOT, with the BepInEx/ prefix the archive did not carry.
	_, err = os.Lstat(filepath.Join(f.root, "BepInEx", "plugins", "ShipLoot.dll"))
	require.NoError(t, err, "the plugin must deploy to <game root>/BepInEx/plugins/")

	// And the package metadata did not: it describes the package to the
	// website, and deploying it scatters a manifest.json and an icon.png
	// into the Steam install directory of every game.
	for _, name := range []string{"manifest.json", "icon.png", "README.md"} {
		_, err := os.Lstat(filepath.Join(f.root, name))
		assert.Truef(t, os.IsNotExist(err), "%s must not deploy into the game root", name)
	}
	_, err = os.Lstat(filepath.Join(f.root, "plugins"))
	assert.True(t, os.IsNotExist(err), "the un-prefixed payload directory must not survive")

	// The DB row records what was actually installed, by the identity the
	// user typed.
	installed, err := f.svc.GetInstalledMod(t.Context(), "thunderstore", "tinyhoot-ShipLoot", "lethal-company", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", installed.Version)
	assert.Equal(t, []string{"1.1.0"}, installed.FileIDs, "the file id IS the version")
}

// TestThunderstoreInstall_RefusesAGameWithNoLoader is the same install into
// a game that declares no loader: refused at PLAN time, off the package's
// own dependency string, before a byte is transferred. Without it lmm would
// deploy an assembly into a game with nothing to load it and report
// success - the hardest kind of failure to diagnose.
func TestThunderstoreInstall_RefusesAGameWithNoLoader(t *testing.T) {
	f := newThunderstoreFixture(t, nil)

	_, err := f.svc.PlanInstall(t.Context(), f.game, "default", "thunderstore", "tinyhoot-ShipLoot", false)
	require.Error(t, err)

	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, domain.LoaderKindBepInEx, loaderErr.Kind)
	assert.Equal(t, "5.4.2100", loaderErr.Version, "the version the package pinned")
	assert.Contains(t, strings.Join(loaderErr.Setup, "\n"), "lmm game edit lethal-company --loader bepinex")

	entries, err := os.ReadDir(f.root)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused plan deploys nothing")
}

// TestThunderstoreUpdate_EndToEnd: the update check runs over the same
// index, with no request of its own.
func TestThunderstoreUpdate_EndToEnd(t *testing.T) {
	f := newThunderstoreFixture(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx})

	plan, err := f.svc.PlanInstall(t.Context(), f.game, "default", "thunderstore", "tinyhoot-ShipLoot", false)
	require.NoError(t, err)
	_, err = f.svc.ApplyInstall(t.Context(), f.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	installed, err := f.svc.GetInstalledMods(t.Context(), f.game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1)

	updates, err := f.svc.CheckGameUpdates(t.Context(), f.game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Empty(t, updates, "the installed version is the newest one the community publishes")
}

// communityListing is the one-package community document the fixture
// server serves. size is the archive's exact byte count, because
// Thunderstore reports file_size to the byte and the source declares
// source.ExactFileSizer - so a size that disagreed with the body would
// (correctly) fail the install.
func communityListing(size int) []byte {
	return fmt.Appendf(nil, `[{
	  "name": "ShipLoot",
	  "full_name": "tinyhoot-ShipLoot",
	  "owner": "tinyhoot",
	  "package_url": "https://thunderstore.io/c/lethal-company/p/tinyhoot/ShipLoot/",
	  "date_updated": "2026-09-05T10:00:00.000000Z",
	  "is_deprecated": false,
	  "categories": ["Mods"],
	  "versions": [{
	    "name": "ShipLoot",
	    "full_name": "tinyhoot-ShipLoot-1.1.0",
	    "description": "Shows the total value of scrap aboard the ship.",
	    "version_number": "1.1.0",
	    "dependencies": ["BepInEx-BepInExPack-5.4.2100"],
	    "date_created": "2026-09-05T10:00:00.000000Z",
	    "website_url": "",
	    "file_size": %d
	  }]
	}]`, size)
}

// zipBytes builds an archive in memory, so the fixture server can serve the
// same bytes it declared the size of.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}
