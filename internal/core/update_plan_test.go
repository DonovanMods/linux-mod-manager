package core_test

// Tests for Service.PlanUpdate (v2 Phase 2 Unit I Task 9, #289): the pure,
// read-only half of the pre-extraction CLI's applySingleUpdate
// (cmd/lmm/update.go), extracted so cmd/lmm can render a plan instead of
// computing locked/pinned/recompile state itself. Reuses newFlowsTestService
// (flows_test.go), seedUpdatableMod (flows_update_test.go), updateMockSource
// (updater_test.go), and newMergedPakTestGame/seedEnabledExmodzMod/
// writeFakeBasePak (merged_pak_test.go/service_icarus_compile_test.go) - all
// in this same core_test package.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planUpdateCheckableCompilerSource wraps fakeCompilerSource with a
// CheckUpdates that returns (nil, nil) rather than source.ErrNotSupported:
// PlanUpdate's single-mod CheckGameUpdates call has no per-source partial-
// result tolerance to fall back on (mirroring applySingleUpdate's own
// pre-lift "any CheckGameUpdates error aborts the plan" behavior - see
// PlanUpdate's doc comment), so the recompile-branch fixture needs a source
// whose CheckUpdates half succeeds while its CheckMergedPakStaleness half
// still reports the staleness row.
type planUpdateCheckableCompilerSource struct {
	*fakeCompilerSource
}

func (s *planUpdateCheckableCompilerSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// newPlanUpdateRecompileTestGame mirrors newMergedPakTestGame (merged_pak_test.go)
// exactly, but registers planUpdateCheckableCompilerSource instead of a bare
// fakeCompilerSource.
func newPlanUpdateRecompileTestGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	src := &planUpdateCheckableCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	return svc, game
}

// planUpdateTestGame registers updateMockSource (updater_test.go), which has
// a real CheckUpdates comparing against a registered currentMod - the
// version-bump/up-to-date/pinned branches' fixture.
func planUpdateTestGame(t *testing.T) (*core.Service, *domain.Game, *updateMockSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	src := &updateMockSource{id: "src"}
	svc.RegisterSource(src)
	return svc, game, src
}

func TestService_PlanUpdate_UpToDate(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "1.0"} // same version: no update

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, old.ID, plan.Mod.ID)
	assert.Nil(t, plan.Update, "no update available")
	assert.False(t, plan.Pinned)
	assert.False(t, plan.Locked)
	assert.False(t, plan.RecompileNeeded)
	assert.Empty(t, plan.Refusal)
}

func TestService_PlanUpdate_Pinned(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	_, settingErr27 := svc.SetModUpdatePolicy(context.Background(), "src", "mod1", "g1", "default", domain.UpdatePinned)
	require.NoError(t, settingErr27)
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"} // a real update exists, but pinned mods are never queried

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.Pinned)
	assert.Nil(t, plan.Update, "pinned mods are filtered before the source is ever queried")
}

func TestService_PlanUpdate_VersionBumpAvailable_Unlocked(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.NotNil(t, plan.Update)
	assert.Equal(t, "2.0", plan.Update.NewVersion)
	assert.Equal(t, "Bug fixes and improvements", plan.Changelog, "CleanChangelog applied - plain text passes through unchanged")
	assert.False(t, plan.Locked)
	assert.Empty(t, plan.Refusal, "Refusal is only populated when Locked")
	assert.False(t, plan.RecompileNeeded)
}

func TestService_PlanUpdate_VersionBumpAvailable_Locked(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "mod1", "1.0"))
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.NotNil(t, plan.Update)
	assert.True(t, plan.Locked)
	assert.Equal(t, "1.0", plan.LockedVersion)
	require.NotEmpty(t, plan.Refusal, "Refusal must be populated when Locked && Update != nil")
	assert.Contains(t, plan.Refusal, "locked at v1.0")
	assert.Contains(t, plan.Refusal, "lmm mod unlock -s src -p default mod1")
	assert.NotContains(t, plan.Refusal, "lmm mod lock", "unit Q review I1: ApplyUpdate refuses on the lock alone")
	assertPlanRefusalIsSentenceHalf(t, plan.Refusal)
}

func TestService_PlanUpdate_RecompileNeeded(t *testing.T) {
	svc, game := newPlanUpdateRecompileTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	// Enabling a second mod invalidates the merged pak's fingerprint -
	// mirrors TestCheckMergedPakStaleness_StaleAfterModEnable exactly.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.RecompileNeeded)
	require.NotNil(t, plan.Update)
	assert.True(t, plan.Update.RecompileNeeded)
	assert.False(t, plan.Pinned)
	assert.False(t, plan.Locked)
}

// TestService_PlanUpdate_PerformsZeroMutations mirrors
// TestService_PlanInstall_PerformsZeroMutations (install.go): a Plan reads
// the DB, profile, and source - it must never write any of them.
func TestService_PlanUpdate_PerformsZeroMutations(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	beforeMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	beforeProfileYAML, err := os.ReadFile(profilePath)
	require.NoError(t, err)

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan)

	afterMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")

	afterProfileYAML, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.Equal(t, beforeProfileYAML, afterProfileYAML, "profile YAML must be untouched after planning")
}

