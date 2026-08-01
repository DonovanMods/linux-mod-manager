package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoModShow_ColorPath_PlainByDefault is the byte-stability guard: with
// color off (the default), mod show output must carry no ANSI escapes,
// whether or not the mod is locked.
func TestDoModShow_ColorPath_PlainByDefault(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", "1.2.3"))

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.NotContains(t, out, "\x1b[")
}

// TestDoModShow_ColorPath_NameBolded_LockAccented: the mod's name banner is
// bolded, and a lock (a held-back/pending state) is accented yellow -
// matching the repo's established "pending"=yellow mapping.
func TestDoModShow_ColorPath_NameBolded_LockAccented(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", "1.2.3"))

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, colorBold("Mod A"))
	assert.Contains(t, out, colorYellow("locked at v1.2.3 — run 'lmm profile apply' to converge"))
}

// TestDoModShow_ColorPath_PinnedPolicyAccented guards the "pinned" update
// policy - the issue's own example of a yellow/pending accent.
func TestDoModShow_ColorPath_PinnedPolicyAccented(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.SetModUpdatePolicy("src", "a", game.ID, "default", domain.UpdatePinned))

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Update policy: "+colorYellow("pinned"))
}
