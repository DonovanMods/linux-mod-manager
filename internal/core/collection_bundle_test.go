package core_test

// #623: bundled files in a `.lmmc` container - the import that installs
// them through the lmm-collection source, the rebuild from the kept
// container, the code gate, the refusals, and the export that makes one.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundleMember is one member of a test container.
type bundleMember struct {
	name, content string
	mode          fs.FileMode
}

// bundleContainer packs doc and members as a `.lmmc`.
func bundleContainer(t *testing.T, doc string, members ...bundleMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "collection.yaml", Method: zip.Deflate})
	require.NoError(t, err)
	_, err = f.Write([]byte(doc))
	require.NoError(t, err)
	for _, m := range members {
		hdr := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		mode := m.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr.SetMode(mode)
		f, err := w.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = f.Write([]byte(m.content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// bundledDoc is a version-1 document naming one bundled item, patch.
const bundledDoc = `lmm_collection: 1
id: c0ffee
name: coop
game: {name: G}
requires: [bundled]
mods:
  - source_id: lmm-collection
    mod_id: patch
    name: My Patch
`

// bundledFixture is a machine with game g (deploying into its mod path,
// with no profile yet, so the import's profile is the active one).
func bundledFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// patchContainer is bundledDoc with its patch item's two files.
func patchContainer(t *testing.T) []byte {
	return bundleContainer(t, bundledDoc,
		bundleMember{name: "files/patch/patch.esp", content: "PATCH"},
		bundleMember{name: "files/patch/cfg/a.ini", content: "[A]\n"},
	)
}

func importBundled(t *testing.T, svc *core.Service, game *domain.Game, data []byte, opts core.ProfileImportOptions) (*core.ImportPlan, *core.ProfileImportResult, error) {
	t.Helper()
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data, GameID: game.ID})
	require.NoError(t, err)
	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, opts, nil)
	return plan, result, err
}

func TestBundledCollection_ImportInstallsTheItemFromTheContainer(t *testing.T) {
	svc, game := bundledFixture(t)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: patchContainer(t), GameID: game.ID})
	require.NoError(t, err)
	c := plan.Collection
	require.Empty(t, c.Unsupported, "bundled is supported")
	require.Equal(t, []string{"bundled"}, c.Requires)
	require.Len(t, c.Bundled, 1)
	b := c.Bundled[0]
	assert.Equal(t, "patch", b.Item)
	assert.Equal(t, "c0ffee.patch", b.ModID)
	assert.Equal(t, "My Patch", b.Name)
	assert.Equal(t, 2, b.Files)
	assert.Equal(t, int64(9), b.Bytes)
	assert.Len(t, b.Version, 16)
	assert.Empty(t, b.Restricted)
	require.Len(t, c.Items, 1)
	assert.True(t, c.Items[0].Bundled)
	assert.Equal(t, "c0ffee.patch", c.Items[0].ModID)

	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	ref := saved.Mods[0]
	assert.Equal(t, domain.SourceCollection, ref.SourceID)
	assert.Equal(t, "c0ffee.patch", ref.ModID)
	assert.Equal(t, b.Version, ref.Version, "the version is the files' digest")

	got, err := os.ReadFile(filepath.Join(game.ModPath, "patch.esp"))
	require.NoError(t, err)
	assert.Equal(t, "PATCH", string(got), "the bundled files are deployed")
	got, err = os.ReadFile(filepath.Join(game.ModPath, "cfg", "a.ini"))
	require.NoError(t, err)
	assert.Equal(t, "[A]\n", string(got))

	row, err := svc.GetInstalledMod(context.Background(), domain.SourceCollection, "c0ffee.patch", game.ID, "coop")
	require.NoError(t, err)
	assert.Equal(t, "My Patch", row.Name, "the row carries the document's name for the item")
	cached := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version})
	info, err := os.Stat(filepath.Join(cached, "patch.esp"))
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&0o111, "a bundled file is never executable")

	// A re-plan of the same container finds it installed.
	again, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: patchContainer(t), GameID: game.ID})
	require.NoError(t, err)
	require.Len(t, again.Installed, 1)
	assert.Empty(t, again.Missing)
}

