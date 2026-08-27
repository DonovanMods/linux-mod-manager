package core_test

// Tests for Service.PlanImport/ApplyImport - the behavior-preserving
// extraction of cmd/lmm/profile.go's doProfileImport, per Phase 6b Task 8.
// See internal/core/flows.go's PlanImport/ApplyImport/ImportPlan/
// ProfileImportOptions/ProfileImportResult doc comments for the exact
// behavior under test here (note: named ProfileImportOptions/
// ProfileImportResult, not ImportOptions/ImportResult, to avoid colliding
// with importer.go's pre-existing types of those names for the unrelated
// `lmm import` local-archive command), and .superpowers/sdd/task-8-report.md
// for the full mapping/decision log.
//
// These tests reuse newFlowsTestService/seedInstalledModUnderProfile
// (flows_test.go), mockSourceWithDownloads/newMockSourceWithDownloads
// (service_test.go), createTestZip (extractor_test.go), and
// multiFileDownloadSource (flows_install_test.go) - all in this same
// core_test package.

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

// TestPlanImportCategorizes guards PlanImport's pure categorization step
// (doProfileImport :416-459): a mod already installed AND cached is
// "Installed", a mod with a DB row but no cache entry is "NeedsRedownload",
// and a mod with no DB row anywhere is "Missing" - including the
// cross-profile scan (:428-438), which must find an installed mod even when
// it lives under a DIFFERENT profile than the one being imported into.
func TestPlanImportCategorizes(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	// installed-mod: installed AND cached, but under "other" - only the
	// cross-profile scan finds it while importing into "target".
	seedInstalledModUnderProfile(t, svc, game, "other", "src", "installed-mod", "Installed Mod", "1.0", true,
		map[string][]byte{"installed.esp": []byte("i")})

	// redownload-mod: a DB row (also under "other"), but nothing in cache.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "redownload-mod", SourceID: "src", Name: "Redownload Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "other",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	// missing-mod: no DB row anywhere.

	profile := &domain.Profile{
		Name: "target", GameID: game.ID,
		Mods: []domain.ModReference{
			{SourceID: "src", ModID: "installed-mod", Version: "1.0"},
			{SourceID: "src", ModID: "redownload-mod", Version: "1.0"},
			{SourceID: "src", ModID: "missing-mod", Version: "1.0"},
		},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)

	require.Len(t, plan.Installed, 1)
	assert.Equal(t, "installed-mod", plan.Installed[0].ModID)
	require.Len(t, plan.NeedsRedownload, 1)
	assert.Equal(t, "redownload-mod", plan.NeedsRedownload[0].ModID)
	require.Len(t, plan.Missing, 1)
	assert.Equal(t, "missing-mod", plan.Missing[0].ModID)
	assert.False(t, plan.Exists, "the \"target\" profile has not been saved yet")
	assert.Equal(t, "target", plan.Profile.Name)
}

// TestApplyImportSavesAndInstalls covers ApplyImport's base case end to end:
// the profile is saved, a Missing mod is fetched/downloaded/deployed/saved
// with a nil ConfirmInstall ("nil = proceed"), and the profile is upserted
// with the actually-downloaded FileIDs.
func TestApplyImportSavesAndInstalls(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)

	var events []core.DeployProgress
	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "target", result.ProfileName)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 0, result.Failed)
	assert.Equal(t, 0, result.Skipped)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, installed.FileIDs)
	assert.True(t, installed.Enabled)
	assert.True(t, installed.Deployed, "installer.Install just succeeded, so the row must record Deployed")

	savedProfile, err := svc.NewProfileManager().Get("g1", "target")
	require.NoError(t, err)
	require.Len(t, savedProfile.Mods, 1)
	assert.Equal(t, []string{"1"}, savedProfile.Mods[0].FileIDs, "UpsertMod must record the downloaded FileIDs")

	var sawSaved, sawInstalled bool
	for _, e := range events {
		switch e.Phase {
		case core.ImportSaved:
			sawSaved = true
		case core.ImportModInstalled:
			sawInstalled = true
		}
	}
	assert.True(t, sawSaved, "expected an ImportSaved event")
	assert.True(t, sawInstalled, "expected an ImportModInstalled event")

	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.NoError(t, err, "the mod's file must be deployed")
}

