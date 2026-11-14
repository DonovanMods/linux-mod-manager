package core_test

// #639 (coordinator ruling): what a refused archive import may discard is
// what the ingest itself CREATED, at the keys it wrote - never a plan's
// prediction of the key, which an answered installer, a stale recording
// or an enrichment rename can each make wrong. Another profile's entry
// the ingest merely wrote over, or renamed beside, always survives.

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

// importRefused runs `lmm import <archive>` into profile and requires the
// refusal its conflicts cause: the conflict refusal, or - when the plan's
// listing could not see them (an installer's archive members are not the
// tree it installs) - the stale-plan one. Both discard what the run
// created.
func importRefused(t *testing.T, svc *core.Service, game *domain.Game, profile, archive string, opts core.ImportArchiveOptions) *core.ImportArchiveResult {
	t.Helper()
	plan, err := svc.PlanImportArchive(context.Background(), game, profile, archive, opts)
	require.NoError(t, err)
	res, err := svc.ApplyImportArchive(context.Background(), game, profile, plan, opts, nil)
	var ce *core.ConflictError
	require.True(t, errors.As(err, &ce) || errors.Is(err, core.ErrStalePlan), "err=%v", err)
	require.NotNil(t, res)
	return res
}

// A FOMOD archive imported with this call's answers resolves to the very
// variant another profile installed from the same answers. The plan could
// not have named it (answers carry no installer config hash); the ingest
// writes over it, so it did not create it, and the refusal keeps it.
func TestFileSwap_RefusedImportWithAnswersKeepsAnotherProfilesVariant(t *testing.T) {
	f := newFomodFixture(t)
	ctx := context.Background()
	f.serve(t, "mod1", "1.0", "4K", "v1")
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "other"))
	plan, err := f.svc.PlanInstall(ctx, f.game, "other", "src", "mod1", false)
	require.NoError(t, err)
	_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{"src:mod1": answer("4K")}}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "other")
	require.NoError(t, err)
	require.NotEmpty(t, row.Variant)
	k := cache.Key{GameID: "g1", SourceID: "src", ModID: "mod1", Version: "1.0", Variant: row.Variant}

	_, err = pm.Create(ctx, "g1", "default")
	require.NoError(t, err)
	rrSwitchTo(t, f.svc, f.game, "default")
	// default deploys another mod over the installer's texture.
	seedInstalledMod(t, f.svc, f.game, "local", "x", "1.0", true, map[string][]byte{"textures/t.dds": []byte("x")})
	require.NoError(t, f.svc.GetInstallerForTest(f.game).Install(ctx, f.game, &domain.Mod{ID: "x", SourceID: "local", Version: "1.0", GameID: "g1"}, "default"))

	// Named as the source's file, so it is that file (#644: an archive no
	// source file is would be a tree of its own, never other's entry).
	f.game.SourceIDs = map[string]string{"src": "g1"}
	archive := filepath.Join(t.TempDir(), "mod1.zip")
	require.NoError(t, os.WriteFile(archive, fomodZip(t, "4K", "v1"), 0o644))
	opts := core.ImportArchiveOptions{SourceID: "src", ModID: "mod1", Fomod: map[string]domain.FomodChoices{"src:mod1": answer("4K")}}
	res := importRefused(t, f.svc, f.game, "default", archive, opts)
	require.NotNil(t, res.Mod)
	require.Equal(t, k.Variant, res.Mod.Variant, "precondition: the ingest wrote other's very entry")
	require.Equal(t, "1.0", res.Mod.Version)

	assert.True(t, f.svc.GetGameCache(f.game).Exists(k), "other's installer entry survives default's refused import")
	// #644: the ingest staged its tree and the refusal committed none of
	// it, so other's files are exactly as other installed them.
	files, err := f.svc.GetGameCache(f.game).ListFiles(k)
	require.NoError(t, err)
	assert.Contains(t, files, "textures/t.dds")
}

