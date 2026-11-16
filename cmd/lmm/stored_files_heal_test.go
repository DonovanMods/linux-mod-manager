package main

// #662: the profile plans name each mod whose stored main files are healed
// to one version, and each collection mod that installs the curator's
// mixed files as chosen.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoredFileLines(t *testing.T) {
	lines := storedFileLines(
		[]core.StoredFilesHeal{
			{SourceID: "src", ModID: "1", Name: "Mod One", Version: "2.0", Dropped: []core.MixedVersion{
				{Version: "1.0", Files: []core.MixedVersionFile{{ID: "11", FileName: "one-1.0.zip"}, {ID: "12"}}},
			}},
			{SourceID: "src", ModID: "2", Version: "3.0", RecordedVersion: "0.9", Dropped: []core.MixedVersion{
				{Version: "2.0", Files: []core.MixedVersionFile{{ID: "21", FileName: "two-2.0.zip"}}},
			}},
		},
		[]core.CuratorMixedFiles{{SourceID: "src", ModID: "3", Name: "Curated", Versions: []core.MixedVersion{
			{Version: "1.0", Files: []core.MixedVersionFile{{ID: "31", FileName: "c-1.0.zip"}}},
			{Version: "1.1", Files: []core.MixedVersionFile{{ID: "32", FileName: "c-1.1.zip"}}},
		}}},
	)
	assert.Equal(t, []string{
		"Will heal 2 mod(s) whose stored main files are of more than one version:",
		"  ~ Mod One: keeps version 2.0, drops one-1.0.zip (1.0), 12 (1.0)",
		"  ~ src:2: keeps the newest, 3.0 (none is at the recorded 0.9), drops two-2.0.zip (2.0)",
		"1 collection mod(s) install the curator's files, which mix versions:",
		"  = Curated: 1.0 (c-1.0.zip); 1.1 (c-1.1.zip)",
	}, lines)
	assert.Nil(t, storedFileLines(nil, nil))
}

func TestStoredFileLines_SanitizesNames(t *testing.T) {
	lines := storedFileLines([]core.StoredFilesHeal{{SourceID: "src", ModID: "1", Name: "Mod\x1b[31m", Version: "2.0", Dropped: []core.MixedVersion{
		{Version: "1.0", Files: []core.MixedVersionFile{{ID: "11", FileName: "a\u202e.zip"}}},
	}}}, nil)
	for _, l := range lines {
		assert.NotContains(t, l, "\x1b")
		assert.NotContains(t, l, "\u202e")
	}
}

// setupMixedRefCLITest is setupTwoVersionInstall with the active profile
// listing mod1 at 2.0 by a stored set mixing the 2.0 and 1.0 main files
// with the 1.0 patch.
func setupMixedRefCLITest(t *testing.T, fromCollection bool) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, _ := setupTwoVersionInstall(t)
	pm := getProfileManager(svc)
	ctx := context.Background()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "2.0", FileIDs: []string{"main2", "main1", "patch1"}, FromCollection: fromCollection,
	}))
	return svc, game
}

func TestDoProfileApply_DryRun_NamesTheHeal(t *testing.T) {
	svc, game := setupMixedRefCLITest(t, false)
	withProfileDryRun(t, &profileApplyDryRun)
	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.Contains(t, out, "Will heal 1 mod(s) whose stored main files are of more than one version:\n"+
		"  ~ Mod One: keeps version 2.0, drops main-1.0.esp (1.0)\n")
}

func TestDoProfileApply_DryRun_NamesTheCuratorsMixedFiles(t *testing.T) {
	svc, game := setupMixedRefCLITest(t, true)
	withProfileDryRun(t, &profileApplyDryRun)
	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.NotContains(t, out, "Will heal")
	assert.Contains(t, out, "1 collection mod(s) install the curator's files, which mix versions:\n"+
		"  = Mod One: 2.0 (main-2.0.esp); 1.0 (main-1.0.esp)\n")
}

// TestDoProfileApply_HealsAndRecords runs the apply: the profile records
// the healed ids, and a second apply has nothing to do.
func TestDoProfileApply_HealsAndRecords(t *testing.T) {
	svc, game := setupMixedRefCLITest(t, false)
	old := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = old })
	out, err := captureStdoutErr(t, func() error { return doProfileApply(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)

	p, err := getProfileManager(svc).Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef("test-src", "mod1")
	require.NotNil(t, ref)
	assert.Equal(t, []string{"main2", "patch1"}, ref.FileIDs)

	out, err = captureStdoutErr(t, func() error { return doProfileApply(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "System already matches profile default.")
}

// The --dry-run --json document carries the plan's heals.
func TestJSONGolden_ProfileApplyDryRunHeals(t *testing.T) {
	svc, game := setupMixedRefCLITest(t, false)
	withProfileDryRun(t, &profileApplyDryRun)
	out := runJSONCommand(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assertJSONCLIGolden(t, "profile_apply_dry_run_heals", out, game.ModPath, "<MODPATH>")
}

func TestDoProfileSwitch_DryRun_NamesTheHeal(t *testing.T) {
	svc, game, _ := setupTwoVersionInstall(t)
	pm := getProfileManager(svc)
	ctx := context.Background()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err = pm.Create(ctx, game.ID, "stable")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "stable", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.0", FileIDs: []string{"main2", "main1", "patch1"},
	}))
	withProfileDryRun(t, &profileSwitchDryRun)
	out := captureStdout(t, func() error {
		return doProfileSwitch(ctx, svc, game, "stable")
	})
	assert.Contains(t, out, "Will heal 1 mod(s) whose stored main files are of more than one version:\n"+
		"  ~ Mod One: keeps version 1.0, drops main-2.0.esp (2.0)\n")
}
