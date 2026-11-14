package serve

// #637: the web UI's install picker selects several files of one mod, and
// the job's file_ids carry them all - the counterpart of `lmm install
// --file a,b`. These pin the two halves over /api/v1: every named file is
// installed and deployed, and a selection core refuses comes back as the
// failed job with core's own message, nothing installed.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowInstall_SeveralFilesAreAllInstalled installs a mod's main file
// and its patch in one job.
func TestFlowInstall_SeveralFilesAreAllInstalled(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	src.mods["mf"] = &installSourceMod{
		mod: domain.Mod{ID: "mf", SourceID: fixtureSourceID, Name: "Main And Patch", Version: "1.0", GameID: game.ID},
		files: []domain.DownloadableFile{
			{ID: "mf1", Name: "Main", FileName: "main.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 64},
			{ID: "mf2", Name: "Patch", FileName: "patch.zip", Version: "1.0", Category: "OPTIONAL", Size: 64},
		},
		members: map[string]string{"mf1": "Mods/main.pak", "mf2": "Mods/patch.pak"},
	}

	j := runFlow(t, s, game, "install", installPlanBody("mf"), `{"version":"1.0","file_ids":["mf1","mf2"]}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "mf", game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"mf1", "mf2"}, installed.FileIDs, "both files are recorded")
	assert.Equal(t, "payload for mf/mf1", deployedContent(t, game, "Mods/main.pak"))
	assert.Equal(t, "payload for mf/mf2", deployedContent(t, game, "Mods/patch.pak"))

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	ref := profile.FindRef(fixtureSourceID, "mf")
	require.NotNil(t, ref)
	assert.ElementsMatch(t, []string{"mf1", "mf2"}, ref.FileIDs, "the profile names both files")
}

// TestFlowInstall_SeveralFilesRefusedLikeTheCLI picks two files the CLI
// refuses together (#211: a merge-compile game's pak and exmodz are
// alternate forms of one mod). The job fails with core's message - the
// one `lmm install --file` prints - and installs nothing.
func TestFlowInstall_SeveralFilesRefusedLikeTheCLI(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	app.RegisterAdapters(svc) // what the real binary resolves against
	game.Adapter = "icarus"
	require.NoError(t, svc.SaveGame(t.Context(), game))
	src.mods["alt"] = &installSourceMod{
		mod: domain.Mod{ID: "alt", SourceID: fixtureSourceID, Name: "Two Forms", Version: "1.0", GameID: game.ID},
		files: []domain.DownloadableFile{
			{ID: "ex", Name: "Exmodz", FileName: "Mod.exmodz", Version: "1.0", Category: "EXMODZ", IsPrimary: true, Size: 64},
			{ID: "pak", Name: "Pak", FileName: "Mod_P.pak", Version: "1.0", Category: "PAK", Size: 64},
		},
		members: map[string]string{"ex": "Mods/mod.exmodz", "pak": "Mods/Mod_P.pak"},
	}
	before := src.downloadCount()

	j := runFlow(t, s, game, "install", installPlanBody("alt"), `{"version":"1.0","file_ids":["ex","pak"]}`)
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.Equal(t, "Mod_P.pak and Mod.exmodz are alternate forms of the same mod - select one", j.status().Error.Error)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "alt", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a refused selection installs nothing")
	assert.Equal(t, before, src.downloadCount(), "nor downloads anything")
}
