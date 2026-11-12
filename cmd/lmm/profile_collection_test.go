package main

// #621 at the CLI seam: `lmm profile export` writes the version-1
// collection (stdout YAML; -o a .lmmc, or YAML with --plain; --json
// unchanged), and `lmm profile import <file>` routes a collection through
// core's collection import while a version-0 export keeps its own path.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setExportFlags sets the export flags for one test.
func setExportFlags(t *testing.T, output string, plain bool, meta core.CollectionExportOptions) {
	t.Helper()
	oldOut, oldPlain, oldMeta := profileExportOutput, profileExportPlain, profileExportMeta
	profileExportOutput, profileExportPlain, profileExportMeta = output, plain, meta
	t.Cleanup(func() { profileExportOutput, profileExportPlain, profileExportMeta = oldOut, oldPlain, oldMeta })
}

// collectionExportService is a game with one profile carrying a hook, an
// override and a fixed collection id.
func collectionExportService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, configDir := newProfileExportTestService(t)
	game.SourceIDs = map[string]string{"steamworkshop": "1091500"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name: "coop", GameID: game.ID,
		Mods:          []domain.ModReference{{SourceID: "src", ModID: "a", Version: "1.0"}},
		Hooks:         domain.GameHooks{Install: domain.HookConfig{AfterAll: "/usr/bin/true"}},
		HooksExplicit: domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}},
		Overrides:     map[string][]byte{"cfg/user.ini": []byte("[A]\n")},
		Collection:    &domain.CollectionInfo{ID: "fixed-id", Title: "Co-op"},
	}))
	return svc, game
}

func TestDoProfileExport_OutputWritesAContainer(t *testing.T) {
	svc, game := collectionExportService(t)
	path := filepath.Join(t.TempDir(), "coop.lmmc")
	setExportFlags(t, path, false, core.CollectionExportOptions{Author: "me"})

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileExport(context.Background(), svc, game, "coop")
	})
	require.NoError(t, err)
	assert.Equal(t, "✓ Exported coop to "+path+"\n", stdout)
	assert.Contains(t, stderr, "--with-hooks", "the export's note goes to stderr")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, core.IsCollectionContainer(data))
}

func TestDoProfileExport_PlainOutputWritesYAML(t *testing.T) {
	svc, game := collectionExportService(t)
	path := filepath.Join(t.TempDir(), "coop.yaml")
	setExportFlags(t, path, true, core.CollectionExportOptions{})
	_, _, err := captureStdoutStderrErr(t, func() error {
		return doProfileExport(context.Background(), svc, game, "coop")
	})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	doc, err := config.ParseCollection(data)
	require.NoError(t, err)
	assert.Equal(t, 1, doc.FormatVersion)
	assert.Equal(t, "fixed-id", doc.Info.ID)
}

func TestDoProfileExport_JSONRefusesTheCollectionFlags(t *testing.T) {
	svc, game := collectionExportService(t)
	withJSONOutput(t)
	setExportFlags(t, "x.lmmc", false, core.CollectionExportOptions{})
	err := doProfileExport(context.Background(), svc, game, "coop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")
}

func TestReportError_JSON_CollectionUnsupportedError(t *testing.T) {
	withJSONOutput(t)
	err := &core.CollectionUnsupportedError{
		Unsupported: []core.UnsupportedCollectionFeature{{Feature: "fomod", Items: []string{"nexusmods:107"}}},
		Supported:   []string{"external"},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"this collection needs fomod, which this version of lmm cannot import; update lmm and try again\",\n"+
		"  \"details\": {\n"+
		"    \"unsupported\": [\n"+
		"      {\n"+
		"        \"feature\": \"fomod\",\n"+
		"        \"items\": [\n"+
		"          \"nexusmods:107\"\n"+
		"        ]\n"+
		"      }\n"+
		"    ],\n"+
		"    \"supported\": [\n"+
		"      \"external\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

// exportedCollection is collectionExportService's profile as a .lmmc.
func exportedCollection(t *testing.T) []byte {
	t.Helper()
	svc, game := collectionExportService(t)
	export, err := svc.ExportCollection(context.Background(), game.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	data, err := export.Container()
	require.NoError(t, err)
	return data
}

// collectionRecipient is another machine with the same game under another
// id, and no profile.
func collectionRecipient(t *testing.T) (*core.Service, string) {
	t.Helper()
	svc, game, configDir := newProfileExportTestService(t)
	game.ID = "cyber"
	game.SourceIDs = map[string]string{"steamworkshop": "1091500"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, configDir
}

func setImportNoInstall(t *testing.T) {
	t.Helper()
	old := profileImportNoInstall
	profileImportNoInstall = true
	t.Cleanup(func() { profileImportNoInstall = old })
}

func TestDoCollectionImport_PrintsThePlanAndWithholdsHooks(t *testing.T) {
	data := exportedCollection(t)
	svc, configDir := collectionRecipient(t)
	setImportNoInstall(t)

	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionImport(context.Background(), svc, data, "")
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "Collection: Co-op (version 1)")
	assert.Contains(t, stdout, "matched by Steam app id 1091500")
	assert.Contains(t, stdout, "install.after_all: /usr/bin/true")
	assert.Contains(t, stdout, "re-run with --with-hooks")
	assert.Contains(t, stdout, "cfg/user.ini")
	assert.Contains(t, stdout, `lmm has no source "src" here`)
	assert.Contains(t, stdout, "✓ Imported profile: coop")
	assert.Contains(t, stdout, "hook(s) were not imported")

	saved, err := config.LoadProfile(configDir, "cyber", "coop")
	require.NoError(t, err)
	assert.False(t, saved.HooksExplicit.Install.AfterAll)
	assert.Equal(t, "fixed-id", saved.Collection.ID)
}

func TestDoCollectionImport_WithHooks(t *testing.T) {
	data := exportedCollection(t)
	svc, configDir := collectionRecipient(t)
	setImportNoInstall(t)
	old := profileImportWithHooks
	profileImportWithHooks = true
	t.Cleanup(func() { profileImportWithHooks = old })

	_, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionImport(context.Background(), svc, data, "")
	})
	require.NoError(t, err)
	saved, err := config.LoadProfile(configDir, "cyber", "coop")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/true", saved.Hooks.Install.AfterAll)
}

func TestDoCollectionImport_JSON(t *testing.T) {
	data := exportedCollection(t)
	svc, _ := collectionRecipient(t)
	setImportNoInstall(t)
	out := runJSONCommand(t, func() error {
		return doCollectionImport(context.Background(), svc, data, "")
	})
	var got core.CollectionImportResult
	decodeStrict(t, out, &got)
	assertJSONCLIGolden(t, "profile_import_collection", out)
}

func TestDoCollectionImport_UnsupportedRefusesBeforeAnyPrompt(t *testing.T) {
	svc, configDir := collectionRecipient(t)
	doc := []byte("lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\nrequires: [fomod]\nmods: []\n")
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionImport(context.Background(), svc, doc, "")
	})
	require.True(t, core.IsCollectionUnsupported(err))
	assert.Contains(t, stdout, "needs features this version of lmm cannot import")
	assert.NotContains(t, stdout, "[Y/n]")
	_, err = config.LoadProfile(configDir, "cyber", "p")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestRunProfileImport_VersionZeroRefusesCollectionFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: p\ngame_id: g\nmods: []\n"), 0o644))
	old := profileImportWithHooks
	profileImportWithHooks = true
	t.Cleanup(func() { profileImportWithHooks = old })
	err := runProfileImport(profileImportCmd, []string{path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--with-hooks applies to a collection")
}