// TestApplyImportConfirmInstallDeclined covers the decline path: the profile
// is still saved, but ConfirmInstall returning false must skip the install
// loop entirely - Skipped records the pending count, Installed stays zero,
// and nothing is fetched/downloaded/saved.
func TestApplyImportConfirmInstallDeclined(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)

	called := false
	opts := core.ProfileImportOptions{ConfirmInstall: func(toDownload []domain.ModReference) bool {
		called = true
		require.Len(t, toDownload, 1)
		return false
	}}
	result, err := svc.ApplyImport(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, called, "ConfirmInstall must be called when downloads are pending")
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, 0, result.Installed)
	assert.Equal(t, 0, result.Failed)

	_, err = svc.NewProfileManager().Get("g1", "target")
	require.NoError(t, err, "the profile must still be saved despite the decline")

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "target")
	assert.Error(t, err, "declining must leave zero install mutations")
}

// TestApplyImportNoInstall covers opts.NoInstall: the install loop is
// skipped unconditionally, and - unlike the decline path - ConfirmInstall
// must never even be consulted (matching doProfileImport's `if len(toDownload)
// == 0 || profileImportNoInstall` early-out, which never reaches the prompt).
func TestApplyImportNoInstall(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)

	called := false
	opts := core.ProfileImportOptions{NoInstall: true, ConfirmInstall: func([]domain.ModReference) bool {
		called = true
		return true
	}}
	result, err := svc.ApplyImport(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, called, "NoInstall must skip the install loop without ever consulting ConfirmInstall")
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, 0, result.Installed)
}

// TestApplyImportForceOverwrite covers the existing-profile gate: without
// Force, saving fails with ImportWithOptions' own "already exists" error and
// the existing profile is left untouched; with Force, the save proceeds and
// overwrites it.
func TestApplyImportForceOverwrite(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "old-mod", Version: "1.0"}))

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "new-mod", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	assert.True(t, plan.Exists, "PlanImport must report that \"target\" already exists")

	t.Run("without force returns an error and leaves the existing profile untouched", func(t *testing.T) {
		result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "profile already exists: target")
		assert.NotNil(t, result, "partial-result convention: a non-nil result must accompany the error")

		saved, gerr := pm.Get(game.ID, "target")
		require.NoError(t, gerr)
		require.Len(t, saved.Mods, 1)
		assert.Equal(t, "old-mod", saved.Mods[0].ModID)
	})

	t.Run("with force overwrites", func(t *testing.T) {
		result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Force: true, NoInstall: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, "target", result.ProfileName)

		saved, gerr := pm.Get(game.ID, "target")
		require.NoError(t, gerr)
		require.Len(t, saved.Mods, 1)
		assert.Equal(t, "new-mod", saved.Mods[0].ModID)
	})
}

// TestApplyImportPartialFailure covers the install loop's skip-and-continue
// semantics: one ref that fails to even fetch (never registered with the
// mock source) must not stop the loop - a later, valid ref still installs,
// and the failure is recorded in Failed (never fatal).
func TestApplyImportPartialFailure(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"good.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "good-mod", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: "g1"})
	// "bad-mod" is deliberately never registered - GetMod returns ErrModNotFound.

	profile := &domain.Profile{
		Name: "target", GameID: "g1",
		Mods: []domain.ModReference{
			{SourceID: "src", ModID: "bad-mod", Version: "1.0"},
			{SourceID: "src", ModID: "good-mod", Version: "1.0"},
		},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 2)

	var failedEvt core.DeployProgress
	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.ImportModFailed {
			failedEvt = p
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.Failed)
	assert.Contains(t, failedEvt.Detail, "failed to fetch mod")

	// #131: the failure must ALSO land in result.Warnings ("source:mod:
	// reason", one entry per failed mod) - the live ImportModFailed event is
	// transient, and an outcome-driven caller needs the reason to survive
	// into the completed result.
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "src:bad-mod")
	assert.Contains(t, result.Warnings[0], "failed to fetch mod")

	_, err = svc.GetInstalledMod("src", "good-mod", "g1", "target")
	assert.NoError(t, err, "the loop must continue past the bad ref and still install the good one")
	_, err = svc.GetInstalledMod("src", "bad-mod", "g1", "target")
	assert.Error(t, err)
}

