package core_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// --- PlanProfileSwitch / ApplyProfileSwitch (Task 4) ---
//
// These extract doProfileSwitch (cmd/lmm/profile.go) into a pure diff
// computation (PlanProfileSwitch) plus an execution step (ApplyProfileSwitch)
// that reuses the flow-event/phase-constant pattern established
// by Task 3, extended with Switch*-prefixed phases rather than a parallel
// SwitchProgress type - see the task report for the full phase mapping.

// seedInstalledModUnderProfile is seedNamedInstalledMod with a
// caller-supplied ProfileName, needed because the pre-extraction
// doProfileSwitch's enable-loop calls SetModEnabled(..., targetName, ...) -
// the NEW profile's name, not the mod's own current ProfileName (see the
// task report) - so a toEnable mod's DB row must already live under the
// target profile name for that call to find and update it.
func seedInstalledModUnderProfile(t *testing.T, svc *core.Service, game *domain.Game, profileName, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     name,
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// installEnabledBlockingTrigger mirrors installBlockingTrigger but targets
// installed_mods.enabled specifically, isolating SetModEnabled failures from
// SetModLinkMethod/SetModDeployed (which installBlockingTrigger blocks) or
// any other column.
func installEnabledBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_enabled_updates
		BEFORE UPDATE OF enabled ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// --- PlanProfileSwitch ---

func TestService_PlanProfileSwitch_AlreadyActive(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.AlreadyActive)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.From)
	assert.Equal(t, "default", plan.To)
	assert.False(t, plan.NoChanges)
	assert.Empty(t, plan.ToDisable)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToInstall)
}

// TestService_PlanProfileSwitch_NoChangesWhenModSetsMatch guards the no-op
// fast path: when the target profile's mod set already matches what's
// enabled under the current default profile, PlanProfileSwitch reports
// NoChanges (only SetDefault is needed) - mirroring doProfileSwitch's
// "No mod changes, just switch the default" branch.
func TestService_PlanProfileSwitch_NoChangesWhenModSetsMatch(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "other")
	require.NoError(t, err)

	seedInstalledMod(t, svc, game, "src", "shared", "1.0", true, map[string][]byte{"shared.esp": []byte("s")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "other", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "other")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.AlreadyActive)
	assert.True(t, plan.NoChanges)
	assert.Empty(t, plan.ToDisable)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToInstall)
}

// TestService_PlanProfileSwitch_ComputesDisableEnableInstallBuckets guards
// the diff algorithm's three buckets in one mixed scenario: a mod only in
// the current profile's enabled set goes to ToDisable, a mod installed (and
// cached) but disabled that the target references goes to ToEnable, and a
// mod the target references with no DB row at all goes to ToInstall.
func TestService_PlanProfileSwitch_ComputesDisableEnableInstallBuckets(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	// modC: enabled under "default", absent from "target" -> ToDisable.
	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "modC", Version: "1.0"}))

	// modB: installed (under "default") but disabled, cached, referenced by
	// "target" -> ToEnable.
	seedNamedInstalledMod(t, svc, game, "src", "modB", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modB", Version: "1.0"}))

	// modD: referenced by "target" only, no DB row at all -> ToInstall.
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modD", Version: "2.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.NoChanges)

	require.Len(t, plan.ToDisable, 1)
	assert.Equal(t, "modC", plan.ToDisable[0].ID)

	require.Len(t, plan.ToEnable, 1)
	assert.Equal(t, "modB", plan.ToEnable[0].ID)

	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "modD", plan.ToInstall[0].ModID)
	assert.Equal(t, "2.0", plan.ToInstall[0].Version)
}

// TestService_PlanProfileSwitch_DeterministicOrder guards Task 1's
// PlanProfileSwitch fix: ToDisable/ToEnable/ToInstall used to be built by
// ranging Go maps (currentEnabled/targetKeys), so their element order was
// arbitrary from run to run. This seeds three mods per bucket with
// profile.Mods orders that deliberately don't match alphabetical or
// insertion order, plans twice, and asserts: (1) both runs produce identical
// slices, and (2) ToDisable follows the FROM profile's ("default") Mods
// order while ToEnable/ToInstall follow the TO profile's ("target") Mods
// order - interleaved in a single Mods list, to prove each bucket
// independently preserves its relative order within that shared list.
func TestService_PlanProfileSwitch_DeterministicOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	// ToDisable candidates: enabled under "default", absent from "target".
	// default.Mods is deliberately not alphabetical (disC, disA, disB).
	for _, id := range []string{"disC", "disA", "disB"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "Dis "+id, "1.0", true, nil)
		require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	// ToEnable candidates: installed (under "default") but disabled, cached,
	// referenced by "target".
	for _, id := range []string{"enA", "enB", "enC"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "En "+id, "1.0", false, map[string][]byte{id + ".dat": []byte(id)})
	}

	// ToInstall candidates: referenced by "target" only, no DB row at all.
	// target.Mods interleaves enable/install refs in an order that matches
	// neither alphabetical nor insertion order for either sub-sequence.
	for _, id := range []string{"enC", "insB", "enA", "insC", "enB", "insA"} {
		require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	plan1, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	plan2, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)

	assert.Equal(t, plan1.ToDisable, plan2.ToDisable, "two runs must produce identical ToDisable order")
	assert.Equal(t, plan1.ToEnable, plan2.ToEnable, "two runs must produce identical ToEnable order")
	assert.Equal(t, plan1.ToInstall, plan2.ToInstall, "two runs must produce identical ToInstall order")

	var disableIDs []string
	for _, im := range plan1.ToDisable {
		disableIDs = append(disableIDs, im.ID)
	}
	assert.Equal(t, []string{"disC", "disA", "disB"}, disableIDs, "ToDisable must follow the FROM profile's (default) Mods order")

	var enableIDs []string
	for _, im := range plan1.ToEnable {
		enableIDs = append(enableIDs, im.ID)
	}
	assert.Equal(t, []string{"enC", "enA", "enB"}, enableIDs, "ToEnable must follow the TO profile's (target) Mods order")

	var installIDs []string
	for _, ref := range plan1.ToInstall {
		installIDs = append(installIDs, ref.ModID)
	}
	assert.Equal(t, []string{"insB", "insC", "insA"}, installIDs, "ToInstall must follow the TO profile's (target) Mods order")
}