// The importing profile's ref records installer choices, but the archive
// is no installer: the ingest writes the plain entry - another profile's -
// where the plan's recorded choices would have predicted a variant.
func TestFileSwap_RefusedImportOfAPlainArchiveKeepsAnotherProfilesEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	pm := f.svc.NewProfileManager()
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	plain := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
	require.NoError(t, gc.Store(plain, "Auctionator/Auctionator-new.lua", []byte("new")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(plain), "200", []string{"Auctionator/Auctionator-new.lua"}))
	_, err = pm.Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1"}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"200"},
	}))
	// default records installer choices for 42 (a stale recording) and
	// deploys another mod over the archive's file.
	require.NoError(t, pm.UpsertMod(ctx, "g1", "default", domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: []string{"200"},
		Fomod: domain.FomodChoices{ConfigSHA256: "0123", Steps: answer("4K").Steps}}))
	seedUpdatableMod(t, f.svc, f.game, "local", "x", "X", "1.0", nil, map[string][]byte{"Auctionator/Auctionator-new.lua": []byte("x")})

	res := importRefused(t, f.svc, f.game, "default", f.archive(t, "Auctionator-2.0.zip"), core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"})
	require.NotNil(t, res.Mod)
	require.Empty(t, res.Mod.Variant, "precondition: the ingest wrote the plain entry")
	require.False(t, res.Renamed)

	assert.True(t, gc.Exists(plain), "other's entry survives default's refused import")
	assert.True(t, gc.HasFileIDs(plain, []string{"200"}))
}

// An archive ingested at version "unknown" and moved to the version the
// source names: a refusal leaves nothing at either identity, and the
// entry another profile has at the destination as it was (#644: the tree
// is staged, and a refusal commits none of it).
func TestFileSwap_RefusedImportRemovesOnlyWhatItsRenameCreated(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "destination free", true: "destination another profile's"}[occupied], func(t *testing.T) {
			f := newFromFileFixture(t)
			ctx := context.Background()
			gc := f.svc.GetGameCache(f.game)
			_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			dest := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
			if occupied {
				require.NoError(t, gc.Store(dest, "Auctionator/Classic.lua", []byte("classic")))
				require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(dest), "201", []string{"Auctionator/Classic.lua"}))
			}
			seedUpdatableMod(t, f.svc, f.game, "local", "x", "X", "1.0", nil, map[string][]byte{"Auctionator/Auctionator-new.lua": []byte("x")})

			res := importRefused(t, f.svc, f.game, "default", f.archive(t, "Auctionator.zip"), core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"})
			assert.False(t, gc.Exists(cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "unknown"}), "the ingest's own entry goes")
			if occupied {
				assert.False(t, res.Renamed, "the rename never lands on another entry")
				assert.True(t, gc.HasFileIDs(dest, []string{"201"}), "and the entry it found survives")
				return
			}
			// #644: a refusal commits nothing - neither the ingest's
			// identity nor the destination is ever written.
			assert.False(t, res.Renamed)
			assert.False(t, gc.Exists(dest), "and the destination is never written")
		})
	}
}

// Update-from-file follows the same rule: a failure after the ingest wrote
// over another profile's entry for the very file (#639: the entry its set
// is) leaves that entry alone - the ingest did not create it.
func TestFileSwap_FailedUpdateFromFileKeepsAnEntryItDidNotCreate(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	plain := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
	require.NoError(t, gc.Store(plain, "Auctionator/Auctionator-new.lua", []byte("new")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(plain), "200", []string{"Auctionator/Auctionator-new.lua"}))
	_, err := f.svc.NewProfileManager().Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1"}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"200"},
	}))
	f.game.Hooks.Install.BeforeEach = createTestScript(t, t.TempDir(), "fail.sh", "#!/bin/sh\nexit 1\n")
	require.NoError(t, f.svc.SaveGame(ctx, f.game))

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.ErrorContains(t, err, "install.before_each hook failed")

	assert.Equal(t, "1.0", f.row(t).Version)
	assert.True(t, gc.Exists(plain), "other's entry survives default's failed update")
}
