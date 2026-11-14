package core_test

// #620 x #621: a profile's load_orders: block travels in the lmm collection
// format. An export carries it and declares `requires:
// file_load_order:<id>`, so an lmm without that load order - an older one,
// or one importing into a game whose adapter does not write it - refuses
// the document rather than import it with the order dropped; an import
// keeps it as written, and refuses a stranger's names it could not keep.

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/cyberpunk2077"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

var collectionLoadOrders = domain.FileLoadOrders{archiveOrder: {"! fixes.archive", "basegame_b.archive", "basegame_a.archive"}}

const loadOrderFeature = "file_load_order:cyberpunk2077-archive"

// cyberRecipient is recipient's game on the cyberpunk2077 adapter.
func cyberRecipient(t *testing.T) *core.Service {
	t.Helper()
	svc, game := recipient(t)
	svc.RegisterAdapter(cyberpunk2077.New())
	game.Adapter = cyberpunk2077.ID
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc
}

// TestExportCollection_LoadOrdersRequireTheirFeature is the version-1
// export/import round trip for the load_orders: block, against its golden.
func TestExportCollection_LoadOrdersRequireTheirFeature(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "cp", Name: "Cyberpunk 2077", ModPath: t.TempDir(), SourceIDs: map[string]string{"steamworkshop": "1091500"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	profile := &domain.Profile{
		Name: "archives", GameID: game.ID,
		Mods:       []domain.ModReference{{SourceID: "nexusmods", ModID: "1", Version: "1.0"}},
		Collection: &domain.CollectionInfo{ID: "2f8c7a3e-0000-4000-8000-000000000620", Version: 1},
		LoadOrders: collectionLoadOrders,
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	export, err := svc.ExportCollection(context.Background(), game.ID, "archives", core.CollectionExportOptions{})
	require.NoError(t, err)
	checkCollectionGolden(t, "export_v1_loadorders", export.Document)
	require.Contains(t, string(export.Document), "requires:\n    - "+loadOrderFeature+"\n")

	recv := cyberRecipient(t)
	plan, err := recv.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: export.Document, GameID: "cyber"})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported, "the game's adapter writes this order")
	require.Equal(t, collectionLoadOrders, plan.Profile.LoadOrders, "the plan previews the order it will save")
	require.Len(t, plan.Collection.LoadOrders, 1)
	sum := plan.Collection.LoadOrders[0]
	require.Equal(t, archiveOrder, sum.ID)
	require.Equal(t, "Archives", sum.Label)
	require.True(t, sum.Managed)
	require.True(t, sum.FirstWins)
	require.Len(t, sum.Files, 3)
	require.Equal(t, "! fixes.archive", sum.Files[0].Name)

	_, err = recv.ApplyCollectionImport(context.Background(), &domain.Game{ID: "cyber"}, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(recv.ConfigDir(), "cyber", "archives")
	require.NoError(t, err)
	require.Equal(t, collectionLoadOrders, saved.LoadOrders, "the order is saved as written")

	again, err := recv.ExportCollection(context.Background(), "cyber", "archives", core.CollectionExportOptions{})
	require.NoError(t, err)
	reparsed, err := config.ParseCollection(again.Document)
	require.NoError(t, err)
	require.Equal(t, collectionLoadOrders, reparsed.LoadOrders)
	require.Equal(t, []string{loadOrderFeature}, reparsed.Requires)
}

func collectionLoadOrderDoc(block string) string {
	return "lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\nrequires: [\"" + loadOrderFeature + "\"]\n" +
		"mods:\n  - {source_id: s, mod_id: m}\nload_orders: " + block + "\n"
}

// A game whose adapter does not write the order refuses the import, and a
// hand-edited document that drops the feature is gated on it all the same.
func TestCollectionImport_LoadOrderNeedsAGameThatWritesIt(t *testing.T) {
	svc, _ := recipient(t)
	doc := strings.Replace(collectionLoadOrderDoc("{cyberpunk2077-archive: [a.archive]}"), "requires: [\""+loadOrderFeature+"\"]\n", "", 1)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	require.Contains(t, plan.Collection.Requires, loadOrderFeature)
	require.Equal(t, []core.UnsupportedCollectionFeature{{Feature: loadOrderFeature}}, plan.Collection.Unsupported)
	require.False(t, plan.Collection.LoadOrders[0].Managed)
	_, err = svc.ApplyCollectionImport(context.Background(), &domain.Game{ID: "cyber"}, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.True(t, core.IsCollectionUnsupported(err), "%v", err)
}

// TestCollectionImport_RefusesLoadOrderNamesItCannotKeepAsWritten: names
// are matched against deployed files, so none is rewritten.
func TestCollectionImport_RefusesLoadOrderNamesItCannotKeepAsWritten(t *testing.T) {
	svc := cyberRecipient(t)
	for name, block := range map[string]string{
		"bidi in a name":     "{cyberpunk2077-archive: [\"a‮.archive\"]}",
		"newline in a name":  "{cyberpunk2077-archive: [\"a\\nb.archive\"]}",
		"path in a name":     "{cyberpunk2077-archive: [../a.archive]}",
		"backslash":          "{cyberpunk2077-archive: [\"x\\\\a.archive\"]}",
		"empty name":         "{cyberpunk2077-archive: [\"\"]}",
		"long name":          "{cyberpunk2077-archive: [" + strings.Repeat("x", 300) + "]}",
		"id with a space":    "{\"cyberpunk2077 archive\": [a.archive]}",
		"id with upper case": "{Cyberpunk2077-Archive: [a.archive]}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(collectionLoadOrderDoc(block)), GameID: "cyber"})
			require.ErrorIs(t, err, core.ErrInvalidCollection)
			require.Contains(t, err.Error(), "its load orders:")
		})
	}
	_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{
		Data: []byte(collectionLoadOrderDoc("{cyberpunk2077-archive: [\"! Café – Ultra.archive\"]}")), GameID: "cyber"})
	require.NoError(t, err, "unusual but plain names import")
}
