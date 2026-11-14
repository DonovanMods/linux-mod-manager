package core_test

// #643: a same-version update whose replacement map supersedes the old main
// (fileA -> fileC) while the source still lists fileA as MAIN must keep the
// unchanged optional (fileB). The selection used to offer fileA - already a
// key of the replacement map - as the replacement for fileB, recording
// [fileC fileA]; since #639 the record is what deploys, so fileB vanished
// from the game directory and the superseded fileA came back.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedOptionalKeptMod seeds mod1 installed from {fileA (MAIN), fileB
// (OPTIONAL)} at 1.0, with a source that lists fileA still as MAIN beside
// the new primary fileC at the same version, and returns the same-version
// update that supersedes fileA with fileC.
func seedOptionalKeptMod(t *testing.T) (*core.Service, *domain.Game, domain.Update) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA", "fileB"},
		map[string][]byte{"mod1-fileA.esp": []byte("A"), "mod1-fileB.esp": []byte("B")})
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "A", FileName: "mod1-fileA.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "B", FileName: "mod1-fileB.esp", Version: "1.0", Category: "OPTIONAL"},
			{ID: "fileC", Name: "C", FileName: "mod1-fileC.esp", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileA", []byte("A"))
	mock.AddDownload("fileB", []byte("B"))
	mock.AddDownload("fileC", []byte("C"))
	return svc, game, domain.Update{InstalledMod: *old, NewVersion: "1.0", FileIDReplacements: map[string]string{"fileA": "fileC"}}
}

// assertOptionalKept checks the record (DB row and profile ref) and the
// deployed tree, both straight after the update and after a fresh deploy.
func assertOptionalKept(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := context.Background()
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fileC", "fileB"}, row.FileIDs, "the superseded main is replaced; the unchanged optional keeps itself")
	profile, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "mod1")
	require.NotNil(t, ref)
	assert.ElementsMatch(t, []string{"fileC", "fileB"}, ref.FileIDs)

	for _, when := range []string{"after the update", "after a deploy"} {
		if when == "after a deploy" {
			_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
		}
		assert.False(t, deployedFile(t, game, "mod1-fileA.esp"), "%s: the superseded main stays retired", when)
		assert.True(t, deployedFile(t, game, "mod1-fileB.esp"), "%s: the unchanged optional stays deployed", when)
		assert.True(t, deployedFile(t, game, "mod1-fileC.esp"), "%s: the new main is deployed", when)
	}
}

func TestApplyUpdate_SameVersionKeepsTheUnchangedOptional(t *testing.T) {
	svc, game, upd := seedOptionalKeptMod(t)
	ctx := context.Background()
	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", upd)
	require.NoError(t, err)
	res, err := svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.UpdateUpdated, res.Status)
	assertOptionalKept(t, svc, game)
}

func TestApplyUpdateBatch_SameVersionKeepsTheUnchangedOptional(t *testing.T) {
	svc, game, upd := seedOptionalKeptMod(t)
	ctx := context.Background()
	plan, err := svc.PlanUpdateBatchFrom(ctx, game, "default", []domain.Update{upd}, nil)
	require.NoError(t, err)
	res, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Failed, "%+v", res.Failed)
	assertOptionalKept(t, svc, game)
}
