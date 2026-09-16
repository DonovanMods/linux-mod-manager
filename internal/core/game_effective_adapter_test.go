package core_test

// #426 (#413 review F4): the game document names the adapter a game
// RESOLVES to, not only the one games.yaml configures. Since U2 a
// `deploy_mode: compile` game with no `adapter:` key compiles through
// icarus, and since U3 a game with BepInEx lays its archives out through
// bepinex - and every surface meant to say which adapter a game uses said
// "generic-files" for both.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/icarus"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListGameEntries_NamesTheEffectiveAdapter(t *testing.T) {
	svc := newFlowsTestService(t) // registers bepinex
	svc.RegisterAdapter(icarus.New())
	ctx := context.Background()

	installed := t.TempDir()
	bepinexInstall(t, installed, "", domain.LoaderBootstrapUnknown, time.Time{})
	declared := t.TempDir()

	games := []*domain.Game{
		{ID: "a-plain", Name: "Plain", InstallPath: t.TempDir()},
		{ID: "b-explicit-generic", Name: "Explicit", InstallPath: t.TempDir(), Adapter: "generic-files"},
		{ID: "c-compile", Name: "Compile", InstallPath: t.TempDir(), DeployMode: domain.DeployCompile},
		{ID: "d-declared", Name: "Declared", InstallPath: declared, ModPath: declared,
			Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}},
		{ID: "e-installed", Name: "Installed", InstallPath: installed, ModPath: installed},
		{ID: "f-explicit-icarus", Name: "Explicit Icarus", InstallPath: t.TempDir(), Adapter: "icarus"},
		// An override wins over the loader, exactly as it does when the
		// game is resolved - so this one's answer is its own key.
		{ID: "g-override", Name: "Override", InstallPath: installed, ModPath: installed, Adapter: "generic-files",
			Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}},
		// #413 re-review P-b: a mod_path that is not the game root keeps
		// the identity, installed and declared alike.
		{ID: "h-plugins-dir", Name: "Plugins Dir", InstallPath: installed,
			ModPath: filepath.Join(installed, "BepInEx", "plugins")},
		{ID: "i-declared-default-mods", Name: "Declared, mods dir", InstallPath: declared,
			ModPath: filepath.Join(declared, "mods"),
			Loader:  &domain.GameLoader{Kind: domain.LoaderKindBepInEx}},
	}
	for _, g := range games {
		require.NoError(t, svc.SaveGame(ctx, g))
	}

	entries, err := svc.ListGameEntries(ctx)
	require.NoError(t, err)
	got := map[string][2]string{}
	for _, e := range entries {
		got[e.ID] = [2]string{e.Adapter, e.EffectiveAdapter}
	}

	// {configured, effective}: effective is omitted for the identity,
	// the same "absent means generic-files" rule the configured key has.
	assert.Equal(t, map[string][2]string{
		"a-plain":                 {"", ""},
		"b-explicit-generic":      {"generic-files", ""},
		"c-compile":               {"", "icarus"},
		"d-declared":              {"", "bepinex"},
		"e-installed":             {"", "bepinex"},
		"f-explicit-icarus":       {"icarus", "icarus"},
		"g-override":              {"generic-files", ""},
		"h-plugins-dir":           {"", ""},
		"i-declared-default-mods": {"", ""},
	}, got)

	for _, e := range entries {
		assert.Equal(t, svc.AdapterName(&e.Game) == "generic-files", e.EffectiveAdapter == "",
			"%s: the document must agree with the resolver", e.ID)
	}
}

// TestGameDetail_NamesTheEffectiveAdapter: `lmm game show` and GET
// /api/v1/games/{id} embed the same row, so they carry the same answer.
func TestGameDetail_NamesTheEffectiveAdapter(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	detail, err := svc.GameDetail(context.Background(), game.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Adapter, "the configured key is still what games.yaml says")
	assert.Equal(t, "bepinex", detail.EffectiveAdapter)
}

// TestGameWrites_AnswerWithTheEffectiveAdapter: the single-step game writes
// answer with the list row, so a frontend splicing that answer into its
// table shows the same cell a reload would.
func TestGameWrites_AnswerWithTheEffectiveAdapter(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)
	ctx := context.Background()

	entry, err := svc.SetGameAdapter(ctx, game.ID, "generic-files")
	require.NoError(t, err)
	assert.Equal(t, "generic-files", entry.Adapter)
	assert.Empty(t, entry.EffectiveAdapter)

	entry, err = svc.SetGameAdapter(ctx, game.ID, "")
	require.NoError(t, err)
	assert.Empty(t, entry.Adapter)
	assert.Equal(t, "bepinex", entry.EffectiveAdapter, "clearing the key falls back to the derived adapter")

	loaderEntry, err := svc.UpdateGameLoader(ctx, game.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, loaderEntry.EffectiveAdapter, "no loader and nothing installed: the identity")
}

// TestListGameEntries_SaysWhenEveryFlowRefusesTheAdapter (#413 re-review
// L2): a game whose adapter AdapterFor refuses uses no adapter at all -
// every flow on it fails - so its row carries the refusal rather than an
// effective_adapter that names an adapter nothing runs, or an absent one
// that reads as generic-files.
func TestListGameEntries_SaysWhenEveryFlowRefusesTheAdapter(t *testing.T) {
	svc := newFlowsTestService(t) // registers bepinex
	ctx := context.Background()
	root := t.TempDir()
	games := []*domain.Game{
		{ID: "a-unknown", Name: "Unknown", InstallPath: root, Adapter: "bepinx"},
		{ID: "b-compile-generic", Name: "Compile", InstallPath: root, DeployMode: domain.DeployCompile, Adapter: "generic-files"},
		{ID: "c-bepinex-off-root", Name: "Off root", InstallPath: root, ModPath: filepath.Join(root, "mods"), Adapter: "bepinex"},
		{ID: "d-fine", Name: "Fine", InstallPath: root, ModPath: root, Adapter: "bepinex"},
	}
	for _, g := range games {
		require.NoError(t, svc.SaveGame(ctx, g))
	}

	entries, err := svc.ListGameEntries(ctx)
	require.NoError(t, err)
	byID := map[string]core.GameListEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	for id, want := range map[string]string{
		"a-unknown":          `unknown adapter "bepinx"`,
		"b-compile-generic":  "cannot compile",
		"c-bepinex-off-root": "is not its install path",
	} {
		e := byID[id]
		assert.Empty(t, e.EffectiveAdapter, "%s: no adapter is in use", id)
		assert.Contains(t, e.AdapterError, want, id)
		_, resolveErr := svc.AdapterFor(&e.Game)
		require.Error(t, resolveErr)
		assert.Equal(t, resolveErr.Error(), e.AdapterError, "%s: the row carries the refusal every flow makes", id)
	}
	assert.Empty(t, byID["d-fine"].AdapterError)
	assert.Equal(t, "bepinex", byID["d-fine"].EffectiveAdapter)
}
