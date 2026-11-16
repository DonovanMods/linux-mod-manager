package core_test

// #646: a collection fills Baldur's Gate 3's .pak order. An lmm collection
// document carrying load_orders: {baldursgate3: [...]} imports into a game
// on the baldurs-gate-3 adapter - the plan says lmm lists the paks in the
// game's mod list - the profile keeps the order as written, and the deploy
// lists the paks' modules in modsettings.lsx in that order. A NexusMods
// recipe's top-level loadOrder converts onto the same order
// (collection_recipe_test.go, collection_convert_loadorders_internal_test.go).

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionImport_FillsThePakOrderAndTheDeployFollowsIt(t *testing.T) {
	ctx := context.Background()
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX)
	doc := "lmm_collection: 1\nname: default\ngame:\n  name: Baldur's Gate 3\nrequires: [\"file_load_order:baldursgate3\"]\n" +
		"mods:\n  - {source_id: src, mod_id: a, version: \"1.0\"}\n  - {source_id: src, mod_id: b, version: \"1.0\"}\n" +
		"load_orders: {baldursgate3: [ModB.pak, Override.pak, ModA.pak]}\n"

	plan, err := f.svc.PlanCollectionImport(ctx, core.CollectionImportRequest{Data: []byte(doc), GameID: f.game.ID})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported, "the game's adapter writes the pak order")
	require.Len(t, plan.Collection.LoadOrders, 1)
	sum := plan.Collection.LoadOrders[0]
	assert.Equal(t, modtypes.PakOrderID, sum.ID)
	assert.Equal(t, "Paks", sum.Label)
	assert.True(t, sum.Managed)
	assert.True(t, sum.Enables)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"ModB.pak", "Override.pak", "ModA.pak"}, saved.LoadOrders[modtypes.PakOrderID])

	// The mods install; the deploy lists their modules in the collection's
	// order, the override pak with none.
	seedNamedInstalledMod(t, f.svc, f.game, "src", "a", "Mod a", "1.0", true, map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	seedNamedInstalledMod(t, f.svc, f.game, "src", "b", "Mod b", "1.0", true, map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB), "Override.pak": lariantest.OverridePak()})
	f.deploy(t)
	assert.Equal(t, []string{"GustavX", "Mod B", "Mod A"}, f.entries(t))

	report, err := f.svc.FileLoadOrder(ctx, f.game, "default", modtypes.PakOrderID)
	require.NoError(t, err)
	assert.Empty(t, report.NotDeployed, "an override pak is deployed, not missing, though it has no entry")
}
