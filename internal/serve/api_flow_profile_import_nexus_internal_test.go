package serve

// The NexusMods collection half of the profile-import flow (#607): POST
// /api/v1/plans/profile_import with a nexus_collection reference, then POST
// /api/v1/jobs, with the assertions on END STATE. The source is a double
// (internal/serve may not import a concrete source), and the collection's
// recipe is already in its cache entry, so no 7z and no network is needed:
// core reads a cached recipe exactly as a fetched one.

import (
	"context"
	"encoding/json/v2"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nexusFixtureSource is a source with one revisioned collection, abcdef
// rev 3, two required items and one optional.
type nexusFixtureSource struct {
	fixtureSource
	tier   source.Tier
	gotRef string
}

func (*nexusFixtureSource) ID() string   { return "nexusmods" }
func (*nexusFixtureSource) Name() string { return "NexusMods" }

func (s *nexusFixtureSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	s.gotRef = ref
	if ref == "nope" {
		return source.Collection{}, source.ErrInvalidReference
	}
	return source.Collection{ID: "abcdef", Name: "Three Mods", Revision: 3, Author: "curator",
		URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/abcdef", Handle: "/h",
		Items: []source.CollectionItem{
			{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten",
				URL: "https://www.nexusmods.com/" + gameSourceID + "/mods/10?tab=files&file_id=100"},
		}}, nil
}

func (*nexusFixtureSource) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	return "", source.ErrNotSupported // the recipe is cached; never fetched
}

func (*nexusFixtureSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := fs.ReadFile(root, "collection.json")
	if err != nil {
		return source.CollectionManifest{}, err
	}
	var m source.CollectionManifest
	return m, json.Unmarshal(data, &m)
}

func (s *nexusFixtureSource) AccountTier(context.Context) (source.Tier, error) { return s.tier, nil }

const nexusFixtureRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "latest"},
	{"Name": "Twelve", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "exact", "Optional": true}]}`

// seedNexusCollection registers the double for game and puts the
// collection's recipe in its cache entry.
func seedNexusCollection(t *testing.T, svc *core.Service, game *domain.Game, tier source.Tier) *nexusFixtureSource {
	t.Helper()
	src := &nexusFixtureSource{tier: tier}
	svc.RegisterSource(src)
	if game.SourceIDs == nil {
		game.SourceIDs = map[string]string{}
	}
	game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, svc.SaveGame(t.Context(), game))
	root := filepath.Join(svc.GlobalCacheDir(), game.ID)
	if game.CachePath != "" {
		root = game.CachePath
	}
	entry := filepath.Join(root, "collections", "nexusmods-abcdef", "3")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(nexusFixtureRecipe), 0o644))
	return src
}

func nexusPlanSource(t *testing.T, raw []byte) *core.ConvertedCollection {
	t.Helper()
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Plan.Collection)
	require.NotNil(t, resp.Plan.Collection.Source, "the plan carries the source collection")
	return resp.Plan.Collection.Source
}

func TestAPIFlow_ProfileImportNexus_PlansTheCollection(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	src := seedNexusCollection(t, svc, game, source.TierPremium)

	_, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef"}`)
	assert.Equal(t, "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", src.gotRef, "forwarded verbatim")
	c := nexusPlanSource(t, raw)
	assert.Equal(t, "abcdef", c.Slug)
	assert.Equal(t, 3, c.Revision)
	assert.Equal(t, map[core.CollectionItemState]int{core.CollectionItemWillInstall: 2, core.CollectionItemOptionalSkipped: 1}, c.Counts)

	// The plan view's per-item toggle re-plans with the item opted in.
	_, raw = planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","optional":["12:120"]}`)
	c = nexusPlanSource(t, raw)
	assert.Equal(t, 3, c.Counts[core.CollectionItemWillInstall])
	assert.Equal(t, []string{"12:120"}, c.Optional)

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "Three Mods")
	require.ErrorIs(t, err, domain.ErrProfileNotFound, "a plan imports nothing")
}

func TestAPIFlow_ProfileImportNexus_FreeAccountSavesThePending(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedNexusCollection(t, svc, game, source.TierFree)

	planID, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","with_optional":true}`)
	c := nexusPlanSource(t, raw)
	assert.True(t, c.ManualDownload)
	assert.Equal(t, 3, c.Counts[core.CollectionItemManualDownload])

	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Zero(t, result.Installed, "a free account downloads nothing")
	assert.NotEmpty(t, result.Notes)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 3)
	assert.Equal(t, "nexusmods:abcdef", profile.Collection.ID)
	installed, err := svc.GetInstalledMods(t.Context(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Empty(t, installed)
}

func TestAPIFlow_ProfileImportNexus_BadRequests(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedNexusCollection(t, svc, game, source.TierPremium)
	for name, body := range map[string]string{
		"unknown collection":       `{"nexus_collection":"nope"}`,
		"with a document":          `{"nexus_collection":"abcdef","data":"name: x"}`,
		"with a workshop ref":      `{"nexus_collection":"abcdef","workshop_collection":"1"}`,
		"optional without a nexus": `{"data":"name: x\ngame_id: g1\nmods: []\n","with_optional":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}