// TestBundledCollection_VerifyFixRebuildsFromTheKeptContainer: there is no
// remote, so a lost cache entry is rebuilt from the container the import
// kept.
func TestBundledCollection_VerifyFixRebuildsFromTheKeptContainer(t *testing.T) {
	svc, game := bundledFixture(t)
	_, _, err := importBundled(t, svc, game, patchContainer(t), core.ProfileImportOptions{Install: true})
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(context.Background(), domain.SourceCollection, "c0ffee.patch", game.ID, "coop")
	require.NoError(t, err)
	key := cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version}
	require.NoError(t, svc.GetGameCache(game).Delete(key))
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "patch.esp")))

	_, err = svc.VerifyReport(context.Background(), game, "coop", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assert.True(t, svc.GetGameCache(game).Exists(key), "the cache entry is rebuilt from the kept container")
	got, err := os.ReadFile(svc.GetGameCache(game).GetFilePath(key, "patch.esp"))
	require.NoError(t, err)
	assert.Equal(t, "PATCH", string(got))
}

// TestBundledCollection_SurvivesAnotherProfilesApply: a second profile
// naming the item installs it from the kept container too.
func TestBundledCollection_ProfileApplyInstallsItFromTheKeptContainer(t *testing.T) {
	svc, game := bundledFixture(t)
	_, _, err := importBundled(t, svc, game, patchContainer(t), core.ProfileImportOptions{Install: true})
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(context.Background(), domain.SourceCollection, "c0ffee.patch", game.ID, "coop")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Delete(cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version}))
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "patch.esp")))
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "cfg", "a.ini")))
	require.NoError(t, svc.DeleteInstalledMod(context.Background(), row.SourceID, row.ID, game.ID, "coop"))

	plan, err := svc.PlanProfileApply(context.Background(), game, "coop")
	require.NoError(t, err)
	_, err = svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(game.ModPath, "patch.esp"))
	require.NoError(t, err)
	assert.Equal(t, "PATCH", string(got))
}

func TestBundledCollection_CodeIsHeldBackUnlessAllowed(t *testing.T) {
	data := bundleContainer(t, strings.Replace(bundledDoc, "mod_id: patch", "mod_id: tool", 1),
		bundleMember{name: "files/tool/bin/helper.dll", content: "MZ\x90\x00"},
		bundleMember{name: "files/tool/readme.txt", content: "hi"},
	)
	t.Run("held back", func(t *testing.T) {
		svc, game := bundledFixture(t)
		plan, result, err := importBundled(t, svc, game, data, core.ProfileImportOptions{Install: true})
		require.NoError(t, err)
		require.Len(t, plan.Collection.Bundled, 1)
		require.Equal(t, []core.RestrictedOverride{{Path: "bin/helper.dll", Reason: "code the system runs (.dll)"}}, plan.Collection.Bundled[0].Restricted)
		assert.Equal(t, []string{"tool"}, result.BundledSkipped)
		saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
		require.NoError(t, err)
		assert.Empty(t, saved.Mods, "a held-back item is left out of the profile")
		assert.NoFileExists(t, filepath.Join(game.ModPath, "bin", "helper.dll"))
	})
	t.Run("allowed", func(t *testing.T) {
		svc, game := bundledFixture(t)
		_, result, err := importBundled(t, svc, game, data, core.ProfileImportOptions{Install: true, AllowAnyOverrides: true})
		require.NoError(t, err)
		assert.Empty(t, result.BundledSkipped)
		assert.FileExists(t, filepath.Join(game.ModPath, "bin", "helper.dll"))
	})
	t.Run("a renamed DLL and a loader configuration", func(t *testing.T) {
		svc, game := bundledFixture(t)
		renamed := bundleContainer(t, strings.Replace(bundledDoc, "mod_id: patch", "mod_id: tool", 1),
			bundleMember{name: "files/tool/data.bin", content: "\x7fELF...."},
			bundleMember{name: "files/tool/Doorstop_Config.ini", content: "[General]\n"},
		)
		plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: renamed, GameID: game.ID})
		require.NoError(t, err)
		require.Len(t, plan.Collection.Bundled[0].Restricted, 2)
	})
}

