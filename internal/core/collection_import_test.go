package core_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

// exportedContainer exports exportFixture's profile as a `.lmmc`.
func exportedContainer(t *testing.T) []byte {
	t.Helper()
	svc, game := exportFixture(t)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	data, err := export.Container()
	require.NoError(t, err)
	return data
}

// recipient is a second machine: the same game under another local id,
// recognisable by its Steam app id only.
func recipient(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{
		ID: "cyber", Name: "CP", ModPath: t.TempDir(),
		SourceIDs: map[string]string{"steamworkshop": "1091500"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

func TestCollectionImport_RoundTripIntoAnotherMachine(t *testing.T) {
	data := exportedContainer(t)
	svc, game := recipient(t)

	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data})
	require.NoError(t, err)
	c := plan.Collection
	require.NotNil(t, c)
	require.True(t, c.Container)
	require.Equal(t, "cyber", c.GameID, "matched to the local game by its Steam app id")
	require.Equal(t, "steam_app_id", c.MatchedBy)
	require.Equal(t, "coop", c.ProfileName)
	require.Equal(t, "Welcome to Night City", c.Name)
	require.Equal(t, []string{"external"}, c.Requires)
	require.Empty(t, c.Unsupported)
	require.True(t, c.HooksImportable)
	require.Equal(t, []core.ImportHook{{Hook: "install.after_all", Command: "/usr/bin/true"}}, c.Hooks)
	require.Equal(t, []string{"engine/config/user.ini"}, c.Overrides)
	require.Len(t, c.Items, 2)
	require.Equal(t, "Much Better Ammo", c.Items[0].Name)
	require.Contains(t, c.Items[0].Unavailable, `no source "nexusmods"`, "a source this machine lacks is named, with the remedy")
	require.Equal(t, "cyber", plan.Profile.GameID, "the profile is saved under the LOCAL game id")

	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.HooksWithheld)

	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, "2f8c7a3e-0000-4000-8000-000000000001", saved.Collection.ID)
	require.Equal(t, 3, saved.Collection.Version)
	require.Equal(t, "curator", saved.Collection.Origin.Author)
	require.Len(t, saved.External, 1)
	require.Equal(t, "[A]\nb=1\n", string(saved.Overrides["engine/config/user.ini"]))
	require.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit, "no hooks without --with-hooks")
	require.False(t, saved.LinkMethodExplicit, "the author's link method never travels")
	require.True(t, saved.Mods[1].Disabled)
	require.Equal(t, "UI", saved.Mods[1].Category)
}

func TestCollectionImport_WithHooksFromAFile(t *testing.T) {
	data := exportedContainer(t)
	svc, game := recipient(t)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data})
	require.NoError(t, err)
	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true, WithHooks: true}, nil)
	require.NoError(t, err)
	require.Zero(t, result.HooksWithheld)
	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, "/usr/bin/true", saved.Hooks.Install.AfterAll)
	require.True(t, saved.HooksExplicit.Install.AfterAll)
}

func TestCollectionImport_NeverHooksFromAURL(t *testing.T) {
	data := exportedContainer(t)
	svc, game := recipient(t)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: data, Input: core.CollectionFromURL})
	require.NoError(t, err)
	require.False(t, plan.Collection.HooksImportable)
	require.Len(t, plan.Collection.Hooks, 1, "the plan still lists them")

	// #622: --with-hooks is ignored for a URL, never refused.
	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true, WithHooks: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.HooksWithheld)
	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit, "a URL's hooks are never saved")
}

