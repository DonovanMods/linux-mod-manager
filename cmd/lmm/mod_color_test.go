package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
)

// TestDoModSetUpdate_SuccessCheckmark_ColorPath and
// TestDoModLock_SuccessCheckmark_ColorPath extend mod.go's existing "✓ ..."
// success lines with the same colorGreen("✓") convention deploy.go/verify.go
// already use - see update.go's identical extension for the update command.
func TestDoModSetUpdate_SuccessCheckmark_ColorPath(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	modSetAuto = true
	t.Cleanup(func() { modSetAuto = false })

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doModSetUpdate(svc, game, "a")
	})

	assert.Contains(t, out, colorGreen("✓")+" Mod A update policy: auto")
}

func TestDoModLock_SuccessCheckmark_ColorPath(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID}, []domain.DownloadableFile{
		{ID: "f1", Version: "1.0", Category: "MAIN"},
	})

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doModLock(context.Background(), svc, game, "a", "")
	})

	assert.Contains(t, out, colorGreen("✓")+" Mod A locked at v1.0")
}