// TestService_PlanProfileSwitch_CacheMissForcesReinstallWithPreservedFileIDs
// guards doProfileSwitch's re-download branch: when a mod the target
// profile references IS installed in the DB but its cache entry is gone,
// PlanProfileSwitch classifies it as ToInstall (not ToEnable) and carries
// over the installed mod's own FileIDs (not the profile YAML's, which may be
// empty or stale) so the redownload uses the same files.
func TestService_PlanProfileSwitch_CacheMissForcesReinstallWithPreservedFileIDs(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	// DB row exists (with FileIDs), but nothing was ever stored in the cache.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "modE", SourceID: "src", Name: "Mod E", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1", "f2"},
	}))
	// Profile YAML's own FileIDs are deliberately absent, to prove
	// PlanProfileSwitch uses the INSTALLED mod's FileIDs, not the profile's.
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modE", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)

	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "modE", plan.ToInstall[0].ModID)
	assert.Equal(t, []string{"f1", "f2"}, plan.ToInstall[0].FileIDs)
	assert.Empty(t, plan.ToEnable, "a cache-miss mod must be reinstalled, not merely re-enabled")
}

func TestService_PlanProfileSwitch_UnknownTargetProfileReturnsError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found: missing")
	assert.Nil(t, plan)
}

// TestService_PlanProfileSwitch_PerformsZeroMutations guards the
// "pure computation" contract callers depend on: calling
// PlanProfileSwitch speculatively (e.g. to render a confirmation prompt) and
// discarding the result must leave the DB, cache, and profile YAMLs
// byte-for-byte untouched.
func TestService_PlanProfileSwitch_PerformsZeroMutations(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "modC", Version: "1.0"}))
	seedNamedInstalledMod(t, svc, game, "src", "modB", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modB", Version: "1.0"}))
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modD", Version: "2.0"}))

	defaultPath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	targetPath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "target.yaml")
	beforeDefault, err := os.ReadFile(defaultPath)
	require.NoError(t, err)
	beforeTarget, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	beforeMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.False(t, plan.NoChanges, "sanity: this scenario must exercise all three diff buckets")

	afterDefault, err := os.ReadFile(defaultPath)
	require.NoError(t, err)
	afterTarget, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, beforeDefault, afterDefault, "profile YAML must be byte-for-byte unchanged after planning")
	assert.Equal(t, beforeTarget, afterTarget, "profile YAML must be byte-for-byte unchanged after planning")

	afterMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "planning must not deploy any files")
}

// TestApplyProfileSwitch_StalePlan_ReturnsErrStalePlan pins the phase-2-close
// review's Important #1 / Ruling 5 for Switch: an installed-mod set that
// moved (under plan.From) since the plan was computed is refused, not
// silently applied against a world it no longer describes.
func TestApplyProfileSwitch_StalePlan_ReturnsErrStalePlan(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "modC", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.False(t, plan.NoChanges, "sanity: this scenario must exercise the disable bucket")

	// State moves after planning: modC is disabled directly, changing the
	// "default" (plan.From) installed-mod set the plan was computed against.
	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", false, map[string][]byte{"c.esp": []byte("c")})

	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// --- ApplyProfileSwitch ---

// TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure
// guards doProfileSwitch's overall execution order (disable loop, then
// enable loop, then install loop, with SetDefault always last) and the
// error-path convention: SetDefault's own failure must not discard the
// Disabled/Enabled/Installed accounting from everything that already ran
// before it, and must leave the previous default profile in place. The
// target profile is deliberately never created, so both the install loop's
// UpsertMod call and the final SetDefault call fail deterministically
// (ErrProfileNotFound) - UpsertMod's failure is expected and non-fatal (see
// TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult
// for a dedicated, isolated test of that same convention).
func TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))

	seedNamedInstalledMod(t, svc, game, "src", "disable-me", "Disable Me", "1.0", true, map[string][]byte{"disable.esp": []byte("d")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "disable-me", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "enable-me", "Enable Me", "1.0", false, map[string][]byte{"enable.esp": []byte("e")})

	disableMod, err := svc.GetInstalledMod(context.Background(), "src", "disable-me", "g1", "default")
	require.NoError(t, err)
	enableMod, err := svc.GetInstalledMod(context.Background(), "src", "enable-me", "g1", "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"install.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "install-me", SourceID: "src", Name: "Install Me", Version: "1.0", GameID: "g1"})

	plan := svc.FreshSwitchPlanForTest(context.Background(), &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
		ToEnable:  []domain.InstalledMod{*enableMod},
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "install-me", Version: "1.0"}},
	})

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.Error(t, err, "SetDefault must fail deterministically: target profile was never created")
	assert.Contains(t, err.Error(), "setting default profile")
	require.NotNil(t, result, "counts/diagnostics accumulated before the fatal SetDefault error must not be discarded")
	assert.Equal(t, 1, result.Disabled)
	assert.Equal(t, 1, result.Enabled)
	assert.Equal(t, 1, result.Installed)
	assert.Empty(t, result.Notes, "#294 (Ruling 5's class extension, Task 13b): the install loop's UpsertMod failure is no longer a --verbose-only note")
	require.Len(t, result.Warnings, 1, "the install loop's UpsertMod failure (target profile doesn't exist) must be recorded")
	assert.Contains(t, result.Warnings[0], "could not update profile")

	var disabledIdx, enabledIdx, installingIdx = -1, -1, -1
	phases, _ := phasesOf(*seen)
	for i, ph := range phases {
		switch ph {
		case core.SwitchDisabled:
			disabledIdx = i
		case core.SwitchEnabled:
			if enabledIdx == -1 {
				enabledIdx = i
			}
		case core.SwitchInstalling:
			installingIdx = i
		}
	}
	require.NotEqual(t, -1, disabledIdx, "expected a SwitchDisabled event")
	require.NotEqual(t, -1, enabledIdx, "expected a SwitchEnabled event")
	require.NotEqual(t, -1, installingIdx, "expected a SwitchInstalling event")
	assert.Less(t, disabledIdx, enabledIdx, "disable phase must complete before the enable phase starts")
	assert.Less(t, enabledIdx, installingIdx, "enable phase must complete before the install phase starts")

	def, err := pm.GetDefault(context.Background(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", def.Name, "a failed SetDefault must leave the previous default profile in place")
}

// TestApplyProfileSwitch_LockedRef_UpsertRefusalIsWarning pins #294's class
// extension (Ruling 5, Task 13b): ApplyProfileSwitch's install loop hits the
// identical swallowed UpsertMod refusal ApplyProfileApply/ApplyProfileSync
// already surface (a LOCKED profile ref, #143) and now records it the same
// way - a SwitchInstallWarning event plus a Result.Warnings entry, not the
// --verbose-only SwitchInstallNote it used to be. Mirrors
// TestApplyProfileApply_LockedRef_UpsertRefusalIsWarning exactly. The mod
// still installs.
func TestApplyProfileSwitch_LockedRef_UpsertRefusalIsWarning(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "target", domain.ModReference{SourceID: "src", ModID: "mod1", Locked: true}))

	svc.RegisterSource(newTwoVersionSource(t))

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.5"}},
	}

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the refusal must not fail the install")
	assert.Empty(t, result.Notes, "#294: the refusal is no longer a --verbose-only note")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "could not update profile: ")
	assert.Contains(t, result.Warnings[0], "is locked at v")
	assert.NotContains(t, result.Warnings[0], "Warning: ",
		"#294: Warnings carry no baked-in prefix - the caller renders `Warning: %s`")

	phases, _ := phasesOf(*events)
	assert.Contains(t, phases, core.SwitchInstallWarning)
	assert.NotContains(t, phases, core.SwitchInstallNote)

	profile, err := pm.Get(context.Background(), game.ID, "target")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Empty(t, profile.Mods[0].Version, "the locked ref must be left unwritten")
}

