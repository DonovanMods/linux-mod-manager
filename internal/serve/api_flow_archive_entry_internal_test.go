package serve

// #644 through the entry points the SPA drives: an archive import or an
// update from a file never changes another profile's cache entry - an
// archive no source file is gets an entry of its own, and an ingest
// commits its tree only once nothing refuses it.

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherProfileOnPlainEntry gives a second profile Mod One 2.0 built from
// the source's file 200: the plain entry a download writes.
func otherProfileOnPlainEntry(t *testing.T, svc *core.Service, game *domain.Game) cache.Key {
	t.Helper()
	ctx := t.Context()
	gc := svc.GetGameCache(game)
	plain := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "2.0"}
	require.NoError(t, gc.Store(plain, deployFixtureFile, []byte("source bytes")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(plain), "200", []string{deployFixtureFile}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(ctx, game.ID, "other", domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: "2.0", FileIDs: []string{"200"}}))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "2.0", GameID: game.ID}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"200"},
	}))
	return plain
}

func requirePlainEntryUntouched(t *testing.T, svc *core.Service, game *domain.Game, k cache.Key) {
	t.Helper()
	gc := svc.GetGameCache(game)
	data, err := os.ReadFile(filepath.Join(gc.ModPath(k), deployFixtureFile))
	require.NoError(t, err)
	assert.Equal(t, "source bytes", string(data), "another profile's tree must not change")
	assert.True(t, gc.HasFileIDs(k, []string{"200"}))
}

// The web import with a source and mod ID, of an archive that is none of
// the source's files: its own entry, never the other profile's plain one.
func TestAPIFlow_ImportArchive_UnmatchedArchiveKeepsAnotherProfilesEntry(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	plain := otherProfileOnPlainEntry(t, svc, game)
	_, err := svc.UninstallMod(t.Context(), game, "default", fixtureSourceID, "m1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)

	id := uploadArchive(t, s, "ModOne-custom-2.0.zip", map[string]string{deployFixtureFile: "custom bytes"})
	j := runFlow(t, s, game, "import_archive", `{"upload_id":"`+string(id)+`","source_id":"`+fixtureSourceID+`","mod_id":"m1"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	requirePlainEntryUntouched(t, svc, game, plain)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.NotEmpty(t, row.Variant)
	assert.Empty(t, row.FileIDs)
	assert.Equal(t, "custom bytes", deployedContent(t, game, deployFixtureFile))
}

// The web update from a file, of an archive that is none of the source's
// files ("Update anyway"): the same.
func TestAPIFlow_UpdateFromArchive_UnmatchedArchiveKeepsAnotherProfilesEntry(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	plain := otherProfileOnPlainEntry(t, svc, game)

	id := uploadArchive(t, s, "ModOne-custom-2.0.zip", map[string]string{deployFixtureFile: "custom bytes"})
	j := runFlow(t, s, game, "update_from_archive", updateFromArchiveBody(id), `{"accept_mismatch":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	requirePlainEntryUntouched(t, svc, game, plain)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.NotEmpty(t, row.Variant)
	assert.Equal(t, "custom bytes", deployedContent(t, game, deployFixtureFile))
}

// An update from the source's own file whose install.before_each hook
// fails: the ingest would have replaced the other profile's entry (it is
// that file's), and a refusal commits none of it.
func TestAPIFlow_UpdateFromArchive_RefusalLeavesAnotherProfilesEntry(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	plain := otherProfileOnPlainEntry(t, svc, game)
	hook := filepath.Join(t.TempDir(), "fail.sh")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	game.Hooks.Install.BeforeEach = hook
	require.NoError(t, svc.SaveGame(t.Context(), game))

	id := uploadArchive(t, s, "ModOne-2.0.zip", map[string]string{deployFixtureFile: "another build"})
	j := runFlow(t, s, game, "update_from_archive", updateFromArchiveBody(id), "")
	require.Equal(t, jobFailed, j.status().State)

	requirePlainEntryUntouched(t, svc, game, plain)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version)
}

// The review's probe through the web UI: a profile listing the mod at the
// version, with no row of its own, is not handed another profile's
// imported archive by "Apply profile" - it installs from the source.
func TestAPIFlow_ProfileApply_NeverBorrowsAnotherProfilesArchive(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	_, err := svc.UninstallMod(t.Context(), game, "default", fixtureSourceID, "m1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	id := uploadArchive(t, s, "ModOne-custom-2.0.zip", map[string]string{deployFixtureFile: "custom bytes"})
	j := runFlow(t, s, game, "import_archive", `{"upload_id":"`+string(id)+`","source_id":"`+fixtureSourceID+`","mod_id":"m1"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	pm := svc.NewProfileManager()
	_, err = pm.Create(t.Context(), game.ID, "third")
	require.NoError(t, err)
	j = runFlow(t, s, game, "switch", `{"profile":"third"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	require.NoError(t, pm.UpsertMod(t.Context(), game.ID, "third", domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: "2.0", FileIDs: []string{"200"}}))

	_, raw := planFlow(t, s, game, "profile_apply", `{"profile":"third"}`)
	var planned struct {
		Plan core.ProfileApplyPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &planned))
	for _, im := range planned.Plan.ToEnable {
		assert.NotEqual(t, "m1", im.ID, "default's custom build must not be enabled in third")
	}
	require.Len(t, planned.Plan.ToInstall, 1)
	assert.Equal(t, "m1", planned.Plan.ToInstall[0].Ref.ModID)
}
