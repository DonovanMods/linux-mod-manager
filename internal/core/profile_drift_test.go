package core_test

// #641: the active profile's drift summary - what `lmm profile apply` would
// converge, counted by reason, from the same comparison PlanProfileApply
// makes, without a download or a source call. GameStatus carries it, so
// `lmm status --game` and the web UI's Profile card both read it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offlineSource is a source the drift summary must never call: every
// catalog read fails the test.
type offlineSource struct {
	*mockSource
	t *testing.T
}

func (s offlineSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	s.t.Error("the drift summary called the source's GetMod")
	return nil, assert.AnError
}

func (s offlineSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	s.t.Error("the drift summary called the source's GetModFiles")
	return nil, assert.AnError
}

// saveRow saves an installed row of profile "default" with no cache entry.
func saveRow(t *testing.T, svc *core.Service, game *domain.Game, im domain.InstalledMod) {
	t.Helper()
	im.GameID = game.ID
	im.ProfileName = "default"
	im.UpdatePolicy = domain.UpdateNotify
	if im.Name == "" {
		im.Name = im.ID
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &im))
}

// gameDrift is GameStatus's drift summary for game.
func gameDrift(t *testing.T, svc *core.Service, game *domain.Game) *core.ProfileDrift {
	t.Helper()
	status, err := svc.GameStatus(context.Background(), game)
	require.NoError(t, err)
	return status.ProfileDrift
}

func TestGameStatus_NoDriftIsAbsent(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	pm := svc.NewProfileManager()
	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("x")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	assert.Nil(t, gameDrift(t, svc, game), "a profile that matches its installs has no drift")
}

// TestGameStatus_DriftCountsEveryReason is one mod per reason, each counted
// once, and the counts add up to exactly what PlanProfileApply would do.
func TestGameStatus_DriftCountsEveryReason(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()
	svc.RegisterSource(offlineSource{mockSource: newMockSource("src"), t: t})

	// version: installed at 1.0, the profile was hand-edited to 2.0.
	saveRow(t, svc, game, domain.InstalledMod{Mod: domain.Mod{ID: "ver", SourceID: "src", Version: "1.0"}, Enabled: true, Deployed: true})
	// files: the same version, another file pinned (#639).
	saveRow(t, svc, game, domain.InstalledMod{Mod: domain.Mod{ID: "files", SourceID: "src", Version: "1.0"}, Enabled: true, Deployed: true, FileIDs: []string{"1"}})
	// fomod: the profile records installer choices the row's tree is not.
	saveRow(t, svc, game, domain.InstalledMod{Mod: domain.Mod{ID: "fomod", SourceID: "src", Version: "1.0"}, Enabled: true, Deployed: true})
	// mod_type: the profile states another type (#607).
	saveRow(t, svc, game, domain.InstalledMod{Mod: domain.Mod{ID: "type", SourceID: "src", Version: "1.0"}, Enabled: true, Deployed: true})
	// not_enabled: listed and on, installed but disabled here.
	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	// unlisted: enabled here, and the profile no longer lists it.
	seedInstalledMod(t, svc, game, "src", "gone", "1.0", true, map[string][]byte{"gone.esp": []byte("x")})
	// switched_off: enabled here, and the profile switches it off (#431).
	seedInstalledMod(t, svc, game, "src", "marked", "1.0", true, map[string][]byte{"marked.esp": []byte("x")})
	// unchanged.
	seedInstalledMod(t, svc, game, "src", "same", "1.0", true, map[string][]byte{"same.esp": []byte("x")})

	refs := []domain.ModReference{
		{SourceID: "src", ModID: "ver", Version: "2.0"},
		{SourceID: "src", ModID: "files", Version: "1.0", FileIDs: []string{"2"}},
		{SourceID: "src", ModID: "fomod", Version: "1.0", Fomod: recordedChoices()},
		{SourceID: "src", ModID: "type", Version: "1.0", ModType: "root"},
		{SourceID: "src", ModID: "off", Version: "1.0"},
		{SourceID: "src", ModID: "marked", Version: "1.0", Disabled: true},
		{SourceID: "src", ModID: "same", Version: "1.0"},
		// not_installed: listed, nothing installed anywhere.
		{SourceID: "src", ModID: "new", Version: "1.0"},
	}
	for _, ref := range refs {
		require.NoError(t, pm.AddMod(ctx, game.ID, "default", ref))
	}

	drift := gameDrift(t, svc, game)
	require.NotNil(t, drift)
	assert.Equal(t, core.ProfileDrift{
		Profile:      "default",
		NotInstalled: 1,
		NotEnabled:   1,
		Unlisted:     1,
		SwitchedOff:  1,
		Version:      1,
		Files:        1,
		Fomod:        1,
		ModType:      1,
	}, *drift)
	assert.Equal(t, 8, drift.Total())

	// The same comparison: every mod the summary counts is one the plan
	// acts on.
	svc.RegisterSource(newMockSource("src"))
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.Equal(t, drift.Total(), len(plan.ToDisable)+len(plan.ToEnable)+len(plan.ToInstall))
}

