package core_test

// #476: when a deploy leaves a file because another game records it
// (#473), core queues a "held path" line for the flow to report. `lmm
// import <archive>` never drained that queue, so its user was not told, and
// the line surfaced later on an unrelated command. The import reports what
// it caused, once.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportArchive_ReportsAFileKeptForAnotherGame(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sky := newLegacyFixture(t, handoffGame(t, "sky", root, domain.LinkCopy))
	sky2 := &legacyFixture{svc: sky.svc, game: handoffGame(t, "sky2", root, domain.LinkCopy)}
	require.NoError(t, sky.svc.SaveGame(ctx, sky2.game))
	sky.profile(t, "default", true)
	sky2.profile(t, "default", true, "j")
	sky2.deployed(t, "default", "j", domain.LinkCopy, map[string]string{"Data/a.esp": "sky2 a"}, nil)
	live := filepath.Join(sky.game.ModPath, "Data", "a.esp")

	archive := filepath.Join(t.TempDir(), "Mine-1.0.zip")
	createImportTestZip(t, archive, map[string]string{"Data/a.esp": "sky a", "Data/b.esp": "sky b"})
	plan, err := sky.svc.PlanImportArchive(ctx, sky.game, "default", archive, core.ImportArchiveOptions{})
	require.NoError(t, err)
	sink, events := core.RecordEvents()

	result, err := sky.svc.ApplyImportArchive(ctx, sky.game, "default", plan, core.ImportArchiveOptions{}, sink)

	require.NoError(t, err)
	want := "Data/a.esp was not replaced: game sky2 records it too, so lmm left that game's file there"
	assert.Equal(t, []string{want}, result.Warnings, "reported once, on the import's own result")
	assert.Equal(t, "sky2 a", readLive(t, live), "sky2's file is untouched")
	var emitted int
	for _, e := range *events {
		if w, ok := e.(core.WarningEvent); ok && w.Message == want {
			emitted++
		}
	}
	assert.Equal(t, 1, emitted, "and as one live warning")

	// Nothing is left for the next command to report.
	disable, err := sky.svc.DisableMod(ctx, sky.game, "default", plan.Mod.SourceID, plan.Mod.ID)
	require.NoError(t, err)
	assert.NotContains(t, disable.Warnings, want)
	_, err = os.Stat(live)
	require.NoError(t, err)
}
