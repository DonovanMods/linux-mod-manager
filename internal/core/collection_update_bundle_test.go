package core_test

// #613 x #623: a collection update whose origin serves a `.lmmc` container
// with bundled items moves an item to the new version's files, keeps the
// new container for the lmm-collection source, and holds back an item
// holding code unless the update allows any override.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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

// bundleUpdateFixture imports v1 from an https address, installing it (with
// any override allowed when allowAny), and records it as version 1; serve
// replaces what the address serves.
func bundleUpdateFixture(t *testing.T, v1 []byte, allowAny bool) (*core.Service, *domain.Game, func([]byte)) {
	t.Helper()
	var body atomic.Pointer[[]byte]
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(*body.Load()) }))
	t.Cleanup(srv.Close)
	svc := newFlowsTestServiceWith(t, srv.Client())
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy}
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, game))
	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/coop.lmmc", GameID: game.ID})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{Install: true, AllowAnyOverrides: allowAny}, nil)
	require.NoError(t, err)
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
	require.NoError(t, err)
	p.Collection.Version = 1
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
	return svc, game, func(b []byte) { body.Store(&b) }
}

// toolDoc names the patch and the tool, at version.
func toolDoc(version string) string {
	return strings.Replace(bundledDoc, "name: coop\n", "name: coop\nversion: "+version+"\n", 1) +
		"  - source_id: lmm-collection\n    mod_id: tool\n    name: Tool\n"
}

var dllMember = bundleMember{name: "files/tool/bin/helper.dll", content: "MZ" + strings.Repeat("\x00", 0x3a) + "\x40\x00\x00\x00PE\x00\x00"}

// keptAdmitted reads the admitted flags of the kept container data's index.
func keptAdmitted(t *testing.T, svc *core.Service, game *domain.Game, data []byte) map[string]bool {
	t.Helper()
	sum := sha256.Sum256(data)
	raw, err := os.ReadFile(filepath.Join(svc.CacheDir(), game.ID, "collections", "lmm-c0ffee", hex.EncodeToString(sum[:])[:32]+".json"))
	require.NoError(t, err)
	var index struct {
		Items map[string]struct {
			Admitted bool `json:"admitted"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	out := map[string]bool{}
	for name, it := range index.Items {
		out[name] = it.Admitted
	}
	return out
}

func toolRef(t *testing.T, svc *core.Service, game *domain.Game) *domain.ModReference {
	t.Helper()
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coop")
	require.NoError(t, err)
	return p.FindRef(domain.SourceCollection, "c0ffee.tool")
}

// #613 follow-up: a bundled item the profile already has, whose new
// version holds code, stays at the version it has when the update does not
// allow any override - and that version is still served, from the
// container the import kept.
func TestCollectionUpdate_AHeldBackExistingBundledItemKeepsItsVersion(t *testing.T) {
	v1 := bundleContainer(t, toolDoc("1"),
		bundleMember{name: "files/patch/patch.esp", content: "PATCH"},
		bundleMember{name: "files/tool/readme.txt", content: "hi"})
	svc, game, serve := bundleUpdateFixture(t, v1, false)
	ctx := context.Background()
	before := toolRef(t, svc, game)
	require.NotNil(t, before, "the code-free tool came in")

	v2 := bundleContainer(t, toolDoc("2"),
		bundleMember{name: "files/patch/patch.esp", content: "PATCH v2"},
		bundleMember{name: "files/tool/readme.txt", content: "hi"}, dllMember)
	serve(v2)
	update, err := svc.PlanCollectionUpdate(ctx, game, core.CollectionUpdateRequest{Profile: "coop"})
	require.NoError(t, err)
	result, err := svc.ApplyCollectionUpdate(ctx, game, update, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"tool"}, result.Import.BundledSkipped, "its new version was held back")

	after := toolRef(t, svc, game)
	require.NotNil(t, after, "still listed")
	assert.Equal(t, before.Version, after.Version, "at the version it had")
	assert.Equal(t, map[string]bool{"patch": true, "tool": false}, keptAdmitted(t, svc, game, v2),
		"the new container admits the patch and not the tool")

	// The kept version is still served: a lost cache entry is rebuilt.
	row, err := svc.GetInstalledMod(ctx, domain.SourceCollection, "c0ffee.tool", game.ID, "coop")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Delete(cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version}))
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "readme.txt")))
	require.NoError(t, svc.DeleteInstalledMod(ctx, row.SourceID, row.ID, game.ID, "coop"))
	apply, err := svc.PlanProfileApply(ctx, game, "coop")
	require.NoError(t, err)
	_, err = svc.ApplyProfileApply(ctx, game, apply, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(game.ModPath, "readme.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hi", string(got))
	_, err = os.Stat(filepath.Join(game.ModPath, "bin", "helper.dll"))
	assert.ErrorIs(t, err, os.ErrNotExist, "never the held-back version's code")
}

// #613 follow-up: --allow-any-overrides on an update admits the new
// version's code and moves the item to it.
func TestCollectionUpdate_AllowAnyOverridesAdmitsABundledItemsCode(t *testing.T) {
	v1 := bundleContainer(t, toolDoc("1"),
		bundleMember{name: "files/patch/patch.esp", content: "PATCH"},
		bundleMember{name: "files/tool/readme.txt", content: "hi"})
	svc, game, serve := bundleUpdateFixture(t, v1, false)
	ctx := context.Background()
	before := toolRef(t, svc, game)

	v2 := bundleContainer(t, toolDoc("2"),
		bundleMember{name: "files/patch/patch.esp", content: "PATCH v2"},
		bundleMember{name: "files/tool/readme.txt", content: "hi"}, dllMember)
	serve(v2)
	update, err := svc.PlanCollectionUpdate(ctx, game, core.CollectionUpdateRequest{Profile: "coop"})
	require.NoError(t, err)
	result, err := svc.ApplyCollectionUpdate(ctx, game, update, core.CollectionUpdateOptions{AllowAnyOverrides: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Import.BundledSkipped)
	after := toolRef(t, svc, game)
	assert.NotEqual(t, before.Version, after.Version, "moved to the new version")
	assert.Equal(t, map[string]bool{"patch": true, "tool": true}, keptAdmitted(t, svc, game, v2))
	_, err = os.Stat(filepath.Join(game.ModPath, "bin", "helper.dll"))
	assert.NoError(t, err, "the admitted code is deployed")
}

// #613 follow-up: a held-back item the profile already has at the new
// version moved nowhere, so the result does not list it as skipped.
func TestCollectionUpdate_AnUnchangedHeldBackItemIsNotListedAsSkipped(t *testing.T) {
	tool := []bundleMember{{name: "files/tool/readme.txt", content: "hi"}, dllMember}
	v1 := bundleContainer(t, toolDoc("1"), append([]bundleMember{{name: "files/patch/patch.esp", content: "PATCH"}}, tool...)...)
	svc, game, serve := bundleUpdateFixture(t, v1, true)
	ctx := context.Background()
	before := toolRef(t, svc, game)
	require.NotNil(t, before, "admitted at import")

	v2 := bundleContainer(t, toolDoc("2"), append([]bundleMember{{name: "files/patch/patch.esp", content: "PATCH v2"}}, tool...)...)
	serve(v2)
	update, err := svc.PlanCollectionUpdate(ctx, game, core.CollectionUpdateRequest{Profile: "coop"})
	require.NoError(t, err)
	result, err := svc.ApplyCollectionUpdate(ctx, game, update, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Import.BundledSkipped, "nothing about the tool moved")
	assert.Equal(t, before.Version, toolRef(t, svc, game).Version)
}
