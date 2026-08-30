package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplySingleUpdate_SuccessCheckmark_ColorPath extends update.go's
// existing "✓ Updated: ..." success line with the same colorGreen("✓")
// convention deploy.go/verify.go already use.
func TestApplySingleUpdate_SuccessCheckmark_ColorPath(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	resetColorFlags(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, colorGreen("✓")+" Updated: Mod One 1.0 → 2.0")
}

// TestDoUpdateRollback_SuccessCheckmark_ColorPath mirrors the above for the
// rollback footer.
func TestDoUpdateRollback_SuccessCheckmark_ColorPath(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))
	require.NoError(t, captureStdoutOnlyErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	}))

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	assert.Contains(t, out, colorGreen("✓")+" Rolled back: Mod One 2.0 → 1.0")
}

// TestDoUpdate_Table_ColorPath guards the update-available table: header
// bolded, the last column (POLICY, never padded by tabwriter - see
// printTable's doc comment) tinted per row, and the summary line accented -
// all without perturbing the plain-mode column alignment.
func TestDoUpdate_Table_ColorPath(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	game.SourceIDs = map[string]string{"test-src": "g1"}
	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", nil, map[string][]byte{"mod1.esp": []byte("data")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"}, nil)

	resetColorFlags(t)
	withColorCapableStdout(t, false)
	plain := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})
	assert.NotContains(t, plain, "\x1b[")
	assert.Contains(t, plain, "1 update(s) available.")

	withColorCapableStdout(t, true)
	colored := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, colored, ansiBold, "table header should be bolded")
	assert.Contains(t, colored, ansiYellow+"1 update(s) available."+ansiReset, "the available-updates summary should be accented")
	assert.Equal(t, plain, stripANSI(colored), "color must not change the visible text or alignment")
}
