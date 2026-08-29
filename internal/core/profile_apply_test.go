package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for PlanProfileApply/ApplyProfileApply - the core twin of the CLI's
// `lmm profile apply` (v2 Phase 2 Unit J Task 14, #290). cmd/lmm's
// TestDoProfileApply_* tests keep pinning the printed lines; these pin the
// classification, the end state, the event stream and the plan-staleness
// guard.

// newApplyTestService builds a service plus a game with an empty "default"
// profile - the shape every test below starts from.
func newApplyTestService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	return svc, game
}

// applyModPhases returns the phase sequence of every mod-lifecycle event in
// order, dropping DownloadEvent ticks (whose count depends on the transfer's
// chunking, not on the flow).
func applyModPhases(events []core.Event) []core.DeployPhase {
	var phases []core.DeployPhase
	for _, e := range events {
		if _, isDownload := e.(core.DownloadEvent); isDownload {
			continue
		}
		if fe, ok := e.(core.FlowEvent); ok {
			phases = append(phases, fe.FlowPhase())
		}
	}
	return phases
}

func TestPlanProfileApply_ProfileNotFound(t *testing.T) {
	svc, game := newApplyTestService(t)

	_, err := svc.PlanProfileApply(context.Background(), game, "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found: ghost")
}

// TestPlanProfileApply_NoChanges: an installed+enabled mod at the profile's
// own version needs nothing.
func TestPlanProfileApply_NoChanges(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
	assert.Empty(t, plan.ToDisable)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToInstall)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.Profile)
}

// TestPlanProfileApply_ClassifiesDisableEnableInstall covers the three plain
// buckets at once: installed+enabled but absent from the profile (disable),
// installed+disabled+cached and listed (enable), listed but not installed
// (install).
func TestPlanProfileApply_ClassifiesDisableEnableInstall(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newMockSourceWithDownloads("src"))

	seedInstalledMod(t, svc, game, "src", "gone", "1.0", true, map[string][]byte{"gone.esp": []byte("x")})
	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "new", Version: ""}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	assert.False(t, plan.NoChanges)

	require.Len(t, plan.ToDisable, 1)
	assert.Equal(t, "gone", plan.ToDisable[0].ID)
	require.Len(t, plan.ToEnable, 1)
	assert.Equal(t, "off", plan.ToEnable[0].ID)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "new", plan.ToInstall[0].Ref.ModID)
	assert.Nil(t, plan.ToInstall[0].Replaces)
}

// TestPlanProfileApply_DisabledModCacheGone_SchedulesInstallWithDBFileIDs:
// a disabled profile mod whose cache entry is gone cannot be re-deployed, so
// it becomes an install carrying the DB row's own FileIDs (not the
// profile's, which may be empty or stale).
func TestPlanProfileApply_DisabledModCacheGone_SchedulesInstallWithDBFileIDs(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
		FileIDs:      []string{"9"},
	}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	assert.Empty(t, plan.ToEnable, "a mod with no cache entry must not be a plain enable")
	require.Len(t, plan.ToInstall, 1)
	entry := plan.ToInstall[0]
	assert.Equal(t, []string{"9"}, entry.Ref.FileIDs, "the DB row's FileIDs drive the re-download")
	assert.Equal(t, "1.0", entry.Version)
	assert.False(t, entry.Cached)
	require.Len(t, entry.Files, 1)
	assert.Equal(t, "9", entry.Files[0].ID)
	assert.Nil(t, entry.Replaces, "a cache-miss redownload is not a replace")
}

// TestPlanProfileApply_VersionDrift_RecordsReplaces is the #96 convergence
// classification: a mod installed at a different version than the profile
// pins is scheduled for reinstall at the PROFILE's version (downgrades
// included), and a live older deployment whose cache entry is intact is
// recorded so the apply can Replace it rather than install over it.
func TestPlanProfileApply_VersionDrift_RecordsReplaces(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, plan.ToInstall, 1)
	entry := plan.ToInstall[0]
	assert.Equal(t, "1.0", entry.Ref.Version)
	assert.Equal(t, "1.0", entry.Version, "the target version is the profile's, not the installed row's")
	require.Len(t, entry.Files, 1)
	assert.Equal(t, "9", entry.Files[0].ID, "the 1.0 file must be selected")
	require.NotNil(t, entry.Replaces)
	assert.Equal(t, "1.5", entry.Replaces.Version)

	// The same drift with the OLD cache entry pruned falls back to a plain
	// install: Installer.Replace reads the old entry to work out which files
	// to retire and hard-fails without it.
	require.NoError(t, gameCache.Delete(game.ID, "src", "mod1", "1.5"))
	plan, err = svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.Nil(t, plan.ToInstall[0].Replaces, "a pruned old cache entry must fall back to Install")
}