// TestService_ApplyProfileSwitch_UsesTargetProfileLinkMethod guards #81's
// no-override-hook path: a switch INTO a profile with an explicit link_method
// must deploy the target's mods with the TARGET profile's method, not the
// game's. Game explicitly symlink, target profile explicitly copy - the
// enabled mod's file must land as a real copy.
func TestService_ApplyProfileSwitch_UsesTargetProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)
	setProfileLinkMethod(t, svc, "g1", "target", domain.LinkCopy)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "enable-me", "Enable Me", "1.0", false, map[string][]byte{"enable.esp": []byte("e")})
	enableMod, err := svc.GetInstalledMod(context.Background(), "src", "enable-me", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Enabled)
	assert.Empty(t, result.Notes)

	info, err := os.Lstat(filepath.Join(gameDir, "enable.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "the target profile's explicit copy must beat the game's explicit symlink")
}

// TestService_ApplyProfileSwitch_DisableLoopUsesSourceProfileLinkMethod pins
// the awkward half of #81's two-profile span: the disable loop undeploys the
// FROM profile's mods, so it must use the FROM profile's method. The from
// profile is explicitly copy (its file on disk is a real file); undeploying
// with the game/target symlink method would refuse ("not a symlink") and
// leave the file behind with a Note.
func TestService_ApplyProfileSwitch_DisableLoopUsesSourceProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	seedNamedInstalledMod(t, svc, game, "src", "disable-me", "Disable Me", "1.0", true, map[string][]byte{"disable.esp": []byte("d")})
	copyInstaller := svc.NewInstallerWithLinkerForTest(game, domain.LinkCopy)
	require.NoError(t, copyInstaller.Install(context.Background(), game, &domain.Mod{ID: "disable-me", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	deployedPath := filepath.Join(gameDir, "disable.esp")
	info, err := os.Lstat(deployedPath)
	require.NoError(t, err, "precondition: the from-profile's mod must be deployed")
	require.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "precondition: the from-profile deployment must be a real copy")

	disableMod, err := svc.GetInstalledMod(context.Background(), "src", "disable-me", "g1", "default")
	require.NoError(t, err)

	plan := svc.FreshSwitchPlanForTest(context.Background(), &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	})

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Disabled)
	assert.Empty(t, result.Notes, "undeploying the from-profile's copy with the from-profile's method must not error")

	_, err = os.Lstat(deployedPath)
	assert.True(t, os.IsNotExist(err), "the disable loop must remove the from-profile's copied file")
}

