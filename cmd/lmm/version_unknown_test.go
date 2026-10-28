package main

// #459: an archive whose name carries no version is imported at the
// "unknown" placeholder. The placeholder stays on the wire, but no CLI
// line labels it as a version ("vunknown"), and a mod with no author has
// no "Author:" label with nothing after it.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importVersionless runs `lmm import` on an archive named without a
// version and returns the imported mod's id.
func importVersionless(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoImportTest(t)
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err := getProfileManager(svc).Create(ctx, game.ID, "default")
	require.NoError(t, err)

	archivePath := filepath.Join(t.TempDir(), "Rooted.zip")
	createTestArchive(t, archivePath, map[string]string{"rooted.esp": "data"})
	_, _, err = captureStdoutAndStderr(t, func() error {
		return doImport(ctx, &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)

	rows, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, core.VersionUnknown, rows[0].Version, "the placeholder stays what lmm records")
	require.Empty(t, rows[0].Author)

	oldSource, oldProfile := modSource, modProfile
	oldAuto, oldNotify, oldPin := modSetAuto, modSetNotify, modSetPin
	t.Cleanup(func() {
		modSource, modProfile = oldSource, oldProfile
		modSetAuto, modSetNotify, modSetPin = oldAuto, oldNotify, oldPin
	})
	modSource, modProfile = "", "default"
	modSetAuto, modSetNotify, modSetPin = false, false, false
	return svc, game, rows[0].ID
}

func TestModShowGolden_VersionlessImport(t *testing.T) {
	svc, game, id := importVersionless(t)

	out := captureStdout(t, func() error { return doModShow(context.Background(), svc, game, id) })
	assert.NotContains(t, out, "vunknown")
	assert.NotContains(t, out, "Author:")
	assertModShowGolden(t, "versionless_import", strings.ReplaceAll(out, id, "<ID>"))
}

func TestModSetUpdatePinGolden_VersionlessImport(t *testing.T) {
	svc, game, id := importVersionless(t)
	modSetPin = true

	out := captureStdout(t, func() error { return doModSetUpdate(context.Background(), svc, game, id) })
	assert.NotContains(t, out, "vunknown")
	assertModShowGolden(t, "versionless_set_update_pin", out)
}