// TestPlanProfileApply_MatchingVersion_IsNoop guards the drift check's guard
// condition: a profile ref at the installed row's own version is satisfied.
func TestPlanProfileApply_MatchingVersion_IsNoop(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "mod1", "1.5", true, map[string][]byte{"mod1.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
}

// TestPlanProfileApply_ResolvesModAndCacheState: the plan resolves each
// install entry against its source (mod, selected files, the #94 effective
// version) and records whether the cache already holds it.
func TestPlanProfileApply_ResolvesModAndCacheState(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	// A COMPLETE cache entry for 1.5 (file "10"): the plan must see it.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "src", "mod1", "1.5"), "10"))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, plan.ToInstall, 1)
	entry := plan.ToInstall[0]
	require.NotNil(t, entry.Mod)
	assert.Equal(t, "Mod One", entry.Mod.Name)
	assert.Equal(t, "1.5", entry.Version)
	assert.True(t, entry.Cached, "a fully-marked cache entry must be reported as cached")
	assert.Empty(t, entry.Error)
}

// TestPlanProfileApply_FetchFailure_RecordsEntryError: a source failure at
// plan time fails that ONE entry (recorded as text, the error-as-text
// convention) and leaves the rest of the plan intact.
func TestPlanProfileApply_FetchFailure_RecordsEntryError(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	mock := newMockSourceWithDownloads("src")
	t.Cleanup(mock.Close)
	mock.AddMod(game.ID, &domain.Mod{ID: "good", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: game.ID})
	svc.RegisterSource(mock)

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "ghost"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "good"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err, "one unresolvable mod must not fail the whole plan")

	require.Len(t, plan.ToInstall, 2)
	assert.Equal(t, "ghost", plan.ToInstall[0].Ref.ModID)
	assert.Contains(t, plan.ToInstall[0].Error, "failed to fetch mod:")
	assert.Nil(t, plan.ToInstall[0].Mod)
	assert.Equal(t, "good", plan.ToInstall[1].Ref.ModID)
	assert.Empty(t, plan.ToInstall[1].Error)
}

// TestPlanProfileApply_OrdersBucketsByProfile is the core twin of the CLI's
// TestDoProfileApply_PrintsDeterministicOrder_MatchesProfileMods: mods
// absent from profile.Mods sort by key, listed mods keep their profile
// order, and the install bucket runs the installed-mods loop's entries
// (convergence, cache-miss redownloads) before the profile loop's brand-new
// ones.
func TestPlanProfileApply_OrdersBucketsByProfile(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newMockSourceWithDownloads("src"))

	for _, id := range []string{"disC", "disB", "disA"} {
		seedInstalledMod(t, svc, game, "src", id, "1.0", true, map[string][]byte{id + ".esp": []byte("x")})
	}
	for _, id := range []string{"enA", "enB", "enC"} {
		seedInstalledMod(t, svc, game, "src", id, "1.0", false, map[string][]byte{id + ".esp": []byte("x")})
	}
	for _, id := range []string{"enC", "insB", "enA", "insC", "enB", "insA"} {
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	var disabled, enabled, installs []string
	for _, im := range plan.ToDisable {
		disabled = append(disabled, im.ID)
	}
	for _, im := range plan.ToEnable {
		enabled = append(enabled, im.ID)
	}
	for _, e := range plan.ToInstall {
		installs = append(installs, e.Ref.ModID)
	}
	assert.Equal(t, []string{"disA", "disB", "disC"}, disabled, "mods unlisted in the profile sort by key")
	assert.Equal(t, []string{"enC", "enA", "enB"}, enabled, "listed mods keep profile order")
	assert.Equal(t, []string{"insB", "insC", "insA"}, installs, "listed mods keep profile order")
}

