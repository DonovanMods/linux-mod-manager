package core_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

// updateCollectionGoldens re-records the collection export goldens:
//
//	go test ./internal/core/ -run TestExportCollection -update-collection-goldens
var updateCollectionGoldens = flag.Bool("update-collection-goldens", false, "rewrite internal/core/testdata/collection/*.golden")

func checkCollectionGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "collection", name+".golden")
	if *updateCollectionGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-collection-goldens", path)
	require.Equal(t, string(want), string(got))
}

// exportFixture is a game with a profile carrying everything an export
// writes or must leave out: installed mods with names, a curator's origin,
// an external item, hooks, an override, an explicit link method and the
// default flag.
func exportFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{
		ID: "cp", Name: "Cyberpunk 2077", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"nexusmods": "cyberpunk2077", "steamworkshop": "1091500"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedInstalledModUnderProfile(t, svc, game, "coop", "nexusmods", "1", "Much Better Ammo", "1.8.0", true, nil)
	profile := &domain.Profile{
		Name: "coop", GameID: game.ID, IsDefault: true,
		LinkMethod: domain.LinkHardlink, LinkMethodExplicit: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "1", Version: "1.8.0", Locked: true},
			{SourceID: "nexusmods", ModID: "2", Version: "2.0", Disabled: true, Category: "UI"},
		},
		Hooks:         domain.GameHooks{Install: domain.HookConfig{AfterAll: "/usr/bin/true"}},
		HooksExplicit: domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}},
		Overrides:     map[string][]byte{"engine/config/user.ini": []byte("[A]\nb=1\n")},
		Collection: &domain.CollectionInfo{
			ID: "2f8c7a3e-0000-4000-8000-000000000001", Version: 3, Title: "Welcome to Night City",
			Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "iszwwe", Revision: 484, Author: "curator"},
		},
		External: []domain.CollectionExternalItem{{Name: "CET", URL: "https://example.invalid/cet", Instructions: "Install by hand."}},
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	return svc, game
}

func TestExportCollection_Document(t *testing.T) {
	svc, game := exportFixture(t)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	checkCollectionGolden(t, "export_v1", export.Document)

	doc := string(export.Document)
	require.NotContains(t, doc, "link_method", "a machine preference is never exported")
	require.NotContains(t, doc, "is_default", "nor is the default flag")
	require.Contains(t, export.Notes[0], "curator", "re-exporting someone else's collection says so once")
}

func TestExportCollection_OptionsOverrideTheStoredMetadata(t *testing.T) {
	svc, game := exportFixture(t)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{
		Version: 4, Author: "me", Description: "New words.", InstallInstructions: "Read me.", Title: "WNC",
	})
	require.NoError(t, err)
	doc, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	require.Equal(t, 4, doc.Info.Version)
	require.Equal(t, "me", doc.Info.Author)
	require.Equal(t, "New words.", doc.Info.Description)
	require.Equal(t, "Read me.", doc.Info.InstallInstructions)
	require.Equal(t, "WNC", doc.Info.Title)
	require.Equal(t, "2f8c7a3e-0000-4000-8000-000000000001", doc.Info.ID, "the stored id is kept")
	require.Equal(t, "curator", doc.Info.Origin.Author, "the origin is carried through")
}

func TestExportCollection_ProfileWithoutIdentityGetsAnID(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "p", GameID: "g"}))

	export, err := svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err)
	doc, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), doc.Info.ID)
	require.Equal(t, 1, doc.Info.Version)
	require.Empty(t, export.Notes, "nothing to say about an author's own profile")
	require.Empty(t, doc.Requires)

	// The minted id is the profile's from now on: recorded once, kept by
	// every later export.
	saved, err := config.LoadProfile(svc.ConfigDir(), "g", "p")
	require.NoError(t, err)
	require.Equal(t, doc.Info.ID, saved.Collection.ID)
	again, err := svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err)
	againDoc, err := config.ParseCollection(again.Document)
	require.NoError(t, err)
	require.Equal(t, doc.Info.ID, againDoc.Info.ID)
}