// TestService_ApplyProfileSwitch_DisableLoop_UndeployAndSetEnabledFailuresAreNonFatalNotes_SuccessEventStillFires
// guards doProfileSwitch's disable-loop semantics: BOTH a failed Uninstall
// and a failed SetModEnabled are recorded as Notes (never Warnings, never
// fatal) and the mod is still counted as Disabled with its success event
// still firing - the disable loop always "wins" regardless of either
// sub-step's outcome, unlike the enable loop (see the InstallFailureSkipsMod
// test below).
func TestService_ApplyProfileSwitch_DisableLoop_UndeployAndSetEnabledFailuresAreNonFatalNotes_SuccessEventStillFires(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err = pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// Corrupt the deployed symlink so Uninstall fails deterministically.
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	// Block updates to installed_mods.enabled so SetModEnabled fails too.
	installEnabledBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	disableMod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)

	plan := svc.FreshSwitchPlanForTest(context.Background(), &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	})

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Disabled, "the mod must still be counted as disabled despite both failures")
	require.Len(t, result.Notes, 2)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to undeploy Test Mod: "), "note[0]: %q", result.Notes[0])
	assert.True(t, strings.HasPrefix(result.Notes[1], "Warning: failed to update Test Mod: "), "note[1]: %q", result.Notes[1])

	var noteEvents []core.StepEvent
	disabledIdx := -1
	for i, e := range *seen {
		if step, ok := e.(core.StepEvent); ok && step.Phase == core.SwitchDisableNote {
			noteEvents = append(noteEvents, step)
		}
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.SwitchDisabled {
			disabledIdx = i
		}
	}
	require.Len(t, noteEvents, 2)
	assert.Equal(t, result.Notes[0], noteEvents[0].Detail)
	assert.Equal(t, result.Notes[1], noteEvents[1].Detail)
	require.NotEqual(t, -1, disabledIdx, "expected a SwitchDisabled event despite both failures")
	assert.Greater(t, disabledIdx, 0, "the success event must come after the note events")
}

// TestService_ApplyProfileSwitch_EnableLoop_InstallFailureSkipsModEntirely
// guards the enable loop's differing semantics from the disable loop: a
// failed Install is fatal FOR THAT MOD ONLY - it is recorded as a Note, but
// SetModEnabled is never called and no SwitchEnabled event fires, mirroring
// doProfileSwitch's `continue` immediately after the Install failure branch.
func TestService_ApplyProfileSwitch_EnableLoop_InstallFailureSkipsModEntirely(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	// Block deployment deterministically: "blocked" already exists as a
	// regular file, so the linker's os.MkdirAll(filepath.Dir(dst)) fails -
	// mirrors TestService_EnableMod_DeployFailurePropagatesAndLeavesDBUntouched.
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"blocked/plugin.esp": []byte("data")})
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "blocked"), []byte("occupied"), 0644))

	enableMod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err, "an Install failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Enabled)
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to deploy Test Mod: "), "note: %q", result.Notes[0])

	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		assert.NotEqual(t, core.SwitchEnabled, ph, "no SwitchEnabled event must fire for a mod whose Install failed")
	}

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "target")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "SetModEnabled must never be called after a failed Install")
}

// TestService_ApplyProfileSwitch_EnableLoop_SetModEnabledFailureIsNonFatalNote
// guards the enable loop's OTHER sub-step: when Install succeeds but
// SetModEnabled fails, the mod is still counted as Enabled and its success
// event still fires - contrasting with the Install-failure case above.
func TestService_ApplyProfileSwitch_EnableLoop_SetModEnabledFailureIsNonFatalNote(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err = pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	installEnabledBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	enableMod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled, "Install still succeeded, so the mod must still be counted as enabled")
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to update Test Mod: "))

	var sawEnabled bool
	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		if ph == core.SwitchEnabled {
			sawEnabled = true
		}
	}
	assert.True(t, sawEnabled, "a SwitchEnabled event must still fire despite the SetModEnabled failure")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "the mod must still have been deployed")
}

// TestService_ApplyProfileSwitch_EnableLoop_ModInstalledUnderOtherProfile_CreatesTargetRow
// guards the #60 fix: when the target profile's YAML references a mod whose
// installed_mods row lives only under a DIFFERENT profile (reachable via
// profile export/import or hand-edited profile lists), enabling it must
// create a row under the target profile - otherwise the deployment is
// orphaned: files on disk and tracked in deployed_files, but invisible to
// GetInstalledMods(game, target), so a later switch AWAY from the target
// never undeploys the mod.
func TestService_ApplyProfileSwitch_EnableLoop_ModInstalledUnderOtherProfile_CreatesTargetRow(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))

	// Installed+disabled under "default"; referenced by "target"'s YAML
	// without ever having been installed there.
	seedInstalledModUnderProfile(t, svc, game, "default", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "target", "src", "1", "1.0")

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 1)
	require.Equal(t, "default", plan.ToEnable[0].ProfileName,
		"precondition: the planned mod's row must live under the other profile")

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled)
	assert.Empty(t, result.Notes, "creating the missing target row must not be downgraded to a warning")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "the mod must be deployed to the game dir")

	row, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "target")
	require.NoError(t, err, "an installed_mods row must exist under the target profile")
	assert.True(t, row.Enabled)
	assert.True(t, row.Deployed)

	// The orphan regression: switching back away from "target" must see the
	// mod as enabled there and schedule it for disable/undeploy.
	backPlan, err := svc.PlanProfileSwitch(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, backPlan.ToDisable, 1, "switching away must undeploy the mod (it was orphaned before #60)")
	assert.Equal(t, "1", backPlan.ToDisable[0].ID)
}

// TestService_ApplyProfileSwitch_InstallLoop_FetchFailureSkipsModAndContinuesToNextMod
// guards the install loop's per-ref isolation: a mod whose source isn't
// registered (GetMod fails) is skipped via a SwitchInstallError event and
// does not stop the remaining ToInstall entries from installing.
func TestService_ApplyProfileSwitch_InstallLoop_FetchFailureSkipsModAndContinuesToNextMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"good.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "good", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: "g1"})
	// "bad" is never registered with the mock source, so GetMod fails.

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "bad", Version: "1.0"},
			{SourceID: "src", ModID: "good", Version: "1.0"},
		},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err, "a per-mod fetch failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "the good mod must still install")

	var errEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError {
			errEvt = &m
			break
		}
	}
	require.NotNil(t, errEvt)
	require.NotNil(t, errEvt.Mod)
	assert.Equal(t, "bad", errEvt.Mod.ModID)
	assert.Contains(t, errEvt.Detail, "failed to fetch mod")

	_, err = os.Lstat(filepath.Join(gameDir, "good.esp"))
	assert.NoError(t, err, "the second mod must still be installed")
}

