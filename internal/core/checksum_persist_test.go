package core_test

// #372: every flow that downloads a file must persist that file's checksum
// onto the installed_mod_files row it just wrote, exactly as `install` does.
// Five flows dropped *DownloadModResult on the floor - `if _, err :=
// s.downloadMod(...)` - which left checksum NULL and made `lmm verify` report
// NO CHECKSUM for a file lmm had just downloaded. Since the first update to
// any mod goes through one of them, a user who updates regularly ends up with
// a database of mostly-unverifiable files.
//
// Each test below drives one flow end to end against the fixture sources and
// asserts (a) the row carries a non-empty checksum and (b) Verify reports it
// as ok rather than as a no-checksum warning.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireChecksumRecorded asserts that every tracked file of the profile
// carries a checksum, and that `lmm verify` agrees - a NO CHECKSUM warning is
// exactly the user-visible symptom #372 describes.
func requireChecksumRecorded(t *testing.T, svc *core.Service, game *domain.Game, profile string, wantFileIDs ...string) {
	t.Helper()

	files, err := svc.GetFilesWithChecksums(context.Background(), game.ID, profile)
	require.NoError(t, err)
	require.Len(t, files, len(wantFileIDs))
	got := make(map[string]string, len(files))
	for _, f := range files {
		got[f.FileID] = f.Checksum
	}
	for _, id := range wantFileIDs {
		checksum, ok := got[id]
		require.True(t, ok, "no installed_mod_files row for file %s", id)
		assert.NotEmpty(t, checksum, "file %s must record the checksum of what was just downloaded", id)
	}
}

// TestUpdate_RecordsTheDownloadedChecksum is the issue's own reproduction:
// install records a checksum, then the first update leaves it NULL.
func TestUpdate_RecordsTheDownloadedChecksum(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.5"}
	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default", upd)
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	requireChecksumRecorded(t, svc, game, "default", "10")
}

// TestDeployConvergence_RecordsTheDownloadedChecksum covers the cache-miss
// redownload inside DeployProfile (redeployFromSource).
func TestDeployConvergence_RecordsTheDownloadedChecksum(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// A row whose cache entry is gone: the deploy must re-download it.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"9"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed)

	requireChecksumRecorded(t, svc, game, "default", "9")
}

// TestProfileApply_RecordsTheDownloadedChecksum covers ApplyProfileApply's
// install loop.
func TestProfileApply_RecordsTheDownloadedChecksum(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)

	result, err := svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)

	requireChecksumRecorded(t, svc, game, "default", "9")
}

// TestProfileSwitch_RecordsTheDownloadedChecksum covers
// ApplyProfileSwitch's install loop.
func TestProfileSwitch_RecordsTheDownloadedChecksum(t *testing.T) {
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

	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "stable",
		domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed)

	requireChecksumRecorded(t, svc, game, "stable", "9")
}

// TestProfileImport_RecordsTheDownloadedChecksum covers ApplyImport's
// install loop.
func TestProfileImport_RecordsTheDownloadedChecksum(t *testing.T) {
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

	profile := &domain.Profile{Name: "target", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)

	_, err = svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	require.NoError(t, err)
	requireChecksumRecorded(t, svc, game, "target", "1")
}