// TestService_ApplyUpdate_ErrStalePlan guards Ruling 5: a plan computed
// against one installed-mod snapshot must be refused once that snapshot has
// since changed - here, a second mod is installed into the SAME profile
// between PlanUpdate and ApplyUpdate.
func TestService_ApplyUpdate_ErrStalePlan(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan.Update)

	// Installed-mod set changes after the plan was computed.
	seedInstalledMod(t, svc, game, "src", "mod2", "1.0", true, map[string][]byte{"mod2.esp": []byte("other")})

	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrStalePlan)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "a stale plan must never apply")
}

// TestService_PlanUpdateFrom_RefusesLocked guards #289 review's Important 1
// fix: applyBulkUpdate now builds its plan via PlanUpdateFrom instead of
// re-invoking PlanUpdate/CheckGameUpdates, so PlanUpdateFrom itself must
// still catch a lock taken between the batch listing (whose already-found
// domain.Update it is handed) and the bulk apply - mirroring
// TestService_PlanUpdate_VersionBumpAvailable_Locked's assertions for the
// single-mod path.
func TestService_PlanUpdateFrom_RefusesLocked(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	mod := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	// The bulk listing's own check, exactly like doUpdate's top-of-function
	// CheckGameUpdates call.
	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", []domain.InstalledMod{*mod}, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].Locked, "not yet locked at listing time")

	// Locked after the listing, before the bulk loop's own PlanUpdateFrom call.
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "mod1", "1.0"))

	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", updates[0])
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.NotNil(t, plan.Update)
	assert.True(t, plan.Locked, "PlanUpdateFrom must re-read the lock state, not trust the stale updates[0].Locked")
	assert.Equal(t, "1.0", plan.LockedVersion)
	require.NotEmpty(t, plan.Refusal, "Refusal must be populated when Locked && Update != nil")
	assert.Contains(t, plan.Refusal, "locked at v1.0")
	assert.Contains(t, plan.Refusal, "lmm mod unlock -s src -p default mod1")
	assert.NotContains(t, plan.Refusal, "lmm mod lock", "unit Q review I1: ApplyUpdate refuses on the lock alone")
	assertPlanRefusalIsSentenceHalf(t, plan.Refusal)
}

// TestService_ApplyUpdate_ErrStalePlan_FromPlanUpdateFrom is
// TestService_ApplyUpdate_ErrStalePlan's analog for the PlanUpdateFrom path:
// a plan built from an already-known domain.Update must still refuse to
// apply once the installed-mod set has moved on since it was computed
// (Ruling 5), exactly like a plan built via PlanUpdate.
func TestService_ApplyUpdate_ErrStalePlan_FromPlanUpdateFrom(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	mod := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	src.currentMod = &domain.Mod{ID: "mod1", Version: "2.0"}

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", []domain.InstalledMod{*mod}, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)

	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", updates[0])
	require.NoError(t, err)
	require.NotNil(t, plan.Update)

	// Installed-mod set changes after the listing, before the plan is
	// applied - the same race applyBulkUpdate's PlanUpdateFrom call must
	// still catch.
	seedInstalledMod(t, svc, game, "src", "mod2", "1.0", true, map[string][]byte{"mod2.esp": []byte("other")})

	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrStalePlan)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "a stale plan must never apply")
}

// TestService_CheckGameUpdates_EntriesCarryLocked guards #289's other half:
// CheckGameUpdates itself stamps Locked/LockedVersion on every returned
// entry, reading the profile once - cmd/lmm's bulk table/JSON no longer
// needs its own profile scan.
func TestService_CheckGameUpdates_EntriesCarryLocked(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a.esp": []byte("a")})
	installed, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "modA", "1.0"))
	src.currentMod = &domain.Mod{ID: "modA", Version: "2.0"}

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installed, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].Locked)
	assert.Equal(t, "1.0", updates[0].LockedVersion)
}

func TestService_CheckGameUpdates_UnlockedEntry_LockedFieldsStayZero(t *testing.T) {
	svc, game, src := planUpdateTestGame(t)
	seedUpdatableMod(t, svc, game, "src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a.esp": []byte("a")})
	installed, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	src.currentMod = &domain.Mod{ID: "modA", Version: "2.0"}

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installed, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].Locked)
	assert.Empty(t, updates[0].LockedVersion)
}

// assertPlanRefusalIsSentenceHalf pins the unit Q review's M1 contract for
// every Plan.Refusal: the field carries the refusal SENTENCE only, never the
// ErrModLocked sentinel prefix, because cmd/lmm prints it verbatim after its
// own context line and would otherwise read "mod is locked: <mod> is locked
// at ...". Prefixing the sentinel is what reproduces the error the matching
// Apply returns, which keeps the sentinel so errors.Is still works.
func assertPlanRefusalIsSentenceHalf(t *testing.T, refusal string) {
	t.Helper()
	assert.NotContains(t, refusal, core.ErrModLocked.Error()+": ",
		"M1: Plan.Refusal is the sentence half - cmd/lmm prints it verbatim")
	assert.True(t, strings.HasPrefix(refusal, "Mod One is locked at v"),
		"M1: Plan.Refusal starts at the sentence, got %q", refusal)
}
