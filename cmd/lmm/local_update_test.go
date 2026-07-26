package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedLocalMod seeds an imported-from-disk mod: present in the profile, but
// with no remote source, so CheckUpdates filters it out.
func seedLocalMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name string) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, domain.SourceLocal, modID, "1.0", name+".esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: domain.SourceLocal, Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: domain.SourceLocal, ModID: modID, Version: "1.0"}))
}

func localUpdateGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "src"}
	return svc, game
}

// TestDoUpdate_AllLocal_DoesNotClaimUpToDate: local mods are filtered before
// any source is queried, so "All mods are up to date" describes a check that
// never happened.
func TestDoUpdate_AllLocal_DoesNotClaimUpToDate(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "up to date", "nothing was checked, so nothing can be up to date")
	assert.Contains(t, out, "local", "should say the mods were skipped as local")
}

// TestDoUpdate_ReportsSkippedLocalCount: mixed profile — the checkable mod is
// reported normally, and the local one is disclosed rather than vanishing.
func TestDoUpdate_ReportsSkippedLocalCount(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedLocalMod(t, svc, game, "localA", "Local A")

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "1 local mod", "should report the skipped local mod")
}

// TestDoUpdate_SingleLocalMod_SaysLocalNotNotFound: the lookup matched on
// source as well as ID, so a local mod reported "not found in profile" — with
// exit 1 — for a mod that is plainly in the profile.
func TestDoUpdate_SingleLocalMod_SaysLocalNotNotFound(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	var err error
	out := captureStdout(t, func() error {
		err = doUpdate(context.Background(), svc, game, []string{"localA"})
		return nil
	})

	assert.NoError(t, err, "a present-but-uncheckable mod is not an error")
	assert.NotContains(t, out, "not found", "the mod is in the profile")
	assert.Contains(t, out, "local", "should say why it cannot be checked")
}

// TestDoUpdate_SingleUnknownMod_StillErrors guards the other direction: a mod
// genuinely absent from the profile must still be an error.
func TestDoUpdate_SingleUnknownMod_StillErrors(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	err := doUpdate(context.Background(), svc, game, []string{"nosuchmod"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestDoUpdate_JSONReportsSkipped: --json consumers have the same blind spot.
func TestDoUpdate_JSONReportsSkipped(t *testing.T) {
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	setPolicy(t, svc, game, "b", domain.UpdatePinned)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var out struct {
		Skipped struct {
			Pinned int `json:"pinned"`
			Local  int `json:"local"`
		} `json:"skipped"`
	}
	raw := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	assert.Equal(t, 1, out.Skipped.Local)
	assert.Equal(t, 1, out.Skipped.Pinned)
}
