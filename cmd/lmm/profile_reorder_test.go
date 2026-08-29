package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file characterizes doProfileReorder (cmd/lmm/profile.go:770-870) as
// it stands BEFORE v2 Phase 2.5 lifts its ID-resolution policy into core
// (docs/plans/2026-08-28-v2-phase2-impl.md Unit... Task 13). Every
// assertion here pins pre-lift behavior byte-for-byte; a diff after the
// lift is a regression unless the plan explicitly says otherwise.

// addProfileMod appends sourceID:modID (version "1.0", no FileIDs) to
// profile's load order via ProfileManager.AddMod - the minimal fixture for
// exercising doProfileReorder's "with args" path, which reads and rewrites
// profile.Mods only and never touches GetInstalledMods.
func addProfileMod(t *testing.T, svc *core.Service, game *domain.Game, profile, sourceID, modID string) {
	t.Helper()
	require.NoError(t, getProfileManager(svc).AddMod(game.ID, profile, domain.ModReference{SourceID: sourceID, ModID: modID, Version: "1.0"}))
}

// reloadProfile re-reads profile from disk, bypassing any in-memory state,
// to verify what doProfileReorder actually persisted (or left untouched).
func reloadProfile(t *testing.T, svc *core.Service, game *domain.Game, profile string) *domain.Profile {
	t.Helper()
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, profile)
	require.NoError(t, err)
	return p
}

func TestDoProfileReorder_NoArgs_PrintsLoadOrderTable(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "alpha", SourceID: "src1", Name: "Alpha Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "beta", SourceID: "src1", Name: "Beta Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "default", "src1", "beta")
	// "gamma" is in the profile's load order but was never installed, so
	// GetInstalledMods can't resolve its name - the no-args table must fall
	// back to "(unknown)".
	addProfileMod(t, svc, game, "default", "src1", "gamma")

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Load order for default (first = lowest priority):\n"+
		"#  MOD_ID  NAME\n"+
		"1  alpha   Alpha Mod\n"+
		"2  beta    Beta Mod\n"+
		"3  gamma   (unknown)\n", out)
}

func TestDoProfileReorder_ExplicitSourceModIDKeys(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "default", "src1", "beta")
	addProfileMod(t, svc, game, "default", "src1", "gamma")

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, []string{"src1:gamma", "src1:alpha"})
	})

	assert.Equal(t, "✓ Load order updated for profile default.\n", out)

	p := reloadProfile(t, svc, game, "default")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, p.Mods, "mentioned mods lead in the given order; the unmentioned mod is appended in its original relative position")
}

func TestDoProfileReorder_BareIDs(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "default", "src1", "beta")
	addProfileMod(t, svc, game, "default", "src1", "gamma")

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, []string{"gamma", "alpha"})
	})

	assert.Equal(t, "✓ Load order updated for profile default.\n", out)

	p := reloadProfile(t, svc, game, "default")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, p.Mods, "an unambiguous bare mod ID resolves the same as its source:modid form")
}

// TestDoProfileReorder_AmbiguousBareID_ErrorsAndLeavesProfileUnchanged pins
// profile.go:848's exact error text. Re-pinned for Ruling 4 (#298): the
// matching candidates are now sorted by source ID, so the reported order is
// fixed (src1, src2) rather than "whichever permutation the map produced".
func TestDoProfileReorder_AmbiguousBareID_ErrorsAndLeavesProfileUnchanged(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src2", "shared")
	addProfileMod(t, svc, game, "default", "src1", "shared")
	before := reloadProfile(t, svc, game, "default")

	err := doProfileReorder(context.Background(), svc, game, []string{"shared"})

	require.Error(t, err)
	assert.EqualError(t, err, "ambiguous mod id shared (use source:modid): src1:shared, src2:shared")

	after := reloadProfile(t, svc, game, "default")
	assert.Equal(t, before.Mods, after.Mods, "an ambiguous-ID error must leave the profile untouched")
}

func TestDoProfileReorder_UnknownID_ErrorsAndLeavesProfileUnchanged(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	before := reloadProfile(t, svc, game, "default")

	err := doProfileReorder(context.Background(), svc, game, []string{"nope"})

	require.Error(t, err)
	assert.Equal(t, "mod nope not in profile", err.Error())

	after := reloadProfile(t, svc, game, "default")
	assert.Equal(t, before.Mods, after.Mods, "an unknown-ID error must leave the profile untouched")
}

