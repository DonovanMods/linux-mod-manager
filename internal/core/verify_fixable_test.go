package core_test

// The Fixable disclosure (#332): every finding a verify run emits reports
// whether a `verify --fix` run would ATTEMPT a repair for it, computed from
// the same decision points the repairs themselves are gated on. These tests
// walk the whole table in VerifyFinding.Fixable's doc comment.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixableByStatus indexes a run's findings by status, for a table that
// cares which STATUS is fixable rather than which row came back first.
func fixableByStatus(t *testing.T, findings []core.VerifyFinding) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(findings))
	for _, f := range findings {
		out[f.Status] = f.Fixable
	}
	return out
}

// newVersionPassFixture seeds four source-backed mods whose version pass
// produces one of each interesting row: an unlocked mismatch (fixable), a
// LOCKED mismatch (refused by #97), an unverifiable mod (no matching file
// id upstream) and an unreachable one (the source errors).
func newVersionPassFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ctx := context.Background()

	svc.RegisterSource(&scriptedVersionSource{
		mockSource: newMockSource("vsrc"),
		filesByModID: map[string][]domain.DownloadableFile{
			"mismatch-mod":        {{ID: "f1", Version: "2.0", IsPrimary: true}},
			"locked-mismatch-mod": {{ID: "f1", Version: "2.0", IsPrimary: true}},
			"unverifiable-mod":    {{ID: "other-file", Version: "1.0", IsPrimary: true}},
		},
		errByModID: map[string]error{"unreachable-mod": errors.New("boom")},
	})

	for _, modID := range []string{"mismatch-mod", "locked-mismatch-mod", "unverifiable-mod", "unreachable-mod"} {
		seedVerifyMod(t, svc, game, "vsrc", modID, modID, "1.0", []string{"f1"}, true)
		require.NoError(t, svc.SaveFileChecksum(ctx, "vsrc", modID, game.ID, "default", "f1", "cs-"+modID))
	}

	require.NoError(t, pm.UpsertMod(ctx, game.ID, "default", domain.ModReference{
		SourceID: "vsrc", ModID: "locked-mismatch-mod", Version: "1.0", FileIDs: []string{"f1"},
	}))
	require.NoError(t, pm.SetModLock(ctx, game.ID, "default", "vsrc", "locked-mismatch-mod", "1.0"))

	return svc, game
}

// TestVerifyFixable_PerFileStatuses covers the three per-file repairs and
// the two rows that are never fixable, in one local-tier run:
//
//   - missing + no_checksum on a SOURCE-backed mod -> fixable
//   - the same two on a LOCALLY IMPORTED mod       -> not (nothing to
//     redownload from)
//   - ok / skipped                                 -> never
func TestVerifyFixable_PerFileStatuses(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ctx := context.Background()

	// A source-backed mod whose cache entry is gone -> missing, fixable.
	seedVerifyMod(t, svc, game, "src", "gone", "Gone", "1.0", []string{"gone-file"}, false)
	require.NoError(t, svc.SaveFileChecksum(ctx, "src", "gone", game.ID, "default", "gone-file", "sum"))

	// A LOCAL mod whose cache entry is gone -> missing, NOT fixable.
	seedVerifyMod(t, svc, game, domain.SourceLocal, "local-gone", "Local Gone", "1.0", []string{"local-gone-file"}, false)
	require.NoError(t, svc.SaveFileChecksum(ctx, domain.SourceLocal, "local-gone", game.ID, "default", "local-gone-file", "sum"))

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	byMod := map[string]core.VerifyFinding{}
	for _, f := range result.Findings {
		byMod[f.ModID] = f
	}
	require.Contains(t, byMod, "gone")
	assert.Equal(t, "missing", byMod["gone"].Status)
	assert.True(t, byMod["gone"].Fixable, "--fix redownloads a source-backed mod's missing cache entry")

	require.Contains(t, byMod, "local-gone")
	assert.Equal(t, "missing", byMod["local-gone"].Status)
	assert.False(t, byMod["local-gone"].Fixable, "a locally imported mod has no source to redownload from")
}

