package core_test

// #621's import gates (owner rulings 2026-11-12, S5/S6): a document's hooks
// are saved only with WithHooks, and its overrides that do not target a
// text config file only with AllowAnyOverrides - for a version-0 document
// exactly as for a version-1 collection.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

const gatedV0 = `name: shared
game_id: cyber
mods: []
overrides:
  engine/config/user.ini: "[A]\nb=1\n"
  Config/Game.INI: "[B]\n"
  bin/x64/version.dll: "MZ..."
`

const gatedV1 = `lmm_collection: 1
name: shared
mods: []
overrides:
  engine/config/user.ini: "[A]\nb=1\n"
  Config/Game.INI: "[B]\n"
  bin/x64/version.dll: "MZ..."
`

func planGated(t *testing.T, svc *core.Service, game *domain.Game, v1 bool) *core.ImportPlan {
	t.Helper()
	if v1 {
		plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(gatedV1), GameID: game.ID})
		require.NoError(t, err)
		return plan
	}
	plan, err := svc.PlanImport(context.Background(), game, []byte(gatedV0))
	require.NoError(t, err)
	return plan
}

func applyGated(t *testing.T, svc *core.Service, game *domain.Game, plan *core.ImportPlan, opts core.ProfileImportOptions) *core.ProfileImportResult {
	t.Helper()
	opts.NoInstall = true
	var result *core.ProfileImportResult
	var err error
	if plan.Collection != nil {
		result, err = svc.ApplyCollectionImport(context.Background(), game, plan, opts, nil)
	} else {
		result, err = svc.ApplyImport(context.Background(), game, plan, opts, nil)
	}
	require.NoError(t, err)
	return result
}

func TestImportGates_OverridesOtherThanTextConfigAreSkippedByDefault(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		name := map[bool]string{false: "version 0", true: "version 1"}[v1]
		t.Run(name, func(t *testing.T) {
			svc, game := recipient(t)
			plan := planGated(t, svc, game, v1)
			require.Len(t, plan.RestrictedOverrides, 1, "the plan lists the .dll")
			require.Equal(t, "bin/x64/version.dll", plan.RestrictedOverrides[0].Path)
			require.Contains(t, plan.RestrictedOverrides[0].Reason, "--allow-any-overrides")

			result := applyGated(t, svc, game, plan, core.ProfileImportOptions{})
			require.Equal(t, plan.RestrictedOverrides, result.OverridesSkipped, "the result lists what it skipped")
			saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "shared")
			require.NoError(t, err)
			require.Contains(t, saved.Overrides, "engine/config/user.ini", "a text config override is always imported")
			require.Contains(t, saved.Overrides, "Config/Game.INI", "extensions match case-insensitively")
			require.NotContains(t, saved.Overrides, "bin/x64/version.dll")
		})
	}
}

func TestImportGates_AllowAnyOverridesIncludesThem(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		name := map[bool]string{false: "version 0", true: "version 1"}[v1]
		t.Run(name, func(t *testing.T) {
			svc, game := recipient(t)
			plan := planGated(t, svc, game, v1)
			result := applyGated(t, svc, game, plan, core.ProfileImportOptions{AllowAnyOverrides: true})
			require.Empty(t, result.OverridesSkipped)
			saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "shared")
			require.NoError(t, err)
			require.Equal(t, "MZ...", string(saved.Overrides["bin/x64/version.dll"]))
			require.Len(t, saved.Overrides, 3)
		})
	}
}

func TestImportGates_EscapingOverridesAreRefusedWhateverTheExtension(t *testing.T) {
	svc, game := recipient(t)
	for _, key := range []string{"../../../.bashrc", "../evil.ini", "/etc/x.cfg"} {
		doc := "name: shared\ngame_id: cyber\nmods: []\noverrides:\n  " + key + ": x\n"
		_, err := svc.PlanImport(context.Background(), game, []byte(doc))
		require.ErrorIs(t, err, core.ErrUnsafeImport, key)
	}
	doc := "name: shared\ngame_id: cyber\nmods: []\nhooks:\n  install:\n    after_all: \"/tmp/evil.sh\\e[2K\\r    - install.after_all: /usr/bin/true\"\n"
	_, err := svc.PlanImport(context.Background(), game, []byte(doc))
	require.ErrorIs(t, err, core.ErrUnsafeImport, "a version-0 hook that hides itself is refused as a collection's is")
}

func TestImportGates_AProfileWithoutHooksOrOverridesIsUnchanged(t *testing.T) {
	svc, game := recipient(t)
	plan, err := svc.PlanImport(context.Background(), game, []byte("name: shared\ngame_id: cyber\nmods: []\n"))
	require.NoError(t, err)
	require.Empty(t, plan.Hooks)
	require.Empty(t, plan.RestrictedOverrides)
	result := applyGated(t, svc, game, plan, core.ProfileImportOptions{})
	require.Zero(t, result.HooksWithheld)
	require.Empty(t, result.OverridesSkipped)
}

// TestImportGates_TextConfigExtensions is the owner's list of text config
// file types an imported override may target without
// --allow-any-overrides, matched case-insensitively, and a few that are
// code and stay held back.
func TestImportGates_TextConfigExtensions(t *testing.T) {
	svc, game := recipient(t)
	cases := []struct {
		path       string
		restricted bool
	}{
		{"a.ini", false}, {"a.cfg", false}, {"a.conf", false}, {"a.txt", false},
		{"a.json", false}, {"a.xml", false}, {"a.toml", false}, {"a.yaml", false},
		{"a.yml", false}, {"server.properties", false}, {"app.config", false},
		{"system.ltx", false}, {"settings.lsx", false}, {"game.cvars", false},
		{"config.ron", false}, {"UPPER.PROPERTIES", false}, {"Mixed.Ltx", false},
		{"version.dll", true}, {"game.exe", true}, {"lib.so", true},
		{"init.lua", true}, {"run.sh", true}, {"noextension", true},
	}
	for _, tc := range cases {
		doc := "name: shared\ngame_id: cyber\nmods: []\noverrides:\n  " + tc.path + ": x\n"
		plan, err := svc.PlanImport(context.Background(), game, []byte(doc))
		require.NoError(t, err, tc.path)
		if tc.restricted {
			require.Len(t, plan.RestrictedOverrides, 1, tc.path)
		} else {
			require.Empty(t, plan.RestrictedOverrides, tc.path)
		}
	}
	require.Len(t, core.TextConfigOverrideExtensions(), 15)
}