// TestApplyImport_StoredFileIDsGone_FailsModWithoutSubstitution guards #95:
// when a fresh-install ref's own FileIDs don't match any file the source
// currently offers, ApplyImport must fail that mod (ImportModFailed +
// Failed++, via selectDeployFiles's allowFallback=false) rather than
// silently substituting the primary file (the old ImportFallbackUsed phase,
// removed entirely in the renderer-cleanup task B2) - and the loop must
// continue past it, mirroring TestApplyImportPartialFailure.
func TestApplyImport_StoredFileIDsGone_FailsModWithoutSubstitution(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"good.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "bad-mod", SourceID: "src", Name: "Bad Mod", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "good-mod", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{
		Name: "target", GameID: "g1",
		Mods: []domain.ModReference{
			// "stale-id" doesn't match the mock source's file ID ("1") - must
			// fail, not silently fall back to the primary file.
			{SourceID: "src", ModID: "bad-mod", Version: "1.0", FileIDs: []string{"stale-id"}},
			{SourceID: "src", ModID: "good-mod", Version: "1.0"},
		},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 2)

	var failedEvt core.DeployProgress
	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.ImportModFailed {
			failedEvt = p
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.Failed)
	assert.Contains(t, failedEvt.Detail, "no longer available upstream")
	assert.Contains(t, failedEvt.Detail, "stale-id")

	// #131: the stored-files-gone reason carries a remediation hint the user
	// must be able to READ after the run - it has to survive into
	// result.Warnings, not just the transient ImportModFailed event.
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "src:bad-mod")
	assert.Contains(t, result.Warnings[0], "no longer available upstream")

	_, err = svc.GetInstalledMod("src", "good-mod", "g1", "target")
	assert.NoError(t, err, "the loop must continue past the bad ref and still install the good one")
	_, err = svc.GetInstalledMod("src", "bad-mod", "g1", "target")
	assert.Error(t, err, "bad-mod must not be installed via fallback substitution")
}

// TestApplyImportRedownloadUsesStoredFileIDs pins doProfileImport's :541-552
// rule: a NeedsRedownload mod must re-fetch using the DB row's OWN FileIDs,
// never the profile YAML's ref.FileIDs (which may be empty or stale) - the
// exact behavior ImportPlan's private storedFileIDs lookup exists to
// preserve from PlanImport through to ApplyImport.
func TestApplyImportRedownloadUsesStoredFileIDs(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	base := newMockSourceWithDownloads("src")
	defer base.Close()
	mock := &multiFileDownloadSource{mockSourceWithDownloads: base, files: []domain.DownloadableFile{
		{ID: "primary", Name: "Primary", FileName: "primary.esp", IsPrimary: true},
		{ID: "stored", Name: "Stored", FileName: "stored.esp"},
	}}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	primaryZip := createTestZip(t, t.TempDir(), map[string]string{"primary.esp": "primary-payload"})
	primaryContent, err := os.ReadFile(primaryZip)
	require.NoError(t, err)
	mock.AddDownload("primary", primaryContent)

	storedZip := createTestZip(t, t.TempDir(), map[string]string{"stored.esp": "stored-payload"})
	storedContent, err := os.ReadFile(storedZip)
	require.NoError(t, err)
	mock.AddDownload("stored", storedContent)

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	// A prior install recorded FileIDs=["stored"], but its cache entry is
	// gone - a cache-miss redownload.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"stored"},
	}))

	// The imported profile carries NO FileIDs of its own - proving ApplyImport
	// consults the DB-stored ones, not ref.FileIDs.
	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, []string{"stored"}, installed.FileIDs, "the redownload must fetch the DB-stored file, not the primary")

	_, statErr := os.Lstat(filepath.Join(gameDir, "stored.esp"))
	assert.NoError(t, statErr, "the stored file must be deployed")
	_, statErr = os.Lstat(filepath.Join(gameDir, "primary.esp"))
	assert.True(t, os.IsNotExist(statErr), "the primary file must NOT have been downloaded/deployed")
}

// TestApplyImport_InstallLoop_RecordsFileVersion is the #94 regression test
// for ApplyImport's install loop: like
// TestApplyImportRedownloadUsesStoredFileIDs above, a prior install recorded
// FileIDs=["1"] with no matching cache entry (forcing a redownload), but
// here the source's served file "1" carries its own Version ("1.0"),
// distinct from the mod-level Version ("1.5"). The saved InstalledMod row
// and cache dir must be stamped with the file's version, not the mod's -
// mirroring flows_test.go's TestService_ApplyProfileSwitch_InstallLoop_
// RecordsFileVersion for this flow. versionedFileSource is defined in
// flows_test.go (same core_test package).
//
// The imported profile's ref.Version is deliberately "" (a legacy/unpinned
// ref), not "1.5" - see TestService_ApplyProfileSwitch_InstallLoop_
// RecordsFileVersion's doc comment for why: #96 made a non-empty ref.Version
// authoritative for FileIDs found upstream, and "1.5" here was only ever
// mock.AddMod's mod-level label, unrelated to what ref.Version should mean
// as a version pin.
func TestApplyImport_InstallLoop_RecordsFileVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &versionedFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"})

	zipPath := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	// A prior install recorded FileIDs=["1"], but its cache entry is gone -
	// a cache-miss redownload, matching TestApplyImportRedownloadUsesStoredFileIDs's setup.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"1"},
	}))

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: ""}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must record the selected file's version, not the mod's latest")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "1.0"), "cache must be keyed by the installed file's version")
}

