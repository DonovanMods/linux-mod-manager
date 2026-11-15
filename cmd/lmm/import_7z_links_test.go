package main

// #658: `lmm import` of a .7z holding a symbolic link is refused before
// anything is written - the plan's listing refuses the link member, as the
// extraction does.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoImport_7zWithASymlinkMemberIsRefused(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	svc, game := setupDoImportTest(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "mymod.esp"), []byte("mod bytes"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(src, "link.txt")))
	archive := filepath.Join(dir, "MyMod-1.0.7z")
	cmd := exec.Command("7z", "a", "-snl", archive, ".")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fixture .7z: %s", out)

	for _, dryRun := range []bool{true, false} {
		importDryRun = dryRun
		_, _, runErr := captureStdoutStderrErr(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archive})
		})
		require.Error(t, runErr, "dry run %v", dryRun)
		assert.Contains(t, runErr.Error(), "link.txt")
		assert.Contains(t, runErr.Error(), "is a link")
	}

	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing deployed")
	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, mods, "nothing recorded")
}
