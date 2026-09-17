package core_test

// #429: `lmm verify` said "No installed mods to verify." for a game with 20
// Workshop items tracked from Steam and a declared loader, and for a
// profile of checksum-less imported mods - the empty-profile branch keyed on
// "nothing checksummed", not "nothing installed". The result now says how
// many mods the run covered, names each external item, counts the mods lmm
// has nothing recorded for, and still runs the loader tier.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerify_AnAllExternalLoaderGameIsNotEmpty(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx})
	bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapNative, time.Time{}) // never ran
	seedExternalMod(t, svc, game, "default", "3000000001", "ModMenu")
	gone := seedExternalMod(t, svc, game, "default", "3000000002", "Unsubscribed")
	require.NoError(t, os.RemoveAll(gone))

	var begin *core.VerifyEvent
	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, func(e core.Event) {
		if ev, ok := e.(core.VerifyEvent); ok && ev.Kind == core.VerifyEvBegin {
			begin = &ev
		}
	})
	require.NoError(t, err)
	result := report.Result

	assert.False(t, result.HasFiles, "nothing is checksummed")
	assert.Equal(t, 2, result.Mods, "but two mods are installed")
	assert.Equal(t, 2, result.External, "and both were checked for presence")
	require.NotNil(t, begin)
	assert.Equal(t, 2, begin.Mods, "the run says so as it starts, before any row")

	present := findingWithStatus(result, "ok")
	require.NotNil(t, present, "a present item is named, not skipped: %v", findingStatuses(result))
	assert.Equal(t, "3000000001", present.ModID)
	assert.True(t, present.External)
	assert.Contains(t, present.Note, "tracked from Steam")

	missing := findingWithStatus(result, "external_missing")
	require.NotNil(t, missing)
	assert.Equal(t, "3000000002", missing.ModID)
	assert.True(t, missing.External)

	assert.NotNil(t, findingWithStatus(result, "loader_never_ran"), "the loader tier runs for the game: %v", findingStatuses(result))
}

func TestVerify_ChecksumlessImportedModsAreCountedNotEmpty(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "imp", SourceID: domain.SourceLocal, Name: "Imported", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	seedProfileWithMod(t, svc, game.ID, "default", domain.SourceLocal, "imp", "1.0")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.False(t, report.Result.HasFiles)
	assert.Equal(t, 1, report.Result.Mods)
	assert.Equal(t, 1, report.Result.Unverified, "an imported mod with no recorded files has nothing to compare")
	assert.Zero(t, report.Result.Issues)
	assert.Zero(t, report.Result.Warnings)
}

// TestVerify_ModCountsFollowTheModFilter: a run naming one mod covers one.
func TestVerify_ModCountsFollowTheModFilter(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3000000001", "ModMenu")
	seedExternalMod(t, svc, game, "default", "3000000002", "Other")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true, ModFilter: "3000000002"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Result.Mods)
	assert.Equal(t, 1, report.Result.External)
	require.Len(t, report.Result.Findings, 1)
	assert.Equal(t, "3000000002", report.Result.Findings[0].ModID)
}

func TestVerify_AnEmptyProfileIsEmpty(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedProfileWithMod(t, svc, game.ID, "default", "src", "never-installed", "1.0")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, report.Result.Mods)
	assert.Zero(t, report.Result.External)
	assert.Zero(t, report.Result.Unverified)
	assert.Empty(t, report.Result.Findings)
}
