package core_test

// #644: an archive ingest never changes another profile's cache entry.
//
//   - An archive linked to a source (--source/--id) that is no file the
//     source lists records no file set, so #639's file-set entry cannot keep
//     it apart: it is ingested into a variant of its own, keyed by the
//     archive's sha256, never the plain entry a source download builds.
//   - An ingest stages its tree and commits it to the cache only once every
//     refusal has passed, so a refused import or update leaves an entry it
//     would have written over exactly as it was.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherProfilesPlainEntry gives a second profile, "other", 42@2.0 built
// from the source's file 200 - the plain entry a download writes - and
// returns its key.
func otherProfilesPlainEntry(t *testing.T, f *fromFileFixture) cache.Key {
	t.Helper()
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	plain := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
	require.NoError(t, gc.Store(plain, "Auctionator/Auctionator-new.lua", []byte("source build")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(plain), "200", []string{"Auctionator/Auctionator-new.lua"}))
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(ctx, "g1", "other", domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: []string{"200"}}))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1"}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"200"},
	}))
	return plain
}

// assertEntryUntouched requires k to still hold the source build of file
// 200, with its marker.
func assertEntryUntouched(t *testing.T, f *fromFileFixture, k cache.Key) {
	t.Helper()
	gc := f.svc.GetGameCache(f.game)
	data, err := os.ReadFile(filepath.Join(gc.ModPath(k), "Auctionator", "Auctionator-new.lua"))
	require.NoError(t, err, "other's entry must survive")
	assert.Equal(t, "source build", string(data), "other's tree must not change")
	assert.True(t, gc.HasFileIDs(k, []string{"200"}), "and its marker must survive")
}

// customArchive is an archive at 2.0 that is none of the files the source
// lists: two files are at 2.0, so no sole file names it either.
func customArchive(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Auctionator-custom-2.0.zip")
	createImportTestZip(t, path, map[string]string{"Auctionator/Auctionator-new.lua": content})
	return path
}

func readDeployed(t *testing.T, f *fromFileFixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.gameDir, "Auctionator", "Auctionator-new.lua"))
	require.NoError(t, err)
	return string(data)
}

// Case 1, `lmm import <archive> --source msrc --id 42`: the archive matches
// no listed file, so it records no file set - and before #644 it was
// ingested into the plain 2.0 entry, other's, which then deployed the
// custom tree while other's plan still reported no changes.
func TestUnmatchedArchive_ImportKeepsAnotherProfilesPlainEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	plain := otherProfilesPlainEntry(t, f)
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)

	archive := customArchive(t, "custom build")
	opts := core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"}
	plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, opts)
	require.NoError(t, err)
	require.Equal(t, "2.0", plan.Mod.Version)
	assert.False(t, plan.EntryPreExists, "the archive's own entry does not exist yet; other's plain one is not it")
	res, err := f.svc.ApplyImportArchive(ctx, f.game, "default", plan, opts, nil)
	require.NoError(t, err)
	assert.Empty(t, res.FileIDs, "precondition: the archive matched no source file")

	assertEntryUntouched(t, f, plain)
	row := f.row(t)
	require.NotEmpty(t, row.Variant, "the archive's tree is an entry of its own")
	own := plain
	own.Variant = row.Variant
	data, err := os.ReadFile(filepath.Join(gc.ModPath(own), "Auctionator", "Auctionator-new.lua"))
	require.NoError(t, err)
	assert.Equal(t, "custom build", string(data))
	assert.Equal(t, "custom build", readDeployed(t, f))

	// The same archive names the same entry again.
	again, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, opts)
	require.NoError(t, err)
	assert.True(t, again.EntryPreExists)
	// Another archive at the same version is another entry.
	other := customArchive(t, "another build")
	res2, err := f.svc.ImportArchive(ctx, f.game, "default", other, core.ImportArchiveOptions{SourceID: "msrc", ModID: "42", AcceptConflicts: true}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, row.Variant, res2.Mod.Variant)
	assert.True(t, gc.Exists(own), "the first archive's entry is not written over either")
	assertEntryUntouched(t, f, plain)

	// The row fits its profile: nothing to converge, and verify is clean.
	ap, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	assert.True(t, ap.NoChanges, "plan: %+v", ap)
	vr, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, vr.Result.Issues, "findings: %+v", vr.Result.Findings)
}

