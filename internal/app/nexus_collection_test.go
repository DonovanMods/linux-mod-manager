package app

// Converting a real NexusMods collection, end to end (#607 C2a, #609 C4).
//
// The real internal/source/nexusmods source (its GraphQL resolver, its
// download_link call and its collection.json parser) meets the real core
// fetch, extraction and converter here, over the recipes and revision lists
// #607's fixture survey recorded (internal/source/nexusmods/testdata/
// collections, reduced to names and ids). It lives in internal/app because
// internal/core may not import a concrete source.
//
// It reaches NO network: one TLS test server answers as api.nexusmods.com
// (GraphQL and REST) and as the CDN, and the recipe archive it serves is a
// .7z built from the fixture at test time (skipped without a system 7z).

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nexusFixtures = "../source/nexusmods/testdata/collections"

// rewriteTransport sends every request to the test server, whatever host
// it names, so the source's real base URLs need no seam.
type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	r.Host = rt.target.Host
	return rt.base.RoundTrip(r)
}

// nexusCollectionServer serves one collection: revisionFixture as the
// GraphQL answer (or a synthetic one when it is empty), the download_link
// path, and recipeFixture as a .7z.
func nexusCollectionServer(t *testing.T, slug string, revision int, revisionFixture, recipeFixture string) (*httptest.Server, *http.Client) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	// A bare name is a recorded survey fixture; a path is this package's
	// own (testdata/collections: hand-written recipes).
	recipePath := recipeFixture
	if !strings.Contains(recipeFixture, "/") {
		recipePath = filepath.Join(nexusFixtures, recipeFixture)
	}
	recipe, err := os.ReadFile(recipePath)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), recipe, 0o644))
	archivePath := filepath.Join(t.TempDir(), "recipe.7z")
	out, err := exec.Command("7z", "a", "-bd", archivePath, filepath.Join(dir, "collection.json")).CombinedOutput()
	require.NoError(t, err, "%s", out)
	archive, err := os.ReadFile(archivePath)
	require.NoError(t, err)

	var revisionBody []byte
	if revisionFixture != "" {
		revisionBody, err = os.ReadFile(filepath.Join(nexusFixtures, revisionFixture))
		require.NoError(t, err)
	} else {
		revisionBody, err = json.Marshal(map[string]any{"data": map[string]any{"collectionRevision": map[string]any{
			"revisionNumber": revision, "downloadLink": "/v2/collections/1/revisions/2/download_link",
			"collection": map[string]any{"slug": slug, "name": slug, "user": map[string]any{"name": "curator"}},
			"modFiles":   []any{}, "externalResources": []any{},
		}}})
		require.NoError(t, err)
	}

	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/graphql":
			body, _ := io.ReadAll(r.Body)
			assert.Contains(t, string(body), slug)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(revisionBody)
		case strings.HasPrefix(r.URL.Path, "/v2/collections/") && strings.HasSuffix(r.URL.Path, "/download_link"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"download_links": []map[string]string{
				{"name": "Nexus CDN", "short_name": "Nexus CDN", "URI": srv.URL + "/cdn/recipe.7z?md5=signed&expires=1"},
			}})
		case r.URL.Path == "/cdn/recipe.7z":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	base := srv.Client().Transport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // a test server's own certificate
	return srv, &http.Client{Transport: rewriteTransport{target: target, base: base}}
}

func convertNexusFixture(t *testing.T, game *domain.Game, ref string, slug string, revision int, revisionFixture, recipeFixture string, opts core.CollectionConvertOptions) *core.ConvertedCollection {
	t.Helper()
	sandboxHome(t)
	srv, client := nexusCollectionServer(t, slug, revision, revisionFixture, recipeFixture)
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(client, "testapikey"))
	RegisterAdapters(svc)
	c, err := svc.ConvertSourceCollection(context.Background(), game, "nexusmods", ref, opts)
	require.NoError(t, err)
	require.Empty(t, c.ManifestError)
	return c
}

func unsupportedNames(c *core.ConvertedCollection) []string {
	var out []string
	for _, f := range c.Unsupported {
		out = append(out, f.Feature)
	}
	return out
}