// TestDoProfileReorder_ExplicitKeyNotInProfile_SameErrorText proves the
// "mod %s not in profile" text is shared by both the explicit source:modid
// miss and the bare-ID zero-match miss (profile.go:824 and :839) - not two
// separately worded errors.
func TestDoProfileReorder_ExplicitKeyNotInProfile_SameErrorText(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	before := reloadProfile(t, svc, game, "default")

	err := doProfileReorder(context.Background(), svc, game, []string{"src2:nope"})

	require.Error(t, err)
	assert.Equal(t, "mod src2:nope not in profile", err.Error())

	after := reloadProfile(t, svc, game, "default")
	assert.Equal(t, before.Mods, after.Mods)
}

func TestDoProfileReorder_PartialReorder_AppendsUnmentionedInOriginalOrder(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "default", "src1", "beta")
	addProfileMod(t, svc, game, "default", "src1", "gamma")

	require.NoError(t, doProfileReorder(context.Background(), svc, game, []string{"gamma"}))

	p := reloadProfile(t, svc, game, "default")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, p.Mods, "alpha and beta, both unmentioned, keep their original relative order (alpha before beta) after the mentioned mod")
}

func TestDoProfileReorder_DuplicateArgs_Deduped(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "default", "src1", "beta")
	addProfileMod(t, svc, game, "default", "src1", "gamma")

	require.NoError(t, doProfileReorder(context.Background(), svc, game, []string{"gamma", "gamma", "alpha"}))

	p := reloadProfile(t, svc, game, "default")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, p.Mods, "a repeated arg contributes only its first occurrence's position")
}

func TestDoProfileReorder_ProfileFlag_SelectsNonDefaultProfile(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	addProfileMod(t, svc, game, "default", "src1", "alpha")
	addProfileMod(t, svc, game, "other", "src1", "x")
	addProfileMod(t, svc, game, "other", "src1", "y")

	oldFlag := profileReorderProfile
	profileReorderProfile = "other"
	t.Cleanup(func() { profileReorderProfile = oldFlag })

	out := captureStdout(t, func() error {
		return doProfileReorder(context.Background(), svc, game, []string{"y"})
	})

	assert.Equal(t, "✓ Load order updated for profile other.\n", out)

	other := reloadProfile(t, svc, game, "other")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "y", Version: "1.0"},
		{SourceID: "src1", ModID: "x", Version: "1.0"},
	}, other.Mods)

	unchangedDefault := reloadProfile(t, svc, game, "default")
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
	}, unchangedDefault.Mods, "-p must scope the reorder to the named profile, leaving the default profile untouched")
}

// TestDoProfileReorder_DeployCompile_ResyncsMergedPak proves
// service.ReorderProfileMods' merged-pak sync (internal/core/flows.go's
// reorderProfileMods, #197) actually fires end-to-end through
// doProfileReorder for a DeployCompile game - the merged pak's byte content
// reflects the NEW load order without any separate SyncMergedPak call,
// mirroring profile_compile_test.go's pattern.
func TestDoProfileReorder_DeployCompile_ResyncsMergedPak(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "bear-mount", "1.0", cache.RetainedSourceName("exmodz-a"), []byte("A")))
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "wolf-mount", "1.0", cache.RetainedSourceName("exmodz-b"), []byte("B")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Name: "Bear Mount", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"exmodz-a"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "wolf-mount", SourceID: "fake-compiler", Name: "Wolf Mount", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"exmodz-b"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := getProfileManager(svc)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0", FileIDs: []string{"exmodz-a"}}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "wolf-mount", Version: "1.0", FileIDs: []string{"exmodz-b"}}))

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "AB", string(before), "precondition: merged pak reflects install order bear, wolf")

	require.NoError(t, doProfileReorder(context.Background(), svc, game, []string{"wolf-mount"}))

	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "doProfileReorder must resync the merged pak, not just persist the new order")
	assert.Equal(t, "BA", string(after), "the merged pak must reflect the new load order (wolf now first) without a separate SyncMergedPak call")
}
