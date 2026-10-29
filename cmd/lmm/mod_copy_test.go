package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDoModCopyTest reuses the lock fixture and resets mod copy's flags.
func setupDoModCopyTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, _ := setupDoModLockTest(t)
	copyTo, copyAll, copyReplace, copyDryRun = "", false, false, false
	t.Cleanup(func() { copyTo, copyAll, copyReplace, copyDryRun = "", false, false, false })
	return svc, game
}

// seedCopyableMod seeds modID into "default" with its bytes in the cache.
func seedCopyableMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, version string) {
	t.Helper()
	seedLockableMod(t, svc, game, modID, name, version)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", modID, version, modID+".esp", []byte("data")))
}

func createCopyDest(t *testing.T, svc *core.Service, game *domain.Game, name string) {
	t.Helper()
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, name)
	require.NoError(t, err)
}

func TestDoModCopy_IntoInactiveProfile_RecordsOnly(t *testing.T) {
	svc, game := setupDoModCopyTest(t)
	seedCopyableMod(t, svc, game, "a", "Mod A", "1.0")
	createCopyDest(t, svc, game, "exp")
	copyTo = "exp"

	out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "a") })
	assert.Contains(t, out, "Mod A")
	assert.Contains(t, out, "Copied 1 mod(s) from default to exp")

	p, err := svc.NewProfileManager().Get(context.Background(), game.ID, "exp")
	require.NoError(t, err)
	require.NotNil(t, p.FindRef("src", "a"))
	assert.Equal(t, "1.0", p.FindRef("src", "a").Version)
	row, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "exp")
	require.NoError(t, err)
	assert.False(t, row.Deployed)
}

func TestDoModCopy_DryRunWritesNothing(t *testing.T) {
	svc, game := setupDoModCopyTest(t)
	seedCopyableMod(t, svc, game, "a", "Mod A", "1.0")
	createCopyDest(t, svc, game, "exp")
	copyTo, copyDryRun = "exp", true

	out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "a") })
	assert.Contains(t, out, "(dry run)")
	assert.Contains(t, out, "recorded only")

	p, err := svc.NewProfileManager().Get(context.Background(), game.ID, "exp")
	require.NoError(t, err)
	assert.Nil(t, p.FindRef("src", "a"))
	_, err = svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "exp")
	assert.Error(t, err)
}

func TestDoModCopy_AlreadyListedNeedsReplace(t *testing.T) {
	svc, game := setupDoModCopyTest(t)
	seedCopyableMod(t, svc, game, "a", "Mod A", "2.0")
	createCopyDest(t, svc, game, "exp")
	require.NoError(t, svc.NewProfileManager().AddMod(context.Background(), game.ID, "exp",
		domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"}))
	copyTo = "exp"

	err := doModCopy(context.Background(), svc, game, "a")
	require.Error(t, err)
	var listed *core.ModAlreadyListedError
	require.ErrorAs(t, err, &listed)
	assert.Contains(t, err.Error(), "--replace")

	copyReplace = true
	captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "a") })
	p, err := svc.NewProfileManager().Get(context.Background(), game.ID, "exp")
	require.NoError(t, err)
	assert.Equal(t, "2.0", p.FindRef("src", "a").Version)
}

func TestDoModCopy_All_SkipsListedAndUncached(t *testing.T) {
	svc, game := setupDoModCopyTest(t)
	seedCopyableMod(t, svc, game, "a", "Mod A", "1.0")
	seedCopyableMod(t, svc, game, "b", "Mod B", "1.0")
	seedLockableMod(t, svc, game, "c", "Mod C", "1.0") // not cached
	createCopyDest(t, svc, game, "exp")
	require.NoError(t, svc.NewProfileManager().AddMod(context.Background(), game.ID, "exp",
		domain.ModReference{SourceID: "src", ModID: "b", Version: "1.0"}))
	copyTo, copyAll = "exp", true

	out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "") })
	assert.Contains(t, out, "Copied 1 mod(s)")
	assert.Contains(t, out, "skipped 2")
	assert.Contains(t, out, "Mod C")

	p, err := svc.NewProfileManager().Get(context.Background(), game.ID, "exp")
	require.NoError(t, err)
	assert.NotNil(t, p.FindRef("src", "a"))
	assert.Nil(t, p.FindRef("src", "c"))
}

func TestDoModCopy_JSON(t *testing.T) {
	svc, game := setupDoModCopyTest(t)
	seedCopyableMod(t, svc, game, "a", "Mod A", "1.0")
	createCopyDest(t, svc, game, "exp")
	copyTo = "exp"
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "a") })
	var res core.CopyModResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Copied, 1)
}

func TestModCopyCmd_Args(t *testing.T) {
	copyAll = false
	t.Cleanup(func() { copyAll = false })
	assert.Error(t, modCopyCmd.Args(modCopyCmd, nil))
	assert.NoError(t, modCopyCmd.Args(modCopyCmd, []string{"a"}))
	copyAll = true
	assert.NoError(t, modCopyCmd.Args(modCopyCmd, nil))
	assert.Error(t, modCopyCmd.Args(modCopyCmd, []string{"a"}))
}

func TestDoProfileDuplicate_PlainAndRefusal(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "a"}))

	out := captureStdout(t, func() error {
		return doProfileDuplicate(context.Background(), svc, game, "default", "copy")
	})
	assert.Equal(t, "✓ Duplicated profile: default → copy\n", out)
	dup, err := pm.Get(context.Background(), game.ID, "copy")
	require.NoError(t, err)
	assert.Len(t, dup.Mods, 1)
	_, err = pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)

	err = doProfileDuplicate(context.Background(), svc, game, "default", "copy")
	require.ErrorIs(t, err, core.ErrProfileExists)
}

func TestJSONGolden_ProfileDuplicate(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, getProfileManager(svc).AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"}))
	withJSONOutput(t)
	out := captureStdout(t, func() error {
		return doProfileDuplicate(context.Background(), svc, game, "default", "copy")
	})
	assertJSONCLIGolden(t, "profile_duplicate", out, configDir, "/GOLDEN/config", dataDir, "/GOLDEN/data")
}

func TestJSONGolden_ModCopy(t *testing.T) {
	setup := func(t *testing.T) (*core.Service, *domain.Game) {
		svc, game := setupDoModCopyTest(t)
		seedCopyableMod(t, svc, game, "a", "Mod A", "1.0")
		seedCopyableMod(t, svc, game, "b", "Mod B", "1.0")
		require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))
		createCopyDest(t, svc, game, "exp")
		copyTo, copyAll = "exp", true
		withJSONOutput(t)
		return svc, game
	}
	t.Run("apply", func(t *testing.T) {
		svc, game := setup(t)
		out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "") })
		assertJSONCLIGolden(t, "mod_copy", out)
	})
	t.Run("dry_run", func(t *testing.T) {
		svc, game := setup(t)
		copyDryRun = true
		out := captureStdout(t, func() error { return doModCopy(context.Background(), svc, game, "") })
		assertJSONCLIGolden(t, "mod_copy_dry_run", out)
	})
}