// TestVerifyFixable_NoChecksumAndOK: an empty checksum on a source-backed
// mod is fixable (redownload-to-populate), an "ok" row never is.
func TestVerifyFixable_NoChecksumAndOK(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ctx := context.Background()

	seedVerifyMod(t, svc, game, "src", "nosum", "No Sum", "1.0", []string{"nosum-file"}, true)
	require.NoError(t, svc.SaveFileChecksum(ctx, "src", "nosum", game.ID, "default", "nosum-file", ""))
	seedVerifyMod(t, svc, game, "src", "fine", "Fine", "1.0", []string{"fine-file"}, true)
	require.NoError(t, svc.SaveFileChecksum(ctx, "src", "fine", game.ID, "default", "fine-file", "sum"))

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	got := fixableByStatus(t, result.Findings)
	assert.True(t, got["no_checksum"], "--fix redownloads to populate a missing checksum")
	assert.False(t, got["ok"], "a healthy row has nothing to repair")
}

// TestVerifyFixable_StaleDeployment: convergence removes it under --fix,
// unconditionally, so a plain run always reports it as fixable.
func TestVerifyFixable_StaleDeployment(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	strayDanglingSymlink(t, svc, game, "stray.pak")

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "stale_deployment", result.Findings[0].Status)
	assert.True(t, result.Findings[0].Fixable)
}