func TestBundledCollection_Refusals(t *testing.T) {
	ok := bundleMember{name: "files/patch/a.txt", content: "a"}
	cases := []struct {
		name string
		data func(t *testing.T) []byte
		want string
	}{
		{"plain YAML", func(*testing.T) []byte { return []byte(bundledDoc) }, "plain YAML document cannot carry files"},
		{"no files for the item", func(t *testing.T) []byte { return bundleContainer(t, bundledDoc) }, "holds no files/patch/"},
		{"version mismatch", func(t *testing.T) []byte {
			return bundleContainer(t, strings.Replace(bundledDoc, "name: My Patch", "name: My Patch\n    version: 0123456789abcdef", 1), ok)
		}, "does not match the version"},
		{"symlink", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/patch/link", content: "/etc/passwd", mode: fs.ModeSymlink | 0o777})
		}, "not a regular file"},
		{"item as a file", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/other", content: "x"})
		}, "is a file"},
		{"bad item name", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/-x/a", content: "x"})
		}, "bundled item's name"},
		{"dot-dot", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/patch/../patch/b", content: "x"})
		}, `".."`},
		{"empty segment", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/patch//b", content: "x"})
		}, "empty"},
		{"backslash", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: `files/patch/a\b`, content: "x"})
		}, "backslash"},
		{"control character", func(t *testing.T) []byte {
			return bundleContainer(t, bundledDoc, ok, bundleMember{name: "files/patch/a\x07", content: "x"})
		}, "control"},
		{"duplicate", func(t *testing.T) []byte { return bundleContainer(t, bundledDoc, ok, ok) }, "more than once"},
		{"collection id with a dot", func(t *testing.T) []byte {
			return bundleContainer(t, strings.Replace(bundledDoc, "id: c0ffee", "id: c0.ffee", 1), ok)
		}, "needs an id"},
		{"item name in the document", func(t *testing.T) []byte {
			return bundleContainer(t, strings.Replace(bundledDoc, "mod_id: patch", "mod_id: ../x", 1), ok)
		}, "cannot name a bundled item"},
		{"FOMOD choices on a bundled item", func(t *testing.T) []byte {
			doc := strings.Replace(bundledDoc, "name: My Patch", "name: My Patch\n    fomod: {config_sha256: "+strings.Repeat("a", 64)+", steps: [{name: S, groups: [{name: G, choices: [{name: C, idx: 0}]}]}]}", 1)
			return bundleContainer(t, doc, ok)
		}, "FOMOD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := bundledFixture(t)
			_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: tc.data(t), GameID: game.ID})
			require.Error(t, err)
			assert.True(t, core.IsInvalidCollection(err), "the caller's input: %v", err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestBundledCollection_AnUnnamedItemIsLeftOutWithAWarning(t *testing.T) {
	svc, game := bundledFixture(t)
	data := bundleContainer(t, bundledDoc,
		bundleMember{name: "files/patch/a.txt", content: "a"},
		bundleMember{name: "files/extra/b.txt", content: "b"},
	)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data, GameID: game.ID})
	require.NoError(t, err)
	require.Len(t, plan.Collection.Bundled, 1)
	assert.Contains(t, strings.Join(plan.Collection.Warnings, "\n"), "files/extra/ is named by no mods: entry")
}

// localModFixture is an author's machine: profile "mine" of game g holds a
// mod imported from a file (local) and one from a source (nexusmods).
func localModFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	ctx := context.Background()
	for _, m := range []struct{ src, id, name string }{{"local", "u-1", "My Tweaks"}, {"nexusmods", "7", "Someone's Mod"}} {
		key := cache.Key{GameID: game.ID, SourceID: m.src, ModID: m.id, Version: "1.0"}
		require.NoError(t, svc.GetGameCache(game).Store(key, "tweaks/a.ini", []byte("[T]\n")))
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: m.id, SourceID: m.src, Name: m.name, Version: "1.0", GameID: game.ID},
			ProfileName: "mine", Enabled: true,
		}))
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "mine", GameID: game.ID,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "7", Version: "1.0"},
			{SourceID: "local", ModID: "u-1", Version: "1.0"},
		},
	}))
	return svc, game
}