func notesContain(c *core.ConvertedCollection, sub string) bool {
	for _, n := range c.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// The Cyberpunk sample: every item pinned and locked, FOMOD replayed, its
// 27 rules all among members; refused for the exact file lists (C5) and the
// archive load order (C9) - and for nothing else.
func TestNexusCollection_Cyberpunk(t *testing.T) {
	game := &domain.Game{ID: "cp2077", Name: "Cyberpunk 2077", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "cyberpunk2077"}}
	c := convertNexusFixture(t, game, "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe@484", "iszwwe", 484,
		"revision-cyberpunk2077-iszwwe-484.json", "cyberpunk2077-iszwwe-484.json", core.CollectionConvertOptions{})

	assert.Equal(t, []string{core.CollectionFeatureFileList, "file_load_order:cyberpunk2077-archive"}, unsupportedNames(c))
	assert.Equal(t, 484, c.Revision)
	doc := c.Document
	assert.Equal(t, "nexusmods:iszwwe", doc.Info.ID)
	assert.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "iszwwe", Revision: 484,
		URL: "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe", Author: "curator"}, doc.Info.Origin)
	assert.Equal(t, []string{core.CollectionFeatureFomod}, doc.Requires)
	require.Len(t, c.Items, 364)

	var installer, fileIDs, locked int
	for _, it := range doc.Items {
		fileIDs += len(it.Ref.FileIDs)
		if it.Ref.Locked {
			locked++
		}
		if !it.Ref.Fomod.IsZero() {
			installer++
		}
	}
	assert.Equal(t, 1, installer)
	assert.Equal(t, len(doc.Items), locked, "every item is exact")
	assert.Equal(t, 364-11, fileIDs, "the 11 file-list items are unsupported; every other file is pinned")
	assert.False(t, notesContain(c, "not in this collection"), "every rule names members: %v", c.Notes)

	var archived bool
	for _, it := range c.Items {
		if it.FileCategory == "ARCHIVED" && it.State == core.CollectionItemWillInstall {
			archived = true
		}
	}
	assert.True(t, archived, "an ARCHIVED file installs by its exact id")
}

