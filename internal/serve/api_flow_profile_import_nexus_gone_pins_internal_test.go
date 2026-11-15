package serve

// #657: a NexusMods collection pinning a file NexusMods no longer offers,
// through the entry point the web UI drives. The plan carries the gone pin
// with the mod's current main file; a job started without a choice
// refuses, writing nothing; the plan view's choice re-plans
// (use_current / skip), and the job then saves the profile as decided.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goneNexusFixtureSource is nexusFixtureSource whose list has no record
// of Ten's pinned file (its author removed it), and whose listing offers
// only 101, Ten's main file.
type goneNexusFixtureSource struct{ *nexusFixtureSource }

func (s goneNexusFixtureSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := s.nexusFixtureSource.ResolveGameCollection(ctx, gameSourceID, ref)
	if len(c.Items) > 0 {
		c.Items[0].ModID = ""
	}
	return c, err
}

func (goneNexusFixtureSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID != "10" {
		return nil, nil
	}
	return []domain.DownloadableFile{
		{ID: "101", FileName: "ten-2.zip", Name: "Ten Two", Version: "2.0", Category: "MAIN", IsPrimary: true},
	}, nil
}

func seedGoneNexusCollection(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	src := seedNexusCollection(t, svc, game, source.TierFree)
	svc.RegisterSource(goneNexusFixtureSource{nexusFixtureSource: src})
}

// #660 owner ruling: a gone pin with a current file defaults to it - the
// plan carries the choice, marked the default - and the import goes ahead
// with no decision first.
func TestAPIFlow_ProfileImportNexus_GonePinDefaultsToTheCurrentFile(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedGoneNexusCollection(t, svc, game)

	planID, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef"}`)
	c := nexusPlanSource(t, raw)
	require.Len(t, c.GonePins, 1)
	assert.Equal(t, "nexusmods:10", c.GonePins[0].Key())
	require.NotNil(t, c.GonePins[0].Current)
	assert.Equal(t, "101", c.GonePins[0].Current.FileID)
	assert.Equal(t, core.GonePinUseCurrent, c.GonePins[0].Choice)
	assert.True(t, c.GonePins[0].Default)

	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, []string{"101"}, saved.FindRef("nexusmods", "10").FileIDs)
}

func TestAPIFlow_ProfileImportNexus_GonePinUseCurrent(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedGoneNexusCollection(t, svc, game)

	planID, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","use_current":["nexusmods:10"]}`)
	c := nexusPlanSource(t, raw)
	require.Len(t, c.GonePins, 1)
	assert.Equal(t, core.GonePinUseCurrent, c.GonePins[0].Choice)
	assert.Equal(t, []string{"nexusmods:10"}, c.UseCurrent)

	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Three Mods")
	require.NoError(t, err)
	ten := saved.FindRef("nexusmods", "10")
	require.NotNil(t, ten)
	assert.Equal(t, []string{"101"}, ten.FileIDs)
	assert.Equal(t, &domain.CollectionPin{Version: "1.0", FileIDs: []string{"100"}}, ten.SubstitutedFor)
}

func TestAPIFlow_ProfileImportNexus_GonePinSkip(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedGoneNexusCollection(t, svc, game)

	planID, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","use_current_all":true,"skip":["nexusmods:10"]}`)
	c := nexusPlanSource(t, raw)
	assert.Equal(t, core.GonePinSkip, c.GonePins[0].Choice)
	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "10"))
	require.NotNil(t, saved.CollectionBaseline)
	assert.Len(t, saved.CollectionBaseline.Skipped, 1)
}