func TestBundledCollection_ExportBundlesTheAuthorsOwnFiles(t *testing.T) {
	svc, game := localModFixture(t)
	candidates, err := svc.BundleCandidates(context.Background(), game.ID, "mine")
	require.NoError(t, err)
	require.Equal(t, []core.BundleCandidate{{SourceID: "local", ModID: "u-1", Name: "My Tweaks"}}, candidates)

	export, err := svc.ExportCollection(context.Background(), game.ID, "mine", core.CollectionExportOptions{Bundle: []string{"My Tweaks"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-tweaks"}, export.Bundled)
	assert.Contains(t, strings.Join(export.Notes, "\n"), "share only files you made")

	doc, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	require.Len(t, doc.Items, 2)
	assert.Equal(t, "nexusmods", doc.Items[0].Ref.SourceID, "a source's mod stays a reference")
	bundled := doc.Items[1]
	assert.Equal(t, domain.SourceCollection, bundled.Ref.SourceID)
	assert.Equal(t, "my-tweaks", bundled.Ref.ModID)
	assert.Equal(t, "My Tweaks", bundled.Name)
	assert.Len(t, bundled.Ref.Version, 16)
	assert.Contains(t, doc.Requires, "bundled")

	data, err := export.Container()
	require.NoError(t, err)

	// The recipient installs the bundled item and keeps the source's mod
	// as a reference.
	other, otherGame := bundledFixture(t)
	plan, err := other.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data, GameID: otherGame.ID})
	require.NoError(t, err)
	require.Len(t, plan.Collection.Bundled, 1)
	assert.Equal(t, bundled.Ref.Version, plan.Collection.Bundled[0].Version, "exporter and importer agree on the digest")
	_, err = other.ApplyCollectionImport(context.Background(), otherGame, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)

	// Re-exporting the recipient's profile carries the bundled item again,
	// unnamed, at the same version - from the kept container, though
	// nothing was installed (--no-install).
	again, err := other.ExportCollection(context.Background(), otherGame.ID, "mine", core.CollectionExportOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-tweaks"}, again.Bundled)
	redoc, err := config.ParseCollection(again.Document)
	require.NoError(t, err)
	assert.Equal(t, "My Tweaks", redoc.Items[1].Name)
	assert.Equal(t, bundled.Ref.Version, redoc.Items[1].Ref.Version)
	recontainer, err := again.Container()
	require.NoError(t, err)
	assert.Equal(t, bundled.Ref.Version, bundledVersionOf(t, recontainer))
}

func TestBundledCollection_ExportRefusesASourcesMod(t *testing.T) {
	svc, game := localModFixture(t)
	for _, name := range []string{"nexusmods:7", "7", "Someone's Mod"} {
		_, err := svc.ExportCollection(context.Background(), game.ID, "mine", core.CollectionExportOptions{Bundle: []string{name}})
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, core.ErrBundleSourceMod), "%s: %v", name, err)
		assert.True(t, core.IsBundleRefused(err))
	}
	_, err := svc.ExportCollection(context.Background(), game.ID, "mine", core.CollectionExportOptions{Bundle: []string{"nope"}})
	assert.True(t, errors.Is(err, core.ErrBundleModNotFound))
}

func TestBundledCollection_ReExportCarriesABundledItemAgain(t *testing.T) {
	svc, game := bundledFixture(t)
	_, _, err := importBundled(t, svc, game, patchContainer(t), core.ProfileImportOptions{Install: true})
	require.NoError(t, err)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"patch"}, export.Bundled, "a bundled item is carried without being named")
	data, err := export.Container()
	require.NoError(t, err)
	assert.Equal(t, patchContainerDigest(t, svc, game), bundledVersionOf(t, data))
}

func patchContainerDigest(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	row, err := svc.GetInstalledMod(context.Background(), domain.SourceCollection, "c0ffee.patch", game.ID, "coop")
	require.NoError(t, err)
	return row.Version
}

func bundledVersionOf(t *testing.T, container []byte) string {
	t.Helper()
	svc, game := bundledFixture(t)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: container, GameID: game.ID})
	require.NoError(t, err)
	require.Len(t, plan.Collection.Bundled, 1)
	return plan.Collection.Bundled[0].Version
}