// TestApplyProfileApply_DisableEnable_EndStateAndEvents pins the disable and
// enable loops: DB rows flip, the enabled mod is deployed, the disabled
// one's files are gone, and both loops report through the event stream.
func TestApplyProfileApply_DisableEnable_EndStateAndEvents(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "gone", "1.0", true, map[string][]byte{"gone.esp": []byte("x")})
	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))

	installer, err := svc.GetInstallerForProfileForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.NoError(t, installer.Install(context.Background(), game,
		&domain.Mod{ID: "gone", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Disabled)
	assert.Equal(t, 1, result.Enabled)
	assert.Empty(t, result.Failed)

	assert.Equal(t, []core.DeployPhase{core.SwitchDisabled, core.SwitchEnabled}, applyModPhases(*events))
	for _, e := range *events {
		fe, ok := e.(core.FlowEvent)
		require.True(t, ok)
		assert.Equal(t, core.OpProfileApply, fe.EventScope().Op)
	}

	off, err := svc.GetInstalledMod(context.Background(), "src", "off", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, off.Enabled)
	gone, err := svc.GetInstalledMod(context.Background(), "src", "gone", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, gone.Enabled)

	_, err = os.Lstat(filepath.Join(game.ModPath, "off.esp"))
	assert.NoError(t, err, "the enabled mod must be deployed")
	_, err = os.Lstat(filepath.Join(game.ModPath, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "the disabled mod must be undeployed")
}

// TestApplyProfileApply_InstallLoop_DownloadsSavesAndUpserts pins the
// install loop end to end: the file is downloaded and cached, the DB row is
// saved with the lmm game ID (never the source-mapped one), the profile ref
// records the downloaded FileIDs, and the events run
// Installing -> InstallingMod -> ... -> Installed.
func TestApplyProfileApply_InstallLoop_DownloadsSavesAndUpserts(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	game.SourceIDs = map[string]string{"src": "external-id"}

	mock := newTwoVersionSource(t)
	mock.AddMod("external-id", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "external-id"})
	svc.RegisterSource(mock)

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	require.Empty(t, plan.ToInstall[0].Error)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 0, result.Replaced)
	assert.Empty(t, result.Failed)

	assert.Equal(t, []core.DeployPhase{
		core.SwitchInstalling, core.SwitchInstallingMod, core.SwitchDownloadDone, core.SwitchInstalled,
	}, applyModPhases(*events))

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "g1", installed.GameID, "the row must be filed under the lmm game ID")
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)
	assert.True(t, installed.Enabled)
	assert.True(t, installed.Deployed)

	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, []string{"9"}, profile.Mods[0].FileIDs, "the profile ref must record the downloaded FileIDs")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the downloaded file must be deployed")
}

// TestApplyProfileApply_CachedEntry_SkipsDownload: a plan entry the cache
// already holds in full deploys straight from cache, with no download and no
// SwitchDownloadDone event (there is no progress readout to terminate).
func TestApplyProfileApply_CachedEntry_SkipsDownload(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "src", "mod1", "1.5"), "10"))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.True(t, plan.ToInstall[0].Cached)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 0, mock.DownloadCount(), "a fully-marked cache entry must not be re-downloaded")
	assert.NotContains(t, applyModPhases(*events), core.SwitchDownloadDone)

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.NoError(t, err, "the cached file must be deployed")
}

// TestApplyProfileApply_VersionDrift_ReplacesLiveDeployment is the #96
// end-to-end guard: converging a deployed 1.5 down to 1.0 must retire the
// obsolete file via Installer.Replace, not leave it alongside the new one.
func TestApplyProfileApply_VersionDrift_ReplacesLiveDeployment(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	installer, err := svc.GetInstallerForProfileForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.NoError(t, installer.Install(context.Background(), game,
		&domain.Mod{ID: "mod1", SourceID: "src", Version: "1.5", GameID: game.ID}, "default"))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, plan.ToInstall[0].Replaces)

	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Replaced, "a replace is counted separately from a plain install")
	assert.Equal(t, 0, result.Installed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete 1.5 file must be retired by Replace")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the 1.0 file must be deployed")
}

// TestApplyProfileApply_EntryError_ReportsAndContinues: a plan entry that
// failed to resolve is reported at its position in the loop (so the CLI can
// print it under that mod's "Installing ..." line) and the loop carries on.
func TestApplyProfileApply_EntryError_ReportsAndContinues(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "ghost"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 2)
	require.NotEmpty(t, plan.ToInstall[0].Error)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the healthy mod must still install")
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0], "ghost")

	phases := applyModPhases(*events)
	assert.Equal(t, []core.DeployPhase{
		core.SwitchInstalling, core.SwitchInstallingMod, core.SwitchInstallError,
		core.SwitchInstallingMod, core.SwitchDownloadDone, core.SwitchInstalled,
	}, phases)
}