// The Skyrim sample: FOMOD items replayed, SKSE64 routed to the game root
// by the bethesda adapter, the plugin list replayed as the profile's (#611);
// refused only for Scrambled Bugs, whose two patch files each carry their
// own FOMOD choices - one profile entry per mod holds one installer's.
func TestNexusCollection_Skyrim(t *testing.T) {
	game := &domain.Game{ID: "skyrimse", Name: "Skyrim Special Edition", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Adapter: "bethesda", Catalog: "skyrim-se", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	c := convertNexusFixture(t, game, "xk05aw", "xk05aw", 325,
		"revision-skyrimspecialedition-xk05aw-325.json", "skyrimspecialedition-xk05aw-325.json", core.CollectionConvertOptions{})

	assert.Equal(t, []string{core.CollectionFeatureFomodMultiFile}, unsupportedNames(c))
	assert.Equal(t, []string{"nexusmods:43532"}, c.Unsupported[0].Items)
	assert.Equal(t, []string{core.CollectionFeatureFomod, "mod_type:dinput", core.CollectionFeaturePluginOrder}, c.Document.Requires)
	assert.Len(t, c.Document.Plugins.Order, 16, "the curator's plugin list is the profile's order")
	assert.True(t, c.Document.Plugins.Rules.IsZero(), "excludePluginRules, and no rules")
	assert.Contains(t, c.Features, "mod_type:dinput")
	var fomods int
	for _, it := range c.Document.Items {
		if !it.Ref.Fomod.IsZero() {
			fomods++
		}
	}
	assert.Equal(t, 18-2, fomods, "18 items record choices; Scrambled Bugs' two cannot share one entry")
	assert.Len(t, c.Tools, 2, "the SSEEdit shortcuts are listed, never run")
	assert.True(t, notesContain(c, "SSEEdit"), "%v", c.Notes)
	assert.True(t, notesContain(c, "conflicts"), "the conflicts rule is informational: %v", c.Notes)
	for _, it := range c.Items {
		if it.ModID == "30379" {
			assert.Equal(t, "dinput", it.ModType)
			assert.Equal(t, core.CollectionItemWillInstall, it.State)
		}
	}

	// Without the adapter, nothing routes SKSE64 to the game root, so it
	// is refused rather than deployed under Data/.
	plain := *game
	plain.Adapter = ""
	c = convertNexusFixture(t, &plain, "xk05aw", "xk05aw", 325,
		"revision-skyrimspecialedition-xk05aw-325.json", "skyrimspecialedition-xk05aw-325.json", core.CollectionConvertOptions{})
	assert.Equal(t, []string{core.CollectionFeatureFomodMultiFile, "mod_type:dinput", core.CollectionFeaturePluginOrder}, unsupportedNames(c),
		"without the adapter lmm writes no plugin list either, so the order is refused")
}

// The survey's other three shapes: plugin rules (Fallout 4), bundled files
// and patches (Stardew Valley), and an external, optional item (Fallout 4).
func TestNexusCollection_SurveyShapes(t *testing.T) {
	fo4 := &domain.Game{ID: "fo4", Name: "Fallout 4", InstallPath: t.TempDir(), ModPath: t.TempDir(), Adapter: "bethesda",
		Catalog: "fallout4", SourceIDs: map[string]string{"nexusmods": "fallout4"}}
	c := convertNexusFixture(t, fo4, "kem16d", "kem16d", 143, "", "fallout4-kem16d-143.json", core.CollectionConvertOptions{})
	assert.NotContains(t, unsupportedNames(c), core.CollectionFeaturePluginOrder)
	assert.NotEmpty(t, c.Document.Plugins.Rules.Plugins, "the survey's pluginRules sample lands as the profile's rules")

	sdv := &domain.Game{ID: "sdv", Name: "Stardew Valley", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "stardewvalley"}}
	c = convertNexusFixture(t, sdv, "tckf0m", "tckf0m", 114, "", "stardewvalley-tckf0m-114.json", core.CollectionConvertOptions{})
	got := unsupportedNames(c)
	assert.Contains(t, got, core.CollectionFeatureBundled)
	assert.Contains(t, got, core.CollectionFeaturePatches)
	assert.NotContains(t, got, "mod_type:SMAPI", "SMAPI is a prerequisite step, not a mod type to route (#635)")
	var smapi *core.ConvertedCollectionItem
	for i := range c.Items {
		if c.Items[i].ModID == "2400" {
			smapi = &c.Items[i]
		}
	}
	require.NotNil(t, smapi, "the recorded recipe lists SMAPI")
	assert.Equal(t, core.CollectionItemExternal, smapi.State)
	assert.Contains(t, smapi.Instructions, "install on Linux.sh")
	assert.Contains(t, smapi.URL, "https://www.nexusmods.com/stardewvalley/mods/2400")
	require.NotEmpty(t, c.Document.External)

	c = convertNexusFixture(t, fo4, "pmmttm", "pmmttm", 2, "", "fallout4-pmmttm-2.json", core.CollectionConvertOptions{})
	assert.Empty(t, c.Document.External, "the optional F4SE item is off by default")
	c = convertNexusFixture(t, fo4, "pmmttm", "pmmttm", 2, "", "fallout4-pmmttm-2.json", core.CollectionConvertOptions{WithOptional: true})
	require.Len(t, c.Document.External, 1)
	assert.Equal(t, "https://f4se.silverlock.org", c.Document.External[0].URL)
}

// The Skyrim sample planned and applied (#607, P3 review) for a game the
// bethesda adapter cannot identify, so lmm writes no plugin list for it: it
// is refused for its plugin list (#611: the converter gates plugin_order
// per game, though the lmm document format supports it since #616) and for
// Scrambled Bugs' two installers - and the refusal's supported list never
// names plugin_order while refusing it.
func TestNexusCollection_SkyrimApplyRefusesAndNamesWhatIsMissing(t *testing.T) {
	game := &domain.Game{ID: "skyrimse", Name: "Skyrim Special Edition", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Adapter: "bethesda", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	sandboxHome(t)
	srv, client := nexusCollectionServer(t, "xk05aw", 325, "revision-skyrimspecialedition-xk05aw-325.json", "skyrimspecialedition-xk05aw-325.json")
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(client, "testapikey"))
	RegisterAdapters(svc)
	require.NoError(t, svc.SaveGame(context.Background(), game))

	plan, err := svc.PlanSourceCollectionImport(context.Background(), game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "xk05aw"})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	var refused *core.CollectionUnsupportedError
	require.ErrorAs(t, err, &refused)
	var names []string
	for _, f := range refused.Unsupported {
		names = append(names, f.Feature)
	}
	assert.Equal(t, []string{core.CollectionFeatureFomodMultiFile, core.CollectionFeaturePluginOrder}, names)
	assert.NotContains(t, refused.Supported, core.CollectionFeaturePluginOrder, "never supported and refused at once")
	assert.Contains(t, refused.Supported, core.CollectionFeatureFomod)
	_, err = svc.NewProfileManager().Get(context.Background(), game.ID, plan.Profile.Name)
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "nothing was written")
}