// TestService_ApplyProfileSwitch_InstallLoop_DownloadFailureEmitsBlankErrorBlankSequence
// guards the dual-event sequence (SwitchDownloadFailed then, always,
// SwitchDownloadDone) that reproduces doProfileSwitch's exact
// blank-line/error/blank-line console sequence on a failed download - see
// SwitchDownloadDone's doc comment.
func TestService_ApplyProfileSwitch_InstallLoop_DownloadFailureEmitsBlankErrorBlankSequence(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src") // no AddDownload: the server 404s every file ID
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	phases, events := phasesOf(*seen)
	var failIdx, doneIdx = -1, -1
	for i, ph := range phases {
		switch ph {
		case core.SwitchDownloadFailed:
			failIdx = i
		case core.SwitchDownloadDone:
			doneIdx = i
		}
	}
	require.NotEqual(t, -1, failIdx, "expected a SwitchDownloadFailed event")
	require.NotEqual(t, -1, doneIdx, "expected a SwitchDownloadDone event")
	assert.Less(t, failIdx, doneIdx, "the loop-done event must fire after the failure event, mirroring the unconditional trailing blank line")
	assert.Contains(t, events[failIdx].(core.ModEvent).Detail, "download failed")
}

// TestService_ApplyProfileSwitch_InstallLoop_StoredFileIDsGone_FailsMod
// guards #95: when a ToInstall ref's FileIDs don't match any file the source
// currently offers, ApplyProfileSwitch must fail that mod (SwitchInstallError,
// via selectDeployFiles's allowFallback=false) rather than silently
// substituting the primary file (the old SwitchFallbackUsed phase, removed
// entirely in the renderer-cleanup task B2) - and the install loop must
// continue past it to the next mod.
func TestService_ApplyProfileSwitch_InstallLoop_StoredFileIDsGone_FailsMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod2.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent) // mockSource.GetModFiles always returns file ID "1"
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			// "stale-id" does not match the mock source's file ID ("1") - mod1
			// must fail, not silently fall back to the primary file.
			{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"stale-id"}},
			// mod2 has no stored FileIDs, so it installs normally - proving
			// mod1's failure doesn't stop the loop.
			{SourceID: "src", ModID: "mod2", Version: "1.0"},
		},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "only mod2 should install; mod1 fails")

	var failEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError && m.ModName == "Mod One" {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "stale-id")

	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "target")
	assert.Error(t, err, "mod1 must not be installed via fallback substitution")
	_, err = svc.GetInstalledMod(context.Background(), "src", "mod2", "g1", "target")
	assert.NoError(t, err, "mod2 must still install, proving the loop continues")
}

// TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion is the #94
// regression test for ApplyProfileSwitch's install loop: the source's served
// file ("1") carries its own Version ("1.0"), distinct from the mod-level
// Version ("1.5") GetMod returns. The saved InstalledMod row and the cache
// dir must be stamped with the file's version, not the mod's, mirroring
// flows_install_test.go's TestApplyInstall_ExplicitOldFile_
// RecordsFileVersionAndCacheKey for this flow.
//
// ref.Version is deliberately "" (a legacy/unpinned ref), not "1.5": #96
// made a non-empty ref.Version authoritative for FileIDs found upstream
// (selectVersionedDeployFiles' fast path re-resolves by version when the
// found files' effective version disagrees with the record - the hand-edited
// -YAML-drift case decision 2 targets). "1.5" here was only ever the
// mod-level label coincidentally reused for the ref before #96 existed; the
// mod-vs-file version discrepancy this test actually guards is entirely
// between mock.AddMod's Version and the served file's Version, independent
// of ref.Version - so "" (decision 1/4's exempted legacy-ref case) preserves
// the exact contract under test without asserting a real version pin.
func TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := &versionedFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "", FileIDs: []string{"1"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must record the selected file's version, not the mod's latest")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "1.0"), "cache must be keyed by the installed file's version")
}

// twoVersionSource serves two versions of the same mod's files: 1.5
// (current, ID "10", primary) and 1.0 (archived, ID "9") - the #96 fixture
// used to prove ApplyProfileSwitch's install loop resolves a pinned/recorded
// version to its matching file instead of always taking the latest/primary.
type twoVersionSource struct{ *mockSourceWithDownloads }

func (s *twoVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: mod.ID + "-old.zip", Version: "1.0", Category: "ARCHIVED"},
	}, nil
}

// newTwoVersionSource wires up a twoVersionSource with both file IDs'
// downloads registered (distinct payloads so a test can tell which file was
// actually fetched) and mod1 registered under g1.
func newTwoVersionSource(t *testing.T) *twoVersionSource {
	t.Helper()
	mock := &twoVersionSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)

	tmpDir := t.TempDir()
	newZip := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "new-payload"})
	newContent, err := os.ReadFile(newZip)
	require.NoError(t, err)
	mock.AddDownload("10", newContent)

	oldZip := createTestZip(t, tmpDir, map[string]string{"mod1-old.esp": "old-payload"})
	oldContent, err := os.ReadFile(oldZip)
	require.NoError(t, err)
	mock.AddDownload("9", oldContent)

	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"})
	return mock
}

// TestApplyProfileSwitch_HonorsProfileVersion_Downgrade is the #96 regression
// guard for selectVersionedDeployFiles' downgrade path: a profile pins mod1
// at 1.0 (hand-edited - no stored FileIDs to lean on), and the source serves
// both 1.5 (current/primary) and 1.0 (archived). The install loop must
// resolve 1.0 -> file "9" and record installed.Version == "1.0" - not fall
// back to the source's latest/primary file.
func TestApplyProfileSwitch_HonorsProfileVersion_Downgrade(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "must record the pinned version, not the source's latest")
	assert.Equal(t, []string{"9"}, installed.FileIDs, "must have selected the archived 1.0 file, not the primary 1.5 file")
}

// TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion is the #96
// healing guard: the profile's stored FileIDs ("999") no longer match
// anything upstream, but its recorded Version ("1.0") still resolves to file
// "9". Pre-#96 this hard-failed with #95's errStoredFilesUnavailable; #96
// heals it by re-resolving to the SAME version instead of erroring or
// silently taking latest.
func TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "must heal to the recorded version, not hard-fail")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)
}

// TestApplyProfileSwitch_StoredIDsGone_VersionAlsoGone_HardFails is #96's
// unresolvable case: neither the stored FileIDs ("999") nor the recorded
// Version ("0.5") match anything upstream. mod1 must fail with the extended
// #95 wording naming both the stale IDs and the missing version; mod2 (a
// normal, resolvable install) must still install, proving the install loop
// continues past the per-mod failure.
func TestApplyProfileSwitch_StoredIDsGone_VersionAlsoGone_HardFails(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.5", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "mod1", Version: "0.5", FileIDs: []string{"999"}},
			{SourceID: "src", ModID: "mod2", Version: "1.5"},
		},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err, "a per-mod resolution failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "mod2 must still install")

	var failEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError && m.ModName == "Mod One" {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "999")
	assert.Contains(t, failEvt.Detail, `version "0.5" not available`)

	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	assert.Error(t, err, "mod1 must not be installed via fallback substitution")
	_, err = svc.GetInstalledMod(context.Background(), "src", "mod2", "g1", "stable")
	assert.NoError(t, err, "mod2 must still install, proving the loop continues")
}

// TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior pins the #130
// vacuous-version rule as it interacts with #96: when the source's files
// carry no Version info at all, selectVersionedDeployFiles must fall straight
// through to the pre-#96 selectDeployFiles - including its un-extended
// errStoredFilesUnavailable wording (no "version" mention) - even though the
// profile ref itself carries a (meaningless, in this context) Version.
func TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	// mockSourceWithDownloads' embedded mockSource.GetModFiles always returns
	// a single versionless file with ID "1" - anyFileHasVersion(files) is
	// false, so version resolution must not engage at all.
	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	// Deliberately no AddDownload("1", ...) - selectDeployFiles must fail
	// before any download is attempted, matching
	// TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError's
	// idiom.

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed, "a versionless source's stale FileIDs must still hard-fail exactly as before #96")

	var failEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError && m.ModName == "Mod One" {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "999")
	assert.NotContains(t, failEvt.Detail, "not available", "a versionless file list must produce the un-extended #95 wording, not the #96 extension")

	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	assert.Error(t, err)
}

// TestApplyProfileSwitch_StoredIDsBothAgree_FastPathReturnsWholeSet is the
// #96 fix-round-1 regression guard for rule 2's fast path (previously
// untested end to end): when ALL of a mod's stored FileIDs are found
// upstream AND their combined EffectiveInstalledVersion agrees with the
// recorded version, selectVersionedDeployFiles must return the WHOLE stored
// set as-is - not re-resolve to matches(version), which would be the
// narrower set. Here ref.Version is "1.5" (the primary file10's own
// version), and the stored pair is [file10 (1.5, primary), file9 (1.0)]:
// EffectiveInstalledVersion picks file10's version ("1.5", since it's
// primary) as representative, agreeing with the record, so the fast path
// fires and keeps file9 riding along even though file9's own version isn't
// 1.5. Rule 3 (stored ∩ matches) would instead compute matches("1.5") =
// [file10] only and keep just that one file - the DIFFERENT, narrower
// outcome that distinguishes "fast path took over" from "matches-based
// resolution ran instead".
func TestApplyProfileSwitch_StoredIDsBothAgree_FastPathReturnsWholeSet(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.5", FileIDs: []string{"10", "9"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"10", "9"}, installed.FileIDs,
		"the fast path must keep the whole stored set, not re-resolve to the narrower matches(\"1.5\") set")
}

// TestApplyProfileSwitch_VersionMatchesNothing_NoStoredIDs_ErrVersionNotFoundWrap
// is the #96 fix-round-1 regression guard for rule 5 (previously untested):
// no stored FileIDs at all, and the recorded version matches nothing
// upstream. Must fail naming the version and pointing at editing the
// profile or reinstalling - not the #95 "gone upstream"/stored-IDs wording,
// which doesn't apply when there were never any stored IDs to begin with.
func TestApplyProfileSwitch_VersionMatchesNothing_NoStoredIDs_ErrVersionNotFoundWrap(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "9.9"}},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	var failEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError && m.ModName == "Mod One" {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "is not available upstream")
	assert.Contains(t, failEvt.Detail, "edit the profile's version or reinstall")
}

// TestApplyProfileSwitch_StoredIDPresentButVersionGone_NewErrorWording is
// the #96 fix-round-1 regression guard for the FINDING 2 split: when at
// least one stored FileID IS still present upstream but the recorded
// version doesn't match anything, that's a wrong version RECORD on a file
// that's still there - not a gone file - so it must get the distinct
// ErrVersionNotFound wrap (pointing at 'lmm verify --fix'/'lmm update'),
// never the #95 "no longer available upstream" wording (which implies the
// file itself is gone, misleading here). file10 (stored) resolves upstream
// fine; only the recorded version ("9.9") is bogus.
func TestApplyProfileSwitch_StoredIDPresentButVersionGone_NewErrorWording(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "9.9", FileIDs: []string{"10"}}},
	}

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	var failEvt *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstallError && m.ModName == "Mod One" {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.NotContains(t, failEvt.Detail, "no longer available upstream", "a present-upstream stored ID must not get the gone-file wording")
	assert.Contains(t, failEvt.Detail, `installed file(s) (ID(s): 10) do not match recorded version "9.9"`)
	assert.Contains(t, failEvt.Detail, "run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version")
}