func TestCollectionImport_UnsupportedFeatureRefusesWithNothingWritten(t *testing.T) {
	svc, game := recipient(t)
	doc := "lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\nrequires: [from-the-future, external]\nmods:\n  - {source_id: s, mod_id: m}\n"
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc)})
	require.NoError(t, err, "the plan always renders")
	require.Equal(t, []core.UnsupportedCollectionFeature{{Feature: "from-the-future"}}, plan.Collection.Unsupported)

	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	var unsupported *core.CollectionUnsupportedError
	require.True(t, errors.As(err, &unsupported))
	require.True(t, core.IsCollectionUnsupported(err))
	require.Equal(t, "from-the-future", unsupported.Unsupported[0].Feature)
	require.Contains(t, unsupported.Supported, "external")
	require.Contains(t, unsupported.Supported, "fomod")
	_, err = config.LoadProfile(svc.ConfigDir(), "cyber", "p")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestCollectionImport_GameMatching(t *testing.T) {
	svc := newFlowsTestService(t)
	for _, g := range []*domain.Game{
		{ID: "a", Name: "A", ModPath: t.TempDir(), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}},
		{ID: "b", Name: "B", ModPath: t.TempDir(), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}},
		{ID: "c", Name: "C", ModPath: t.TempDir(), SourceIDs: map[string]string{"curseforge": "432"}},
	} {
		require.NoError(t, svc.SaveGame(context.Background(), g))
	}
	plan := func(game, gameID string) (*core.ImportPlan, error) {
		return svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{
			Data:   []byte("lmm_collection: 1\nname: p\ngame:\n" + game + "mods: []\n"),
			GameID: gameID,
		})
	}

	p, err := plan("  sources: {curseforge: \"432\"}\n", "")
	require.NoError(t, err)
	require.Equal(t, "c", p.Collection.GameID)
	require.Equal(t, "source:curseforge", p.Collection.MatchedBy)

	_, err = plan("  sources: {nexusmods: skyrimspecialedition}\n", "")
	var gameErr *core.CollectionGameError
	require.True(t, errors.As(err, &gameErr))
	require.Equal(t, []string{"a", "b"}, gameErr.Candidates)
	require.Contains(t, err.Error(), "--game")

	_, err = plan("  name: Elsewhere\n  steam_app_id: \"1\"\n", "")
	require.True(t, core.IsCollectionGameUnresolved(err))
	require.Contains(t, err.Error(), "matches none")

	p, err = plan("  sources: {nexusmods: skyrimspecialedition}\n", "b")
	require.NoError(t, err)
	require.Equal(t, "b", p.Collection.GameID)
	require.Equal(t, "game", p.Collection.MatchedBy)
	require.Empty(t, p.Collection.Warnings)

	p, err = plan("  sources: {curseforge: \"999\"}\n", "c")
	require.NoError(t, err)
	require.Contains(t, p.Collection.Warnings[0], "importing into c as asked")
}

func TestCollectionImport_ReaderRefusals(t *testing.T) {
	svc, _ := recipient(t)
	plan := func(doc string) error {
		_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
		return err
	}
	err := plan("lmm_collection: 2\nname: p\n")
	require.ErrorIs(t, err, core.ErrCollectionFormatTooNew)
	require.True(t, core.IsInvalidCollection(err))

	require.ErrorIs(t, plan("name: p\ngame_id: cyber\nmods: []\n"), core.ErrInvalidCollection, "version 0 has its own path")

	long := make([]byte, 2000)
	for i := range long {
		long[i] = 'x'
	}
	require.ErrorIs(t, plan("lmm_collection: 1\nname: p\nauthor: "+string(long)+"\nmods: []\n"), core.ErrInvalidCollection)

	err = plan("lmm_collection: 1\nname: ../escape\nmods: []\n")
	require.NoError(t, err, "a name that cannot name a file is slugged, not trusted")
}

func TestCollectionImport_SanitisesWhatAStrangerWrote(t *testing.T) {
	svc, _ := recipient(t)
	doc := "lmm_collection: 1\nname: p\ntitle: \"Evil\\u202eTitle\\u001b[31m\"\nsigner: someone\n" +
		"mods:\n  - {source_id: s, mod_id: m, name: \"N\\u0007ame\"}\n  - {name: nothing}\n"
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	c := plan.Collection
	require.NotContains(t, c.Name, "\u202e")
	require.NotContains(t, c.Name, "\x1b")
	require.Len(t, c.Items, 1, "an entry with no ids is skipped")
	require.Equal(t, "Name", c.Items[0].Name)
	require.Len(t, c.Warnings, 2)
}

func TestCollectionImport_PerItemFailuresStillImportTheRest(t *testing.T) {
	// Owner ruling 2026-11-12 (Q1): a v1 import keeps ordinary import's
	// per-item failure counting.
	svc, game := recipient(t)
	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("cyber", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "cyber"})

	doc := "lmm_collection: 1\nname: p\nmods:\n  - {source_id: src, mod_id: mod1, version: \"1.0\"}\n  - {source_id: gone, mod_id: x}\n"
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Collection.Items[1].Unavailable)

	result, err := svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.True(t, errors.As(err, &incomplete), "the missing source fails its own item: %v", err)
	require.Equal(t, 1, result.Installed)
	require.Equal(t, 1, result.Failed)
	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "cyber", "p")
	require.NoError(t, err)
}