// TestProfileDrift_AgreesWithThePlan: whatever the summary says, the plan
// says the same - drift exactly when the plan has changes.
func TestProfileDrift_AgreesWithThePlan(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()
	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("x")})
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	drift, err := svc.ProfileDriftForTest(ctx, game)
	require.NoError(t, err)
	assert.Nil(t, drift)
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)

	// A hand edit of the version.
	require.NoError(t, pm.UpsertMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "0.9"}))
	drift, err = svc.ProfileDriftForTest(ctx, game)
	require.NoError(t, err)
	require.NotNil(t, drift)
	assert.Equal(t, 1, drift.Version)
	svc.RegisterSource(newMockSource("src"))
	plan, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.False(t, plan.NoChanges)
}

// TestProfileDrift_CountsAnOverrideChange is the overrides reason (#631):
// an override added to the profile by hand is a change the apply makes,
// and once applied there is none.
func TestProfileDrift_CountsAnOverrideChange(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.overriddenProfile(t, "default", true, nil, "k")
	f.deploy(t, "default", core.DeployOptions{})
	ctx := context.Background()

	drift, err := f.svc.ProfileDriftForTest(ctx, f.game)
	require.NoError(t, err)
	assert.Nil(t, drift, "deployed, with no override: nothing to apply")

	f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "tweaked\n"}, "k")
	drift, err = f.svc.ProfileDriftForTest(ctx, f.game)
	require.NoError(t, err)
	require.NotNil(t, drift)
	assert.Equal(t, core.ProfileDrift{Profile: "default", Overrides: 1}, *drift)
	assert.Len(t, f.applyPlan(t, "default").Overrides, drift.Overrides)

	f.applyProfile(t, "default")
	drift, err = f.svc.ProfileDriftForTest(ctx, f.game)
	require.NoError(t, err)
	assert.Nil(t, drift, "applied: the game matches the profile")

	// The override's content edited by hand, to the same length: the
	// count memoised for the profile as it was must not answer for it.
	f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "tweakeD\n"}, "k")
	drift, err = f.svc.ProfileDriftForTest(ctx, f.game)
	require.NoError(t, err)
	require.NotNil(t, drift, "a profile edit is seen through the memo")
	assert.Equal(t, 1, drift.Overrides)
	assert.Len(t, f.applyPlan(t, "default").Overrides, 1)
}

// TestProfileDrift_NoActiveProfile: a game with no profile has nothing to
// converge.
func TestProfileDrift_NoActiveProfile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	drift, err := svc.ProfileDriftForTest(context.Background(), game)
	require.NoError(t, err)
	assert.Nil(t, drift)
}

// TestProfileDrift_LeftoverCopyWithNoManifestRecord is the #641 review's
// blocker: an overridden copy whose manifest note is gone (the manifest
// deleted, a note that failed to write, a copy from a build before #631
// recorded them) is still found by its deployed record's fingerprint. The
// apply plan restores it, and the drift summary counts it - the two agree.
func TestProfileDrift_LeftoverCopyWithNoManifestRecord(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")
			manifest := filepath.Join(f.svc.DataDirForTest(), "snapshots", f.game.ID, "_originals", "manifest.json")
			require.FileExists(t, manifest)
			require.NoError(t, os.Remove(manifest))
			// The override deleted from the profile by hand.
			f.overriddenProfile(t, "default", true, nil, "k")

			plan := f.applyPlan(t, "default")
			changes := 0
			for _, c := range plan.Overrides {
				if c.Action != core.OverrideKeep {
					changes++
				}
			}
			drift, err := f.svc.ProfileDriftForTest(context.Background(), f.game)
			require.NoError(t, err)
			if method != domain.LinkCopy {
				// Linked: the copy is found by its fingerprint alone.
				require.NotZero(t, changes, "the plan restores the left-over copy")
			}
			if changes == 0 {
				assert.True(t, plan.NoChanges)
				assert.Nil(t, drift, "the plan has nothing to do, so there is no drift")
				return
			}
			assert.Equal(t, []core.OverrideChange{{Path: "Data/k.ini", Action: core.OverrideRestore, SourceID: "local", ModID: "k"}}, plan.Overrides)
			assert.False(t, plan.NoChanges)
			require.NotNil(t, drift, "the plan restores the copy, so the summary counts it")
			assert.Equal(t, core.ProfileDrift{Profile: "default", Overrides: changes}, *drift)
		})
	}
}