// TestPlanProfileSwitch_VersionDrift_SchedulesReinstall is the #96
// convergence guard: mod1 is installed+enabled at 1.5 under "testing" (the
// current default profile), but the target profile "stable" pins it at 1.0.
// PlanProfileSwitch must classify this as ToInstall (carrying the ref as-is,
// version "1.0") rather than leaving it alone as already-satisfied - the
// version mismatch itself, not cache/enabled state, drives the decision.
func TestPlanProfileSwitch_VersionDrift_SchedulesReinstall(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "testing")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "testing"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "testing", "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "testing", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.False(t, plan.NoChanges)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToDisable)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "mod1", plan.ToInstall[0].ModID)
	assert.Equal(t, "1.0", plan.ToInstall[0].Version, "must schedule reinstall at the profile's (lower) version")
}

// TestPlanProfileSwitch_MatchingVersion_RemainsNoop is the regression guard
// for the new drift case's guard conditions: when the target ref's Version
// matches the installed mod's (or is empty), the mod must be classified
// exactly as it was before #96 - no ToInstall entry.
func TestPlanProfileSwitch_MatchingVersion_RemainsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "testing")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "testing"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "testing", "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "testing", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.True(t, plan.NoChanges, "matching version must remain a no-op, exactly as before #96")
	assert.Empty(t, plan.ToInstall)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToDisable)
}

// TestApplyProfileSwitch_Downgrade_EndToEnd is the #96 convergence
// end-to-end guard: mod1 installed+DEPLOYED at 1.5 (file "10", live on disk
// as "mod1.esp"); switching to "stable" (which pins 1.0) must plan AND apply
// a full reinstall at 1.0 - resolving to the archived file "9" via T5's
// selectVersionedDeployFiles, caching it, recording the downgrade in the DB,
// and REPLACING the live 1.5 deployment (not merely installing 1.0
// alongside it) - review round 1 finding 1: a bare Installer.Install call
// only adds the new version's files; it never removes files the old,
// still-live deployment holds that the new version doesn't serve, leaving
// mod1.esp (1.5) orphaned on disk forever (invisible to later uninstalls,
// which only ever undeploy the CURRENTLY RECORDED version's files).
func TestApplyProfileSwitch_Downgrade_EndToEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Test Mod", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.5", GameID: game.ID}, "default"))
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err, "precondition: 1.5 must be actually deployed")

	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Len(t, plan.ToInstall, 1, "the version-drifted mod must be scheduled for reinstall")

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs, "must have selected the archived 1.0 file, not the primary 1.5 file")
	assert.True(t, installed.Deployed, "the row must record that files are actually live on disk (review finding 3)")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the downgraded version must be cached")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete 1.5 file must be removed by Replace, not left behind by a bare Install (review finding 1)")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the new 1.0 file must be deployed")
}

// TestApplyProfileSwitch_PartialCacheEntry_StillDownloads is the #96 review
// round 1 finding 2 guard: a version directory can exist on disk without
// being fully populated (e.g. a previous download run that broke off before
// any file was committed, or partway through a multi-file mod). The
// cache-first guard must not mistake directory presence alone for "already
// cached" - bare cache.Exists would report true here and silently skip the
// download forever, leaving the mod stuck without its files.
func TestApplyProfileSwitch_PartialCacheEntry_StillDownloads(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// Simulate a version directory left behind by a previously broken-off
	// download run: the directory exists (bare cache.Exists would report
	// "cached") but holds none of the expected files.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.MkdirAll(gameCache.ModPath(game.ID, "src", "mod1", "1.0"), 0755))
	require.True(t, gameCache.Exists(game.ID, "src", "mod1", "1.0"), "precondition: bare Exists must see the empty dir as already cached")

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "a partial cache entry must not fool the cache-first guard into skipping the download")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, []string{"9"}, installed.FileIDs, "the download must actually have run and selected the 1.0 file")

	// The DB's FileIDs alone don't prove the download actually ran (they're
	// stamped from the SELECTED files regardless of whether the download
	// loop executed) - what distinguishes a real download from a
	// fooled-by-directory-presence skip is whether the byte content is
	// actually on disk, both in the cache and deployed into the game dir.
	cached, err := gameCache.ListFiles(game.ID, "src", "mod1", "1.0")
	require.NoError(t, err)
	assert.Contains(t, cached, "mod1-old.esp", "the cache entry must actually contain the downloaded file, not just an empty directory")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the file must actually be deployed, not merely a DB row claiming so")

	// The completed download must leave the entry MARKED, so the next
	// convergence pass over the same profile is a genuine cache hit.
	assert.True(t, gameCache.HasFileIDs(game.ID, "src", "mod1", "1.0", []string{"9"}),
		"a successful download must commit the per-file completion marker")
	assert.Equal(t, 1, mock.DownloadCount(), "exactly one download, not a retry loop")
}

// TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload is the #96 review
// round 2 guard: the cache-first guard must actually FIRE for a complete
// cache entry. Round 1 keyed the check off DownloadableFile.FileName, but the
// default DeployExtract flow stores an archive's extracted MEMBERS
// ("mod1-old.esp" here) rather than the archive's own name
// ("mod1-old.zip") - so the guard read false for essentially every
// archive-based mod and redownloaded despite a complete cache, hollowing out
// the "deploy from cache when possible" design decision (which matters most
// for downgrades, whose archived files can vanish upstream).
func TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// A COMPLETE cache entry as a real extract-mode download leaves it: the
	// archive member on disk under a name that matches nothing about the
	// DownloadableFile ("mod1-old.zip"), plus file "9"'s completion marker.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.0", "mod1-old.esp", []byte("old-payload")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "src", "mod1", "1.0"), "9"))

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	assert.Equal(t, 0, mock.DownloadCount(),
		"a fully-marked cache entry must be deployed from cache, not redownloaded")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the cached file must still be deployed")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)
}