// TestApplyImport_StoredIDsGone_HealsToRecordedVersion is #96's ApplyImport
// healing guard, mirroring flows_test.go's
// TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion for this flow:
// mod1 is Missing (no DB row), and the imported profile's own ref carries
// FileIDs ["999"] that don't match anything upstream, but its Version
// ("1.0") still resolves to the source's archived file "9". Pre-#96 this
// hard-failed with #95's errStoredFilesUnavailable; #96 heals it by
// re-resolving to the SAME version instead of erroring or silently taking
// the source's current primary (1.5/"10"). twoVersionSource/
// newTwoVersionSource are defined in flows_test.go (same core_test
// package).
func TestApplyImport_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	profile := &domain.Profile{
		Name: "target", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "must heal to the recorded version, not hard-fail")
	assert.Equal(t, 0, result.Failed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)

	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.NoError(t, statErr, "the recorded 1.0 file's payload should be deployed, not the source's current primary")
}

// TestApplyImport_VersionlessSource_KeepsLegacyBehavior pins the #130
// vacuous-version rule as it interacts with #96, mirroring flows_test.go's
// TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior for this
// flow: when the source's files carry no Version info at all,
// selectVersionedDeployFiles must fall straight through to the pre-#96
// selectDeployFiles - including its un-extended errStoredFilesUnavailable
// wording (no "version" mention) - even though the imported ref itself
// carries a (meaningless, in this context) Version.
func TestApplyImport_VersionlessSource_KeepsLegacyBehavior(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// mockSourceWithDownloads' embedded mockSource.GetModFiles always returns
	// a single versionless file with ID "1" - anyFileHasVersion(files) is
	// false, so version resolution must not engage at all.
	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	// Deliberately no AddDownload("1", ...) - selectDeployFiles must fail
	// before any download is attempted.

	profile := &domain.Profile{
		Name: "target", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)

	var failedEvt core.DeployProgress
	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.ImportModFailed {
			failedEvt = p
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed, "a versionless source's stale FileIDs must still hard-fail exactly as before #96")
	assert.Equal(t, 1, result.Failed)

	assert.Contains(t, failedEvt.Detail, "no longer available upstream")
	assert.Contains(t, failedEvt.Detail, "999")
	assert.NotContains(t, failedEvt.Detail, "not available", "a versionless file list must produce the un-extended #95 wording, not the #96 extension")

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "target")
	assert.Error(t, err)
}

// TestPlanImport_VersionDrift_SchedulesReinstall guards #138's classification
// fix: a mod already installed (and cached) at a DIFFERENT version than the
// imported profile records must be scheduled for reinstall at the profile's
// version (NeedsRedownload), never classified as "Installed - nothing to do"
// - mirroring PlanProfileSwitch's #96 drift case. A ref at the SAME version,
// or one with no version at all (legacy/unpinned), keeps the pre-#138
// classification.
func TestPlanImport_VersionDrift_SchedulesReinstall(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	// All three installed AND cached at their recorded versions - pre-#138,
	// every one of them would land in Installed.
	seedInstalledModUnderProfile(t, svc, game, "other", "src", "drifted-mod", "Drifted Mod", "1.5", true,
		map[string][]byte{"drifted.esp": []byte("d")})
	seedInstalledModUnderProfile(t, svc, game, "other", "src", "same-mod", "Same Mod", "1.0", true,
		map[string][]byte{"same.esp": []byte("s")})
	seedInstalledModUnderProfile(t, svc, game, "other", "src", "unpinned-mod", "Unpinned Mod", "1.5", true,
		map[string][]byte{"unpinned.esp": []byte("u")})

	profile := &domain.Profile{
		Name: "target", GameID: game.ID,
		Mods: []domain.ModReference{
			{SourceID: "src", ModID: "drifted-mod", Version: "1.0"},
			{SourceID: "src", ModID: "same-mod", Version: "1.0"},
			{SourceID: "src", ModID: "unpinned-mod", Version: ""},
		},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)

	require.Len(t, plan.NeedsRedownload, 1, "the version-drifted mod must be scheduled for reinstall at the profile's version")
	assert.Equal(t, "drifted-mod", plan.NeedsRedownload[0].ModID)
	assert.Equal(t, "1.0", plan.NeedsRedownload[0].Version, "the reinstall must target the imported profile's version, not the installed one")
	require.Len(t, plan.Installed, 2, "same-version and unpinned refs keep the pre-#138 classification")
	assert.Equal(t, "same-mod", plan.Installed[0].ModID)
	assert.Equal(t, "unpinned-mod", plan.Installed[1].ModID)
	assert.Empty(t, plan.Missing)
}