func TestAPIFlow_ProfileImportNexus_GonePinBadRequests(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedGoneNexusCollection(t, svc, game)
	for name, body := range map[string]string{
		"both ways":           `{"nexus_collection":"abcdef","use_current":["nexusmods:10"],"skip":["nexusmods:10"]}`,
		"no gone pin":         `{"nexus_collection":"abcdef","skip":["nexusmods:11"]}`,
		"skip_file on a main": `{"nexus_collection":"abcdef","skip_file":["nexusmods:10"]}`,
		"without a nexus ref": `{"data":"name: x\ngame_id: g1\nmods: []\n","skip":["nexusmods:10"]}`,
		"with a url":          `{"url":"https://example.invalid/c.lmmc","use_current_all":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// revisionFourGoneSource serves the collection at revision 4, which adds
// mod 13 at a file NexusMods no longer offers (and still pins Ten's gone
// file).
type revisionFourGoneSource struct{ goneNexusFixtureSource }

func (s revisionFourGoneSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := s.goneNexusFixtureSource.ResolveGameCollection(ctx, gameSourceID, ref)
	c.Revision = 4
	// NexusMods has no record of 13's file: its author removed it.
	c.Items = append(c.Items, source.CollectionItem{Kind: source.CollectionItemModFile, FileID: "130"})
	return c, err
}

func (s revisionFourGoneSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID == "13" {
		return []domain.DownloadableFile{{ID: "131", FileName: "thirteen-2.zip", Version: "2.0", Category: "MAIN", IsPrimary: true}}, nil
	}
	return s.goneNexusFixtureSource.GetModFiles(ctx, mod)
}

// TestFlowCollectionUpdate_GonePins (#657): the update plan lists the mod
// the import skipped as kept out, carries the new version's gone pin, and
// the plan view's choice re-plans it before the job applies it.
func TestFlowCollectionUpdate_GonePins(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedGoneNexusCollection(t, svc, game)
	j := runFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","skip":["nexusmods:10"]}`, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	svc.RegisterSource(revisionFourGoneSource{goneNexusFixtureSource: src.(goneNexusFixtureSource)})
	seedNexusRevision(t, svc, game, 4, `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "latest"},
	{"Name": "Thirteen", "Version": "1.0", "SourceType": "nexus", "ModID": "13", "FileID": "130", "UpdatePolicy": "exact"}]}`)

	_, raw := planFlow(t, s, game, "collection_update", `{"profile":"Three Mods"}`)
	var resp struct {
		Plan core.CollectionUpdatePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Len(t, resp.Plan.Skipped, 1)
	assert.Equal(t, "nexusmods:10", resp.Plan.Skipped[0].Key())
	require.NotNil(t, resp.Plan.Source)
	require.Len(t, resp.Plan.Source.GonePins, 1)
	assert.Equal(t, "nexusmods:13", resp.Plan.Source.GonePins[0].Key())

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/collection_update", game),
		`{"profile":"Three Mods","use_current":["nexusmods:13"],"skip":["nexusmods:13"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	j = runFlow(t, s, game, "collection_update", `{"profile":"Three Mods","use_current":["nexusmods:13"]}`, `{}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Three Mods")
	require.NoError(t, err)
	thirteen := saved.FindRef("nexusmods", "13")
	require.NotNil(t, thirteen)
	assert.Equal(t, []string{"131"}, thirteen.FileIDs)
	assert.NotNil(t, thirteen.SubstitutedFor)
	assert.Nil(t, saved.FindRef("nexusmods", "10"), "still skipped")
}

// seedNexusRevision caches recipe as the collection's revision rev.
func seedNexusRevision(t *testing.T, svc *core.Service, game *domain.Game, rev int, recipe string) {
	t.Helper()
	entry := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "nexusmods-abcdef", strconv.Itoa(rev))
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(recipe), 0o644))
}

// reofferedTenSource serves revision 4, which pins Ten anew at file 102,
// available.
type reofferedTenSource struct{ goneNexusFixtureSource }

func (s reofferedTenSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := s.nexusFixtureSource.ResolveGameCollection(ctx, gameSourceID, ref)
	c.Revision = 4
	c.Items[0].FileID = "102" // listed, as MAIN: available
	return c, err
}

// TestFlowCollectionUpdate_ASkippedModComesBack (#657): a mod the import
// skipped, which the new version pins at an available file, is in the
// plan's reoffered list and added by default; the plan view's toggle
// re-plans with keep_skipped, which keeps it out.
func TestFlowCollectionUpdate_ASkippedModComesBack(t *testing.T) {
	for _, keep := range []bool{false, true} {
		s, svc, game := newFlowFixtureServer(t)
		seedGoneNexusCollection(t, svc, game)
		j := runFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef","skip":["nexusmods:10"]}`, `{"install":true}`)
		require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
		src, err := svc.GetSource("nexusmods")
		require.NoError(t, err)
		svc.RegisterSource(reofferedTenSource{src.(goneNexusFixtureSource)})
		seedNexusRevision(t, svc, game, 4, `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "3.0", "SourceType": "nexus", "ModID": "10", "FileID": "102", "UpdatePolicy": "exact"},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "latest"}]}`)

		body := `{"profile":"Three Mods"}`
		if keep {
			body = `{"profile":"Three Mods","keep_skipped":["nexusmods:10"]}`
		}
		_, raw := planFlow(t, s, game, "collection_update", body)
		var resp struct {
			Plan core.CollectionUpdatePlan `json:"plan"`
		}
		require.NoError(t, json.Unmarshal(raw, &resp))
		require.Len(t, resp.Plan.Reoffered, 1)
		assert.Equal(t, "nexusmods:10", resp.Plan.Reoffered[0].Key())
		assert.Contains(t, resp.Plan.Reoffered[0].Note, "which is available")
		assert.Equal(t, keep, resp.Plan.Reoffered[0].KeepSkipped)

		j = runFlow(t, s, game, "collection_update", body, `{}`)
		require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
		saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Three Mods")
		require.NoError(t, err)
		if keep {
			assert.Nil(t, saved.FindRef("nexusmods", "10"))
			assert.Len(t, saved.CollectionBaseline.Skipped, 1)
			continue
		}
		ten := saved.FindRef("nexusmods", "10")
		require.NotNil(t, ten, "added by default")
		assert.Equal(t, []string{"102"}, ten.FileIDs)
		assert.Empty(t, saved.CollectionBaseline.Skipped)
	}
}