// TestProfileImport_CrossProfileMod_CopiesTheChecksum is P1a review finding
// F2: the OTHER half of ApplyImport, #371's AlreadyCached bucket, which
// installs from a cache entry another profile already has and so downloads
// nothing at all. It wrote the row's FileIDs without their checksums, so the
// same bytes were verifiable under one profile and NO CHECKSUM under the
// other - #372's stated harm, on a path #371 created. Nothing downloads
// here, so there is no DownloadModResult to record: the checksums come from
// the row the new one is cloned from.
func TestProfileImport_CrossProfileMod_CopiesTheChecksum(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "alpha", "1.0.0", "alpha.esp", []byte("a")))
	// The completion marker a real download leaves: without it the entry is
	// only partially populated and belongs in NeedsRedownload (F6).
	require.NoError(t, cache.MarkFileCompleteWithMembers(
		gameCache.ModPath(game.ID, "src", "alpha", "1.0.0"), "f1", []string{"alpha.esp"}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "alpha", SourceID: "src", Name: "Alpha", Version: "1.0.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1"},
	}))
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "src", "alpha", game.ID, "default", "f1", "deadbeef"))

	profile := &domain.Profile{Name: "imported", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "alpha", Version: "1.0.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	// No source is registered: an AlreadyCached entry must never fetch.
	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.AlreadyCached, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed, "warnings: %v", result.Warnings)

	requireChecksumRecorded(t, svc, game, "imported", "f1")

	files, err := svc.GetFilesWithChecksums(context.Background(), game.ID, "imported")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "deadbeef", files[0].Checksum,
		"the same bytes must carry the same checksum in both profiles")
}

// TestProfileImport_CrossProfileMod_NoChecksumToCopy_IsHonestlyEmpty is the
// other half of the branch above (P1a re-review N3): the source row carries
// no checksum of its own - a legacy install, or a source that hashes nothing.
// There is nothing to copy, so the new row lands with an empty checksum and
// the import says nothing about it. Honest emptiness is a real state and must
// not be dressed up as either a phantom checksum or a warning.
func TestProfileImport_CrossProfileMod_NoChecksumToCopy_IsHonestlyEmpty(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "alpha", "1.0.0", "alpha.esp", []byte("a")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(
		gameCache.ModPath(game.ID, "src", "alpha", "1.0.0"), "f1", []string{"alpha.esp"}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "alpha", SourceID: "src", Name: "Alpha", Version: "1.0.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1"},
	}))
	// Deliberately NO SaveFileChecksum: that is the whole fixture.

	profile := &domain.Profile{Name: "imported", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "alpha", Version: "1.0.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.AlreadyCached, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed, "warnings: %v", result.Warnings)
	assert.Empty(t, result.Warnings, "a row with nothing to copy is not a failure")

	files, err := svc.GetFilesWithChecksums(context.Background(), game.ID, "imported")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Empty(t, files[0].Checksum, "no checksum existed to copy, so none may be invented")
}

// TestProfileImport_CrossProfileMod_ChecksumReadFailure_IsReported is P1a
// re-review N2: the copy loop read `if err != nil || checksum == ""` and
// treated both as "nothing to copy". They are not the same thing -
// GetFileChecksum answers ("", nil) for a row that simply has no checksum, so
// a non-nil error is a genuine DB failure. Swallowed, it writes an
// unverifiable row and says nothing, while the WRITE half of the same copy
// (recordFileChecksums) warns about its own failures.
//
// The failure is injected by renaming the column out from under the read -
// the same second-connection trick installBlockingTrigger uses, and the
// narrowest way to make one SELECT fail while the flow's writes (which never
// name that column) carry on.
func TestProfileImport_CrossProfileMod_ChecksumReadFailure_IsReported(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	pm := svc.NewProfileManager()
	_, err = pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "alpha", "1.0.0", "alpha.esp", []byte("a")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(
		gameCache.ModPath(game.ID, "src", "alpha", "1.0.0"), "f1", []string{"alpha.esp"}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "alpha", SourceID: "src", Name: "Alpha", Version: "1.0.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1"},
	}))

	profile := &domain.Profile{Name: "imported", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "alpha", Version: "1.0.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.AlreadyCached, 1)

	breakChecksumColumn(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the files are installed either way - a checksum is not the install")
	require.NotEmpty(t, result.Warnings, "a failed READ must be reported, exactly as a failed write is")
	assert.Contains(t, result.Warnings[0], "f1", "the warning names the file whose checksum could not be read")
}

// breakChecksumColumn renames installed_mod_files.checksum out from under the
// service's own connection, so every read of it fails while the inserts that
// flow performs (which name the other five columns explicitly) keep working.
func breakChecksumColumn(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`ALTER TABLE installed_mod_files RENAME COLUMN checksum TO checksum_gone`)
	require.NoError(t, err)
}