// TestApplyProfileApply_EnableLoop_InstallFailure_NotesAndSkipsEnable pins
// review Important 2(a): the enable loop's failure semantics are the one
// asymmetry with its disable-loop neighbour. Disable reports "✓ Disabled"
// unconditionally even after a failed Uninstall (SwitchDisableNote fires,
// but so does SwitchDisabled); a failed enable-loop Install must instead
// skip the mod entirely - a Note, no SwitchEnabled event, and the enabled
// flag left false in the DB - exactly as doProfileApply's `continue` did.
func TestApplyProfileApply_EnableLoop_InstallFailure_NotesAndSkipsEnable(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 1)

	// Prune the cache entry PlanProfileApply saw between plan and apply, so
	// installer.Install fails deterministically ("mod not in cache") -
	// the same external-pruning race M2's doc comment names for the
	// Replace-vs-Install decision, reused here on purpose to force the
	// enable loop's own failure branch without touching filesystem
	// permissions.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Delete(game.ID, "src", "off", "1.0"))

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Enabled, "a failed deploy must not count as an enable")
	require.Len(t, result.Notes, 1)
	assert.Contains(t, result.Notes[0], "Warning: failed to deploy Test Mod: ")

	phases := applyModPhases(*events)
	assert.Equal(t, []core.DeployPhase{core.SwitchEnableNote}, phases, "no SwitchEnabled event may follow a failed deploy")

	off, err := svc.GetInstalledMod(context.Background(), "src", "off", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, off.Enabled, "the enabled flag must be left unset")
}

// TestApplyProfileApply_InstallLoop_DownloadFailure_DownloadDoneFollowsFailure
// pins review Important 2(b): a download failure inside the install loop
// still emits SwitchDownloadDone right after SwitchDownloadFailed (the
// unconditional Println terminating doProfileApply's carriage-returned
// progress line, on the failure path too), the failed mod is recorded and
// skipped, and the loop continues on to install a later healthy entry.
func TestApplyProfileApply_InstallLoop_DownloadFailure_DownloadDoneFollowsFailure(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	// perModFileSource keys its one downloadable file by the mod's own ID
	// (flows_install_test.go), so "bad" and "good" can be given independent
	// download outcomes on the same mockSourceWithDownloads server: "bad"
	// gets no registered payload and 404s, "good" downloads cleanly.
	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	mock.AddMod(game.ID, &domain.Mod{ID: "bad", SourceID: "src", Name: "Bad Mod", Version: "1.0", GameID: game.ID})
	registerDownloadableMod(t, mock, &domain.Mod{ID: "good", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: game.ID}, "good.esp", "plugin content")
	svc.RegisterSource(mock)

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "bad"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "good"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 2)
	require.Empty(t, plan.ToInstall[0].Error)
	require.Empty(t, plan.ToInstall[1].Error)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the healthy mod must still install")
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0], "bad")
	assert.Contains(t, result.Failed[0], "download failed")

	phases := applyModPhases(*events)
	assert.Equal(t, []core.DeployPhase{
		core.SwitchInstalling, core.SwitchInstallingMod, core.SwitchDownloadFailed, core.SwitchDownloadDone,
		core.SwitchInstallingMod, core.SwitchDownloadDone, core.SwitchInstalled,
	}, phases, "SwitchDownloadDone must follow SwitchDownloadFailed just as it follows a successful download")

	_, err = svc.GetInstalledMod(context.Background(), "src", "bad", game.ID, "default")
	assert.Error(t, err, "a mod whose download failed must not be installed")
	good, err := svc.GetInstalledMod(context.Background(), "src", "good", game.ID, "default")
	require.NoError(t, err, "the loop must continue past the failure")
	assert.True(t, good.Enabled)
}

// TestApplyProfileApply_LockedRef_UpsertRefusalIsNote pins ruling 9: the
// post-install UpsertMod is refused by a LOCKED profile ref (#143), and the
// flow records that as a Note (the CLI's --verbose-only warning) without
// failing the mod. The behaviour fix is deferred to Phase 3.
func TestApplyProfileApply_LockedRef_UpsertRefusalIsNote(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Locked: true}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the refusal must not fail the install")
	require.Len(t, result.Notes, 1)
	assert.Contains(t, result.Notes[0], "Warning: could not update profile: ")
	assert.Contains(t, result.Notes[0], "is locked at v")
	assert.Contains(t, applyModPhases(*events), core.SwitchInstallNote)

	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods[0].Version, "the locked ref must be left unwritten")
}

// TestApplyProfileApply_StalePlan is ruling 5: a plan computed against an
// installed-mod set that has since changed is refused, so a frontend that
// held a plan across a mutation re-plans instead of applying a stale diff.
func TestApplyProfileApply_StalePlan(t *testing.T) {
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 1)

	// Something else enables the mod behind the plan's back.
	require.NoError(t, svc.SetModEnabledForTest(context.Background(), "src", "off", game.ID, "default", true))

	_, err = svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}
