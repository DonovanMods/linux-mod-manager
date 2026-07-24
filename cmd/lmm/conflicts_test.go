package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupConflictsTest builds a *core.Service plus a game and resets the
// conflicts command's flag globals, following setupDoDeployTest's pattern.
// Callers seed their own mods/profile.
func setupConflictsTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	oldProfile, oldJSON := conflictsProfile, jsonOutput
	conflictsProfile = ""
	jsonOutput = false
	t.Cleanup(func() { conflictsProfile, jsonOutput = oldProfile, oldJSON })

	return svc, game
}

// seedConflictMod installs modID/name as enabled with the given cached files
// and appends it to the "default" profile's load order.
func seedConflictMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name string, enabled bool, files map[string][]byte) {
	t.Helper()

	for path, content := range files {
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", modID, "1.0", path, content))
	}
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

// seedTwinConflictFixture seeds two mods ("Mod A" first in profile order,
// "Mod B" second) that both provide "shared.esp" and deploys the profile, so
// Mod B (last in load order, deploys last per Task 1) owns the shared path.
func seedTwinConflictFixture(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	seedConflictMod(t, svc, game, "a", "Mod A", true, map[string][]byte{"shared.esp": []byte("A-content")})
	seedConflictMod(t, svc, game, "b", "Mod B", true, map[string][]byte{"shared.esp": []byte("B-content")})
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
}

// --- Byte-identical baselines (format unchanged from the pre-extraction CLI) ---

// TestDoConflicts_NoInstalledMods_TextAndJSON pins the empty-mods outputs,
// which must remain byte-identical through the extraction.
func TestDoConflicts_NoInstalledMods_TextAndJSON(t *testing.T) {
	svc, game := setupConflictsTest(t)

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})
	assert.Equal(t, "No installed mods.\n", out)

	jsonOutput = true
	out = captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})
	assert.Equal(t, "{\n  \"game_id\": \"g1\",\n  \"profile\": \"default\",\n  \"conflicts\": []\n}\n", out)
}

// TestDoConflicts_NoConflicts_TextAndJSON pins the installed-but-conflict-free
// outputs, which must also remain byte-identical.
func TestDoConflicts_NoConflicts_TextAndJSON(t *testing.T) {
	svc, game := setupConflictsTest(t)
	seedConflictMod(t, svc, game, "a", "Mod A", true, map[string][]byte{"a-only.esp": []byte("A")})
	seedConflictMod(t, svc, game, "b", "Mod B", true, map[string][]byte{"b-only.esp": []byte("B")})
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})
	assert.Equal(t, "No conflicts found.\n", out)

	jsonOutput = true
	out = captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})
	assert.Equal(t, "{\n  \"game_id\": \"g1\",\n  \"profile\": \"default\",\n  \"conflicts\": []\n}\n", out)
}

// --- Populated captures (detection fixed + declared additive changes) ---

// TestDoConflicts_TwinConflict_Text pins the full text output for a real
// conflict: every pre-extraction line byte-identical, plus the additive
// "    Winner:" line after "Also in:". Owner and winner agree (the profile
// was deployed in its current order) so no stale suffix appears.
func TestDoConflicts_TwinConflict_Text(t *testing.T) {
	svc, game := setupConflictsTest(t)
	seedTwinConflictFixture(t, svc, game)

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})

	assert.Equal(t,
		"Found 1 conflicting file(s):\n\n"+
			"  shared.esp\n"+
			"    Owner: Mod B\n"+
			"    Also in: Mod A\n"+
			"    Winner: Mod B\n"+
			"\n",
		out)
}

// TestDoConflicts_TwinConflict_Stale_Text: reordering the profile without
// redeploying flips the load-order winner to Mod A while the DB owner stays
// Mod B - the Winner line gains the stale suffix.
func TestDoConflicts_TwinConflict_Stale_Text(t *testing.T) {
	svc, game := setupConflictsTest(t)
	seedTwinConflictFixture(t, svc, game)

	require.NoError(t, svc.NewProfileManager().ReorderMods(game.ID, "default", []domain.ModReference{
		{SourceID: "src", ModID: "b", Version: "1.0"},
		{SourceID: "src", ModID: "a", Version: "1.0"},
	}))

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})

	assert.Equal(t,
		"Found 1 conflicting file(s):\n\n"+
			"  shared.esp\n"+
			"    Owner: Mod B\n"+
			"    Also in: Mod A\n"+
			"    Winner: Mod A (stale — redeploy to apply)\n"+
			"\n",
		out)
}

// TestDoConflicts_TwinConflict_JSON pins the full JSON bytes: the
// pre-extraction fields in their exact positions plus the additive "winner"
// and "stale" fields.
func TestDoConflicts_TwinConflict_JSON(t *testing.T) {
	svc, game := setupConflictsTest(t)
	seedTwinConflictFixture(t, svc, game)
	jsonOutput = true

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})

	assert.Equal(t,
		"{\n"+
			"  \"game_id\": \"g1\",\n"+
			"  \"profile\": \"default\",\n"+
			"  \"conflicts\": [\n"+
			"    {\n"+
			"      \"path\": \"shared.esp\",\n"+
			"      \"owner\": \"Mod B\",\n"+
			"      \"also_in\": [\n"+
			"        \"Mod A\"\n"+
			"      ],\n"+
			"      \"winner\": \"Mod B\",\n"+
			"      \"stale\": false\n"+
			"    }\n"+
			"  ]\n"+
			"}\n",
		out)
}

// TestDoConflicts_SortedByPath: multiple conflicts must list in Path order -
// the declared fix for the pre-extraction map-random ordering.
func TestDoConflicts_SortedByPath(t *testing.T) {
	svc, game := setupConflictsTest(t)
	shared := func(tag string) map[string][]byte {
		return map[string][]byte{
			"zebra.esp": []byte(tag),
			"alpha.esp": []byte(tag),
		}
	}
	seedConflictMod(t, svc, game, "a", "Mod A", true, shared("A"))
	seedConflictMod(t, svc, game, "b", "Mod B", true, shared("B"))
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})

	assert.Equal(t,
		"Found 2 conflicting file(s):\n\n"+
			"  alpha.esp\n"+
			"    Owner: Mod B\n"+
			"    Also in: Mod A\n"+
			"    Winner: Mod B\n"+
			"\n"+
			"  zebra.esp\n"+
			"    Owner: Mod B\n"+
			"    Also in: Mod A\n"+
			"    Winner: Mod B\n"+
			"\n",
		out)
}
