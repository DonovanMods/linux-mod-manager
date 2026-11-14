package serve_test

// #613: updating a collection profile from the browser - the profiles
// modal's "Update collection…", the plan view's added/changed/removed
// buckets and conflicts, the removal and override checkboxes, and the job
// leaving the profile at the new version with exactly the choices made.
// The origin is an httptest TLS server the Service's downloads trust, and
// every entry is switched off, so nothing downloads: no network.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const e2eUpdateV1 = `lmm_collection: 1
id: coll-1
version: 1
name: shared
title: Shared Setup
game_id: g1
mods:
  - {source_id: fake, mod_id: one, version: "1.0", disabled: true}
  - {source_id: fake, mod_id: two, version: "1.0", disabled: true}
  - {source_id: fake, mod_id: four, version: "1.0", disabled: true}
`

const e2eUpdateV2 = `lmm_collection: 1
id: coll-1
version: 2
name: shared
title: Shared Setup
game_id: g1
mods:
  - {source_id: fake, mod_id: one, version: "1.1", disabled: true}
  - {source_id: fake, mod_id: three, version: "1.0", disabled: true}
  - {source_id: fake, mod_id: four, version: "2.0", disabled: true}
`

func TestE2E_CollectionUpdate_PlanChoicesAndApply(t *testing.T) {
	sandboxE2EEnv(t)
	var body atomic.Pointer[string]
	v1 := e2eUpdateV1
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*body.Load()))
	}))
	t.Cleanup(srv.Close)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := newFakeSource("fake")
	svc.RegisterSource(src)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{src.ID(): ""}}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	// The collection profile, imported from the address; the user then
	// locks "four", so the new version's change to it is a conflict.
	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/shared.yaml", GameID: game.ID})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	p.FindRef("fake", "four").Locked = true
	p.FindRef("fake", "four").FromCollection = false
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
	v2 := e2eUpdateV2
	body.Store(&v2)

	baseURL := startE2EServer(t, svc)
	browser, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: browser, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	var planText string
	var plainSyncRows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesImport(),
		// Only the collection profile offers the update.
		chromedp.Evaluate(`document.querySelectorAll('[data-action="update-collection"]').length`, &plainSyncRows),
		clickWhenSettled(`[data-action="update-collection"][data-profile="shared"]`),
		chromedp.WaitVisible(`[data-testid="update-summary"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="collection_update"]`, &planText),
		clickWhenSettled(`[data-testid="update-removed"] input[data-key="fake:two"]`),
		clickWhenSettled(`[data-testid="update-conflicts"] input[data-key="fake:four"]`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)

	assert.Equal(t, 1, plainSyncRows, "the default profile has no collection to update")
	assert.Contains(t, planText, "to version 2 of Shared Setup")
	assert.Contains(t, planText, "Will be added (1)")
	assert.Contains(t, planText, "fake:three")
	assert.Contains(t, planText, "Will move to the collection's new pin (1)")
	assert.Contains(t, planText, "1.0 → 1.1")
	assert.Contains(t, planText, "No longer in the collection (1)")
	assert.Contains(t, planText, "Conflicts - mods you locked or edited (1)")
	assert.NotContains(t, planText, srv.URL+"/shared.yaml?", "no query to redact, none shown")

	require.Eventually(t, func() bool {
		p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
		// The new version is saved before the obsolete member's uninstall
		// removes its ref (#613 review: a failed removal must never strand
		// a mod), so wait for both, not only the version.
		return err == nil && p.Collection.Version == 2 && p.FindRef("fake", "two") == nil
	}, 15*time.Second, 50*time.Millisecond, "the update records the new version and removes the checked member")
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("fake", "two"), "removed, as checked")
	assert.Equal(t, "2.0", saved.FindRef("fake", "four").Version, "taken the collection's way, as checked")
	assert.Equal(t, "1.1", saved.FindRef("fake", "one").Version)
	assert.NotNil(t, saved.FindRef("fake", "three"))
	assert.Empty(t, f.BrowserErrors())
}

// #613 review: an obsolete member the update could not remove (here, its
// uninstall hook refuses) is said where the page moved on: the job's toast
// is held and carries the warning, and the member stays listed and
// installed.
func TestE2E_CollectionUpdate_ARemovalThatFailsIsSaid(t *testing.T) {
	sandboxE2EEnv(t)
	var body atomic.Pointer[string]
	v1 := e2eUpdateV1
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*body.Load()))
	}))
	t.Cleanup(srv.Close)
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := newFakeSource("fake")
	svc.RegisterSource(src)
	ctx := context.Background()
	script := filepath.Join(t.TempDir(), "refuse.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{src.ID(): ""},
		Hooks: domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: script}}}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/shared.yaml", GameID: game.ID})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "shared"))
	// "two" is installed, so removing it runs the uninstall and its hook.
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "two", SourceID: "fake", Name: "Two", Version: "1.0", GameID: game.ID},
		ProfileName: "shared", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	v2 := e2eUpdateV2
	body.Store(&v2)

	baseURL := startE2EServer(t, svc)
	browser, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: browser, BaseURL: baseURL, Svc: svc, Game: game, Profile: "shared", BrowserErrors: browserErrors}

	var toast string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesImport(),
		clickWhenSettled(`[data-action="update-collection"][data-profile="shared"]`),
		chromedp.WaitVisible(`[data-testid="update-summary"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="update-removed"] input[data-key="fake:two"]`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		pollUntil(`Array.from(document.querySelectorAll(".toast__detail")).some((d) => d.textContent.includes("could not remove"))`),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".toast__detail")).map((d) => d.textContent).join("\n")`, &toast),
	)
	assert.Contains(t, toast, "could not remove fake:two")
	assert.Contains(t, toast, "stays in the profile, installed")
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.NotNil(t, saved.FindRef("fake", "two"), "still listed")
	_, err = svc.GetInstalledMod(ctx, "fake", "two", game.ID, "shared")
	assert.NoError(t, err, "still installed")
	assert.Empty(t, f.BrowserErrors())
}