// TestVerifyFixable_VersionStatuses is the version pass's half of the
// table: a mismatch on an unlocked, source-backed mod is fixable; the same
// mismatch on a LOCKED ref is not (its Version is the lock's target, which
// --fix refuses to rewrite); version_unverifiable never is, because there
// is nothing to repair it with.
func TestVerifyFixable_VersionStatuses(t *testing.T) {
	svc, game := newVersionPassFixture(t)

	result, err := svc.VerifyForTest(context.Background(), game, "default",
		core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	byMod := map[string]core.VerifyFinding{}
	for _, f := range result.Findings {
		if f.FileID == "" {
			byMod[f.ModID] = f
		}
	}

	require.Contains(t, byMod, "mismatch-mod")
	assert.Equal(t, "version_mismatch", byMod["mismatch-mod"].Status)
	assert.True(t, byMod["mismatch-mod"].Fixable)

	require.Contains(t, byMod, "locked-mismatch-mod")
	assert.Equal(t, "version_mismatch", byMod["locked-mismatch-mod"].Status)
	assert.False(t, byMod["locked-mismatch-mod"].Fixable,
		"#97: --fix refuses to rewrite a locked ref's version, so it is not fixable")

	require.Contains(t, byMod, "unverifiable-mod")
	assert.Equal(t, "version_unverifiable", byMod["unverifiable-mod"].Status)
	assert.False(t, byMod["unverifiable-mod"].Fixable, "version_unverifiable is never fixable")

	require.Contains(t, byMod, "unreachable-mod")
	assert.Equal(t, "skipped", byMod["unreachable-mod"].Status)
	assert.False(t, byMod["unreachable-mod"].Fixable, "a skipped check has nothing to repair")
}

// TestVerifyFixable_FixRunReportsNothingFixable is the other half of the
// contract: a row produced BY a --fix run has already had its repair
// attempted, refused or completed, so it never claims to still be fixable.
// The locked mismatch is the sharpest case - it is the one row a --fix run
// leaves reading exactly as it did before, minus the claim.
func TestVerifyFixable_FixRunReportsNothingFixable(t *testing.T) {
	svc, game := newVersionPassFixture(t)

	result, err := svc.VerifyForTest(context.Background(), game, "default",
		core.VerifyOptions{Tier: core.VerifyFull, Fix: true}, nil)
	require.NoError(t, err)

	for _, f := range result.Findings {
		assert.False(t, f.Fixable,
			"a --fix run's own row must not claim a repair is still pending: %+v", f)
	}
}

// TestVerifyFixable_CompileGameStatuses is M7's completeness fix: the CLI
// verify goldens already pinned stale_compile/needs_reingest/
// file_count_mismatch/conversion_failed correctly (unit6a-review.md
// confirmed each by hand), but this package's own table tests never
// exercised them - this test closes that gap directly against the table in
// VerifyFinding.Fixable's doc comment, reusing the DeployCompile fixture
// TestVerify_CompileGameStatuses (verify_test.go) built for the same four
// statuses, plus the file_count_mismatch shape from
// TestVerify_FullOrder_Integration.
func TestVerifyFixable_CompileGameStatuses(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	src := &pakConversionOutcomeSource{
		fakeCompilerSource: &fakeCompilerSource{},
		failRefs:           map[string]string{"fake-compiler:badpak": "irreconcilable pak layout"},
	}
	registerCompileSource(svc, src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy, ConvertPaks: true,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)

	// file_count_mismatch: a cache version directory that still exists but
	// holds none of its recorded files (TestVerify_FullOrder_Integration's
	// own fixture).
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "fc-mod", "1.0", "fc-file", []byte("x")))
	require.NoError(t, os.Remove(gameCache.GetFilePath(game.ID, "fake-compiler", "fc-mod", "1.0", "fc-file")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "fc-mod", SourceID: "fake-compiler", Name: "FC Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"fc-file"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "fc-mod", Version: "1.0", FileIDs: []string{"fc-file"}}))
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "fc-mod", game.ID, "default", "fc-file", "checksum-fc"))

	// goodmod converts cleanly; badpak's conversion is scripted to fail -
	// the conversion_failed fixture.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "goodmod", "1.0", "exmodz-file", []byte("good-bytes"))
	seedEnabledPakMod(t, svc, game, "fake-compiler", "badpak", "1.0", "pak", []byte("bad-bytes"))

	// legacypak (source-backed) and locallegacy (SourceLocal): both
	// pre-#221 pak cache entries - a deployable member on disk, no
	// retained source - so both produce needs_reingest, but only the
	// source-backed one is fixable.
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "legacypak", "1.0", "legacypak.pak", []byte("legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "legacypak", SourceID: "fake-compiler", Name: "Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "legacypak", Version: "1.0", FileIDs: []string{"pak"}}))

	require.NoError(t, gameCache.Store(game.ID, domain.SourceLocal, "locallegacy", "1.0", "locallegacy.pak", []byte("local-legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "locallegacy", SourceID: domain.SourceLocal, Name: "Local Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: domain.SourceLocal, ModID: "locallegacy", Version: "1.0", FileIDs: []string{"pak"}}))

	// First (only) sync: stamps MergedPakOutcomes with badpak's conversion
	// failure and establishes an up-to-date fingerprint.
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	// Go stale: enable a third exmodz mod WITHOUT syncing again - the
	// stale_compile fixture.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolfmod", "1.0", "exmodz-file", []byte("wolf-bytes"))

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	byModID := map[string]core.VerifyFinding{}
	for _, f := range result.Findings {
		if f.FileID == "" {
			byModID[f.ModID] = f
		}
	}

	require.Contains(t, byModID, "fc-mod")
	assert.Equal(t, "file_count_mismatch", byModID["fc-mod"].Status)
	assert.False(t, byModID["fc-mod"].Fixable, "file_count_mismatch is never fixable")

	require.Contains(t, byModID, "merged-pak")
	assert.Equal(t, "stale_compile", byModID["merged-pak"].Status)
	assert.True(t, byModID["merged-pak"].Fixable, "--fix's merged-pak resync regenerates it")

	require.Contains(t, byModID, "badpak")
	assert.Equal(t, "conversion_failed", byModID["badpak"].Status)
	assert.False(t, byModID["badpak"].Fixable, "conversion_failed is never fixable")

	var reingest []core.VerifyFinding
	for _, f := range result.Findings {
		if f.Status == "needs_reingest" {
			reingest = append(reingest, f)
		}
	}
	require.Len(t, reingest, 2)
	byReingestModID := map[string]core.VerifyFinding{reingest[0].ModID: reingest[0], reingest[1].ModID: reingest[1]}

	require.Contains(t, byReingestModID, "legacypak")
	assert.True(t, byReingestModID["legacypak"].Fixable, "a source-backed needs_reingest row can be redownloaded")

	require.Contains(t, byReingestModID, "locallegacy")
	assert.False(t, byReingestModID["locallegacy"].Fixable, "a SourceLocal mod has no source to redownload from")
}