func TestExportCollection_IDThatCannotBeRecordedFailsTheExport(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "p", GameID: "g"}))
	path, err := config.ProfilePath(svc.ConfigDir(), "g", "p")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o444))

	_, err = svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "recording the collection identity")
}

func TestExportCollection_Container(t *testing.T) {
	svc, game := exportFixture(t)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	container, err := export.Container()
	require.NoError(t, err)
	require.True(t, core.IsCollectionContainer(container))
	require.True(t, core.IsCollectionDocument(container))
	require.True(t, core.IsCollectionDocument(export.Document))
	require.False(t, core.IsCollectionDocument([]byte("name: p\ngame_id: g\nmods: []\n")), "version 0 is not")
}

// TestExportCollection_MetadataIsRecordedAndReused is the owner's ruling of
// 2026-11-12: what an export's flags set is saved in the profile's
// collection block, the next export reuses it, a flag overrides it (and the
// override is saved), and the version moves only when the author sets it.
func TestExportCollection_MetadataIsRecordedAndReused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "p", GameID: "g"}))
	export := func(opts core.CollectionExportOptions) *domain.CollectionDocument {
		t.Helper()
		e, err := svc.ExportCollection(context.Background(), game.ID, "p", opts)
		require.NoError(t, err)
		doc, err := config.ParseCollection(e.Document)
		require.NoError(t, err)
		return doc
	}
	saved := func() *domain.CollectionInfo {
		t.Helper()
		p, err := config.LoadProfile(svc.ConfigDir(), "g", "p")
		require.NoError(t, err)
		return p.Collection
	}

	first := export(core.CollectionExportOptions{
		Title: "Night", Author: "me", Description: "Words.", InstallInstructions: "Read me.", Version: 2,
	})
	require.Equal(t, first.Info, *saved(), "the profile records exactly what was published")

	again := export(core.CollectionExportOptions{})
	require.Equal(t, first.Info, again.Info, "a bare export reuses every saved value, version included")

	changed := export(core.CollectionExportOptions{Author: "you"})
	require.Equal(t, "you", changed.Info.Author)
	require.Equal(t, 2, changed.Info.Version, "never bumped automatically")
	require.Equal(t, "Night", changed.Info.Title)
	require.Equal(t, "you", saved().Author, "the override is saved")
	require.Equal(t, first.Info.ID, saved().ID)

	bumped := export(core.CollectionExportOptions{Version: 3})
	require.Equal(t, 3, bumped.Info.Version)
	require.Equal(t, 3, export(core.CollectionExportOptions{}).Info.Version)
}

func TestExportCollection_MetadataThatCannotBeRecordedFailsTheExport(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "p", GameID: "g", Collection: &domain.CollectionInfo{ID: "kept", Version: 1, Author: "me"},
	}))
	path, err := config.ProfilePath(svc.ConfigDir(), "g", "p")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o444))

	_, err = svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err, "nothing changed, so nothing is written: a read-only profile still exports")

	_, err = svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{Author: "you"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "recording the collection identity")
}

// TestExportCollection_NoRecordNeverWrites is #621 review S8's core half:
// a read-only export of a profile whose identity still has to be recorded
// refuses, and RecordCollectionIdentity is the write it waits for.
func TestExportCollection_NoRecordNeverWrites(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "p", GameID: "g"}))
	path, err := config.ProfilePath(svc.ConfigDir(), "g", "p")
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{NoRecord: true})
	require.ErrorIs(t, err, core.ErrCollectionIdentityUnrecorded)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "a read-only export writes nothing")

	info, err := svc.RecordCollectionIdentity(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, info.ID)
	again, err := svc.RecordCollectionIdentity(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err)
	require.Equal(t, info.ID, again.ID, "recording again keeps the id")

	export, err := svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{NoRecord: true})
	require.NoError(t, err)
	doc, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	require.Equal(t, info.ID, doc.Info.ID)
}
