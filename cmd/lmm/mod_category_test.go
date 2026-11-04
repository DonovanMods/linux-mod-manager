package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withSetCategoryClear sets `lmm mod set-category --clear` for one test.
func withSetCategoryClear(t *testing.T, clear bool) {
	t.Helper()
	old := modSetCategoryClear
	modSetCategoryClear = clear
	t.Cleanup(func() { modSetCategoryClear = old })
}

// profileYAML is the default profile's file as written.
func profileYAML(t *testing.T, game *domain.Game) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "games", game.ID, "profiles", "default.yaml"))
	require.NoError(t, err)
	return string(data)
}

// TestDoModSetCategory_SetThenClearRoundTrip: the category lands on the
// profile ref, and --clear takes it off again - the file reads as if it
// had never been set (#592).
func TestDoModSetCategory_SetThenClearRoundTrip(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	before := profileYAML(t, game)

	withSetCategoryClear(t, false)
	out := captureStdout(t, func() error {
		return doModSetCategory(context.Background(), svc, game, "a", "  Gameplay  ")
	})
	assert.Equal(t, "✓ Mod A category: Gameplay\n", out)
	assert.Contains(t, profileYAML(t, game), "category: Gameplay")

	withSetCategoryClear(t, true)
	out = captureStdout(t, func() error {
		return doModSetCategory(context.Background(), svc, game, "a", "")
	})
	assert.Equal(t, "✓ Mod A category cleared\n", out)
	assert.Equal(t, before, profileYAML(t, game))
}

// TestDoModSetCategory_ClearNamesTheSourceCategory: after a clear the mod
// falls back to its source's category, and the message says which.
func TestDoModSetCategory_ClearNamesTheSourceCategory(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	mod, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	mod.CategoryName = "Weapons"
	require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))
	require.NoError(t, svc.NewProfileManager().SetModCategory(context.Background(), game.ID, "default", "src", "a", "Mine"))

	withSetCategoryClear(t, true)
	out := captureStdout(t, func() error {
		return doModSetCategory(context.Background(), svc, game, "a", "")
	})
	assert.Equal(t, "✓ Mod A category cleared (source category: Weapons)\n", out)
}

// TestDoModSetCategory_ClearWithNothingSet: --clear on a mod with no
// category of the user's says so, rather than "cleared".
func TestDoModSetCategory_ClearWithNothingSet(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")

	withSetCategoryClear(t, true)
	out := captureStdout(t, func() error {
		return doModSetCategory(context.Background(), svc, game, "a", "")
	})
	assert.Equal(t, "✓ Mod A has no category of yours to clear\n", out)
}

// TestDoModSetCategory_NotInstalled: the siblings' "mod not found".
func TestDoModSetCategory_NotInstalled(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	withSetCategoryClear(t, false)

	err := doModSetCategory(context.Background(), svc, game, "nope", "Gameplay")
	assert.EqualError(t, err, "mod not found: nope")
}

// TestValidateModSetCategoryArgs covers the usage errors, raised before the
// service opens: exactly one of a name or --clear, and a name core accepts.
func TestValidateModSetCategoryArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		clear   bool
		wantErr string
		isCat   bool
	}{
		{name: "name", args: []string{"a", "Gameplay"}},
		{name: "clear", args: []string{"a"}, clear: true},
		{name: "neither", args: []string{"a"}, wantErr: "specify a category name, or --clear to remove it"},
		{name: "both", args: []string{"a", "Gameplay"}, clear: true, wantErr: "specify a category name or --clear, not both"},
		{name: "blank name", args: []string{"a", "   "}, wantErr: "the category name is empty; use --clear to remove it"},
		{name: "too long", args: []string{"a", strings.Repeat("x", core.MaxCategoryLength+1)}, isCat: true},
		{name: "control character", args: []string{"a", "Game\nplay"}, isCat: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSetCategoryClear(t, tt.clear)
			err := validateModSetCategoryArgs(tt.args)
			switch {
			case tt.isCat:
				require.Error(t, err)
				assert.ErrorIs(t, err, core.ErrInvalidCategory)
			case tt.wantErr != "":
				assert.EqualError(t, err, tt.wantErr)
			default:
				assert.NoError(t, err)
			}
		})
	}
}

// TestModShow_InstalledCategoryLine: the installed block names the
// effective category and where it came from.
func TestModShow_InstalledCategoryLine(t *testing.T) {
	tests := []struct {
		name       string
		sourceName string
		user       string
		want       string
	}{
		{name: "user", sourceName: "Weapons", user: "Mine", want: "  Category: Mine (yours)\n"},
		{name: "source", sourceName: "Weapons", want: "  Category: Weapons (from the source)\n"},
		// No category, no line: a mod show of an uncategorised mod reads
		// as it did before #592.
		{name: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, game, src := setupDoModLockTest(t)
			seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
			src.AddMod(richMod(game.ID), nil)
			if tt.sourceName != "" {
				mod, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
				require.NoError(t, err)
				mod.CategoryName = tt.sourceName
				require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))
			}
			if tt.user != "" {
				require.NoError(t, svc.NewProfileManager().SetModCategory(context.Background(), game.ID, "default", "src", "a", tt.user))
			}

			out := captureStdout(t, func() error {
				return doModShow(context.Background(), svc, game, "a")
			})
			if tt.want == "" {
				assert.NotContains(t, out, "  Category:")
				return
			}
			assert.Contains(t, out, tt.want)
		})
	}
}