// TestApplyImport_Downgrade_EndToEnd is #138's convergence guard, mirroring
// TestApplyProfileSwitch_Downgrade_EndToEnd (flows_test.go) for the import
// flow: mod1 installed+DEPLOYED at 1.5 (file "10", live on disk as
// "mod1.esp"); importing a profile that pins 1.0 must plan AND apply a full
// reinstall at 1.0 - resolving to the archived file "9" via
// selectVersionedDeployFiles, caching it, recording the downgrade in the DB,
// and REPLACING the live 1.5 deployment (not merely installing 1.0 alongside
// it). Pre-#138, PlanImport classified the mod as Installed ("nothing to
// do") and the 1.5 deployment stayed live until the next apply/switch.
//
// The imported ref also carries a #97 lock at 1.0, covering the issue's
// pairing: a lock imported from a shared profile takes effect - converged
// and recorded - without a second command.
func TestApplyImport_Downgrade_EndToEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Test Mod", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.5", GameID: game.ID}, "default"))
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err, "precondition: 1.5 must be actually deployed")
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	profile := &domain.Profile{
		Name: "stable", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", Locked: true}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1, "the version-drifted mod must be scheduled for reinstall")
	assert.Empty(t, plan.Installed, "a drifted mod must not be classified as already-installed")

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 0, result.Failed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs, "must have selected the archived 1.0 file, not the primary 1.5 file")
	assert.True(t, installed.Deployed, "the row must record that files are actually live on disk")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the downgraded version must be cached")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete 1.5 file must be removed by Replace, not left behind by a bare Install")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the new 1.0 file must be deployed")

	saved, err := pm.Get("g1", "stable")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	assert.True(t, saved.Mods[0].Locked, "the imported lock must survive the convergence UpsertMod")
	assert.Equal(t, "1.0", saved.Mods[0].Version)
}

// TestApplyImport_FullyMarkedCache_SkipsDownload mirrors
// TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload (flows_test.go) for
// the import flow's #138 convergence: when the imported profile's recorded
// version is already fully cached (payload + per-file completion markers),
// the reinstall must deploy from cache without redownloading - which matters
// most for exactly this drift case, a downgrade whose archived file may have
// vanished upstream - while still REPLACING the live drifted deployment.
func TestApplyImport_FullyMarkedCache_SkipsDownload(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// 1.5 installed+deployed (the drifted live version), and 1.0 COMPLETELY
	// cached as a real extract-mode download leaves it: the archive member on
	// disk plus file "9"'s completion marker.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.0", "mod1-old.esp", []byte("old-payload")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "src", "mod1", "1.0"), "9"))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Test Mod", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.5", GameID: game.ID}, "default"))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	profile := &domain.Profile{
		Name: "stable", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	assert.Equal(t, 0, mock.DownloadCount(),
		"a fully-marked cache entry must be deployed from cache, not redownloaded")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the drifted 1.5 deployment must still be replaced, even on a cache hit")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the cached 1.0 file must be deployed")
}

// TestApplyImportCtxCancelled covers the loop's cancellation check: it must
// be honored BETWEEN mods (never mid-file-operation), matching
// DeployProfile/ApplyProfileSwitch's identical check - the quit-drain
// compatibility every core flow's per-mod loop shares.
func TestApplyImportCtxCancelled(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"mod.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{
		Name: "target", GameID: "g1",
		Mods: []domain.ModReference{
			{SourceID: "src", ModID: "mod1", Version: "1.0"},
			{SourceID: "src", ModID: "mod2", Version: "1.0"},
		},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 2)

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyImport(ctx, game, plan, core.ProfileImportOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.ImportModInstalled {
			cancel() // cancel right after the first mod finishes, before the second's iteration starts
		}
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result, "partial result (the first mod's success) must not be discarded")
	assert.Equal(t, 1, result.Installed, "the first mod must finish before cancellation is honored")

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "target")
	assert.NoError(t, err, "mod1 must have completed before cancellation")
	_, err = svc.GetInstalledMod("src", "mod2", "g1", "target")
	assert.Error(t, err, "mod2 must never have been attempted once ctx was cancelled")
}