// TestService_ApplyProfileSwitch_InstallLoop_SavesWithNormalizedGameID
// regression-guards the P3 orphaning bug class (see profile_gameid_test.go's
// CLI-level counterpart): Service.GetMod may stamp a source-mapped GameID
// onto the fetched *domain.Mod, but the InstalledMod row ApplyProfileSwitch
// saves must always be normalized to the lmm game.ID, so every other DB read
// (which queries by the lmm game ID) can find it again.
func TestService_ApplyProfileSwitch_InstallLoop_SavesWithNormalizedGameID(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	// The mock mod's own GameID deliberately differs from game.ID ("g1"),
	// mirroring what Service.GetMod stamps on when a source-mapped ID is in
	// play - the saved row must NOT inherit this value.
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "source-mapped-id"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "target")
	require.NoError(t, err, "the installed row must be visible under the lmm game ID")
	assert.Equal(t, "g1", installed.GameID, "persisted GameID must be normalized to the lmm game, not the source-mapped value")
}

// TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult
// guards the Task 2/3 error-path convention applied to ApplyProfileSwitch: a
// fatal error (here, SetDefault) must not discard the SwitchResult
// accumulated up to that point. Isolated to the simplest possible scenario
// (no disable/enable buckets) so it stands independently of
// TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure,
// which exercises the same convention but with all three buckets active.
func TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	// "target" is never created, so both UpsertMod and the final SetDefault
	// fail deterministically.

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting default profile")
	require.NotNil(t, result, "the result accumulated before the fatal error must not be discarded")
	assert.Equal(t, 1, result.Installed, "the install itself succeeded before the later fatal SetDefault error")
	assert.Empty(t, result.Notes, "#294 (Ruling 5's class extension, Task 13b): the install loop's UpsertMod failure is no longer a --verbose-only note")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "could not update profile")
}

// TestService_ApplyProfileSwitch_NilProgressCallbackIsSafe guards that
// progress may be nil per the required API (mirroring
// TestService_DeployProfile_NilProgressCallbackIsSafe).
func TestService_ApplyProfileSwitch_NilProgressCallbackIsSafe(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	disableMod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)

	plan := svc.FreshSwitchPlanForTest(context.Background(), &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	})

	assert.NotPanics(t, func() {
		result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Disabled)
	})
}

// --- Task 6 item d: cancel-then-drain (ctx checked between loop iterations) ---

// TestService_ApplyProfileSwitch_ContextCancelledBetweenDisableLoopMods_ReturnsPartialResultWithCtxErr
// guards the disable loop: a cancelled ctx aborts BETWEEN mods, never
// mid-file-operation, with the partial-result convention. The progress
// callback cancels the instant the first mod's SwitchDisabled event fires;
// the second mod must never be touched (still deployed).
func TestService_ApplyProfileSwitch_ContextCancelledBetweenDisableLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "a", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "b", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	modA, err := svc.GetInstalledMod(context.Background(), "src", "a", "g1", "default")
	require.NoError(t, err)
	modB, err := svc.GetInstalledMod(context.Background(), "src", "b", "g1", "default")
	require.NoError(t, err)

	plan := svc.FreshSwitchPlanForTest(context.Background(), &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*modA, *modB},
	})

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchDisabled && m.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Disabled)

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.NoError(t, err, "Mod B must never have been touched - still deployed")
}

// TestService_ApplyProfileSwitch_ContextCancelledBetweenEnableLoopMods_ReturnsPartialResultWithCtxErr
// mirrors the disable-loop test above for the enable loop: the second mod
// must never be deployed/enabled.
func TestService_ApplyProfileSwitch_ContextCancelledBetweenEnableLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "a", "Mod A", "1.0", false, map[string][]byte{"a.esp": []byte("a")})
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "b", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})

	modA, err := svc.GetInstalledMod(context.Background(), "src", "a", "g1", "target")
	require.NoError(t, err)
	modB, err := svc.GetInstalledMod(context.Background(), "src", "b", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*modA, *modB},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchEnabled && m.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled)

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "Mod B must never have been deployed/enabled")
}

// TestService_ApplyProfileSwitch_ContextCancelledBetweenInstallLoopMods_ReturnsPartialResultWithCtxErr
// mirrors the disable/enable-loop tests above for the install loop: the
// second ModReference must never be fetched/downloaded/installed.
func TestService_ApplyProfileSwitch_ContextCancelledBetweenInstallLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	_, err = pm.Create(context.Background(), game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	firstZip := createTestZip(t, tmpDir, map[string]string{"first.esp": "payload"})
	firstContent, err := os.ReadFile(firstZip)
	require.NoError(t, err)
	mock.AddDownload("1", firstContent)
	mock.AddMod("g1", &domain.Mod{ID: "first", SourceID: "src", Name: "First Mod", Version: "1.0", GameID: "g1"})
	secondZip := createTestZip(t, tmpDir, map[string]string{"second.esp": "payload"})
	secondContent, err := os.ReadFile(secondZip)
	require.NoError(t, err)
	mock.AddDownload("2", secondContent)
	mock.AddMod("g1", &domain.Mod{ID: "second", SourceID: "src", Name: "Second Mod", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "first", Version: "1.0"},
			{SourceID: "src", ModID: "second", Version: "1.0"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.SwitchInstalled && m.Mod != nil && m.Mod.ModID == "first" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	_, err = os.Lstat(filepath.Join(gameDir, "second.esp"))
	assert.True(t, os.IsNotExist(err), "the second ModReference must never have been fetched/installed")
	_, err = svc.GetInstalledMod(context.Background(), "src", "second", "g1", "target")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}