// Case 1 through `lmm update 42 --from-file`: an archive that is no listed
// file updates into its own entry, and the rollback returns to the
// version it replaced, with other's entry untouched throughout.
func TestUnmatchedArchive_UpdateFromFileKeepsAnotherProfilesPlainEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	plain := otherProfilesPlainEntry(t, f)

	plan := f.plan(t, customArchive(t, "custom build"), core.UpdateFromArchiveOptions{})
	require.Equal(t, "2.0", plan.ToVersion)
	require.Empty(t, plan.FileIDs, "precondition: the archive matched no source file")
	_, err := f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{AcceptMismatch: true}, nil)
	require.NoError(t, err)

	assertEntryUntouched(t, f, plain)
	row := f.row(t)
	require.Equal(t, "2.0", row.Version)
	require.NotEmpty(t, row.Variant)
	assert.Equal(t, "custom build", readDeployed(t, f))
	vr, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, vr.Result.Issues, "findings: %+v", vr.Result.Findings)

	rb, err := f.svc.PlanRollback(ctx, f.game, "default", "msrc", "42")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, rb, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	row = f.row(t)
	assert.Equal(t, "1.0", row.Version)
	assert.Equal(t, "2.0", row.PreviousVersion)
	assert.True(t, gc.Exists(cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0", Variant: row.PreviousVariant}),
		"the archive's entry stays for a roll forward")
	assertEntryUntouched(t, f, plain)
}

// Case 2, import: the archive is file 200, whose entry is other's plain
// one, but a different build of it. The import is refused (default
// deploys another mod over its file), and other's tree, marker included,
// is exactly as it was: the ingest commits nothing before the gates pass.
func TestRefusedImport_LeavesAnotherProfilesEntryAsItWas(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	plain := otherProfilesPlainEntry(t, f)
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	seedUpdatableMod(t, f.svc, f.game, "local", "x", "X", "1.0", nil, map[string][]byte{"Auctionator/Auctionator-new.lua": []byte("x")})

	res := importRefused(t, f.svc, f.game, "default", f.archive(t, "Auctionator-2.0.zip"), core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"})
	require.NotNil(t, res.Mod)
	require.Empty(t, res.Mod.Variant, "precondition: the ingest resolved other's plain entry")
	assertEntryUntouched(t, f, plain)
}

// Case 2, update from a file: the archive is file 200 again, and the
// update fails at its install.before_each hook - before the Replace, so
// before anything may change the cache.
func TestFailedUpdateFromFile_LeavesAnotherProfilesEntryAsItWas(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	plain := otherProfilesPlainEntry(t, f)
	f.game.Hooks.Install.BeforeEach = createTestScript(t, t.TempDir(), "fail.sh", "#!/bin/sh\nexit 1\n")
	require.NoError(t, f.svc.SaveGame(ctx, f.game))

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err := f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.ErrorContains(t, err, "install.before_each hook failed")
	assert.Equal(t, "1.0", f.row(t).Version)
	assertEntryUntouched(t, f, plain)
}

// A refusal after the conflict gate - a failing install.before_all hook -
// leaves no cache entry behind either: before #644 the entry the ingest
// created outlived it, a copy of the archive nothing referenced.
func TestRefusedImport_HookFailureLeavesNoCacheEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	f.game.Hooks.Install.BeforeAll = createTestScript(t, t.TempDir(), "fail.sh", "#!/bin/sh\nexit 1\n")
	require.NoError(t, f.svc.SaveGame(ctx, f.game))

	_, err = f.svc.ImportArchive(ctx, f.game, "default", f.archive(t, "Auctionator-2.0.zip"), core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"}, nil)
	require.ErrorContains(t, err, "install.before_all hook failed")
	assert.False(t, gc.Exists(cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}))
	_, err = f.svc.GetInstalledMod(ctx, "msrc", "42", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound), "err=%v", err)
}

// A deploy that finds an archive tree's entry gone does not download the
// source's file into its place: no download is that tree. The plan and
// the deploy both say so (ReasonArchiveTree).
func TestUnmatchedArchive_MissingEntryIsNotRedownloaded(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	_, err = f.svc.ImportArchive(ctx, f.game, "default", customArchive(t, "custom build"), core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"}, nil)
	require.NoError(t, err)
	row := f.row(t)
	own := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0", Variant: row.Variant}
	require.NoError(t, gc.Delete(own))

	plan, err := f.svc.PlanDeploy(ctx, f.game, "default", core.DeployOptions{})
	require.NoError(t, err)
	var found bool
	for _, m := range plan.Mods {
		if m.Ref.ModID == "42" {
			found = true
			assert.Equal(t, core.ReasonArchiveTree, m.Skipped)
			assert.False(t, m.Redownload)
		}
	}
	require.True(t, found)

	res, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, core.ReasonArchiveTree, res.Skipped[0].Reason)
	assert.False(t, gc.Exists(own))
	assert.False(t, gc.Exists(cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}))
}
