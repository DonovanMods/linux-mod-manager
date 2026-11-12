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
	require.Contains(t, err.Error(), "recording the collection id")
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
