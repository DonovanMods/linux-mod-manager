package core_test

// #613 x #623: a collection update whose origin serves a `.lmmc` container
// with bundled items moves an item to the new version's files, keeps the
// new container for the lmm-collection source, and holds back an item
// holding code unless the update allows any override.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionUpdate_BundledItemsFromAContainer(t *testing.T) {
	var body atomic.Pointer[[]byte]
	v1 := patchContainer(t)
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(*body.Load()) }))
	t.Cleanup(srv.Close)
	svc := newFlowsTestServiceWith(t, srv.Client())
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy}
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, game))

	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/coop.lmmc", GameID: game.ID})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
	require.NoError(t, err)
	require.True(t, saved.Mods[0].FromCollection)
	p := saved
	p.Collection.Version = 1
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))

	// Version 2: the patch's files change, and a tool holding code joins.
	doc := strings.Replace(bundledDoc, "name: coop\n", "name: coop\nversion: 2\n", 1) +
		"  - source_id: lmm-collection\n    mod_id: tool\n    name: Tool\n"
	v2 := bundleContainer(t, doc,
		bundleMember{name: "files/patch/patch.esp", content: "PATCH v2"},
		bundleMember{name: "files/tool/bin/helper.dll", content: "MZ" + strings.Repeat("\x00", 0x3a) + "\x40\x00\x00\x00PE\x00\x00"},
	)
	body.Store(&v2)
	update, err := svc.PlanCollectionUpdate(ctx, game, core.CollectionUpdateRequest{Profile: "coop"})
	require.NoError(t, err)
	require.Empty(t, update.Unsupported)
	assert.Equal(t, []string{"lmm-collection:c0ffee.patch"}, keys(update.Changed))
	assert.Equal(t, []string{"lmm-collection:c0ffee.tool"}, keys(update.Added))
	require.Len(t, update.RestrictedOverrides, 1)
	assert.Equal(t, "files/tool/", update.RestrictedOverrides[0].Path, "the tool is held back unless allowed")

	result, err := svc.ApplyCollectionUpdate(ctx, game, update, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Import)
	assert.Equal(t, []string{"tool"}, result.Import.BundledSkipped)

	got, err := os.ReadFile(filepath.Join(game.ModPath, "patch.esp"))
	require.NoError(t, err)
	assert.Equal(t, "PATCH v2", string(got), "the new version's files, from the kept container")
	saved, err = config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
	require.NoError(t, err)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.Nil(t, saved.FindRef(domain.SourceCollection, "c0ffee.tool"), "the held-back tool is not added")
	assert.NotNil(t, saved.FindRef(domain.SourceCollection, "c0ffee.patch"))
}
