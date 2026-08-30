package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockChecksumUpdates opens a second connection to dbPath and installs a
// trigger that aborts every UPDATE of installed_mod_files.checksum, forcing
// core.ApplyInstall's shared BATCH engine (applyInstallBatchMod,
// internal/core/install.go) to emit InstallChecksumSaveFailed
// deterministically - mirroring internal/core's own
// TestService_ApplyInstall_ChecksumSaveFailure_WarningNotDoublePrefixed.
// InstallChecksumSaveFailed only fires from that shared engine (see its doc
// comment in internal/core/install.go), and both frontends that reach it -
// doInstallBatch (the dependency path) and installMultipleMods (the
// multi-select path) - word the SAME event differently, which is what the
// two tests below pin (review Minor M3: this phase had no test on either
// frontend).
func blockChecksumUpdates(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_checksum_updates
		BEFORE UPDATE OF checksum ON installed_mod_files
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// checksumWarningLine returns the one line in output that reports the
// checksum-save failure, so a test can assert its exact prefix - the whole
// reason InstallChecksumSaveFailed is a distinct phase from InstallWarning:
// one frontend indents it, the other prints it flush.
func checksumWarningLine(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "failed to save checksum") {
			return line
		}
	}
	t.Fatalf("no checksum-save-failure line in output:\n%s", output)
	return ""
}

// TestInstallMultipleMods_ChecksumSaveFailure_PrintsIndentedWarning covers
// review Minor M3 for the multi-select path: installMultipleMods's
// InstallChecksumSaveFailed arm prints "  Warning: %s" (indented) - unlike
// doInstallBatch's flush "Warning: %s" (see
// TestDoInstallBatch_ChecksumSaveFailure_PrintsFlushWarning) - and a later
// "cleanup" that unified the two arms would break this silently.
func TestInstallMultipleMods_ChecksumSaveFailure_PrintsIndentedWarning(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	mod := &domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"}
	src.AddMod(mod, []domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("content"))

	blockChecksumUpdates(t, filepath.Join(dataDir, "lmm.db"))

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return installMultipleMods(context.Background(), svc, game, []*domain.Mod{mod}, "default")
	})
	require.NoError(t, err, "a checksum-save failure must not fail the whole install")

	line := checksumWarningLine(t, stderr)
	assert.True(t, strings.HasPrefix(line, "  Warning: failed to save checksum: "), "got: %q", line)
	assert.Contains(t, line, "blocked for test")

	assert.Contains(t, stdout, "✓ Installed (1 files)\n", "the checksum-save failure is non-fatal - the mod still installs")
}

// TestDoInstallBatch_ChecksumSaveFailure_PrintsFlushWarning is
// TestInstallMultipleMods_ChecksumSaveFailure_PrintsIndentedWarning's
// dependency-path counterpart (review Minor M3): doInstallBatch renders the
// SAME InstallChecksumSaveFailed event flush, with no indent.
func TestDoInstallBatch_ChecksumSaveFailure_PrintsFlushWarning(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)

	dep := &domain.Mod{ID: "dep1", SourceID: "test-src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "test-src", ModID: "dep1"}}}
	src.AddMod(dep, []domain.DownloadableFile{{ID: "dep-file", FileName: "dep1.esp", IsPrimary: true}})
	src.AddDownload("dep-file", []byte("dep content"))
	src.AddMod(root, []domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("root content"))

	blockChecksumUpdates(t, filepath.Join(dataDir, "lmm.db"))

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return doInstall(context.Background(), svc, game, nil)
	})
	require.NoError(t, err, "a checksum-save failure must not fail the whole install")

	line := checksumWarningLine(t, stderr)
	assert.True(t, strings.HasPrefix(line, "Warning: failed to save checksum: "), "got: %q", line)
	assert.False(t, strings.HasPrefix(line, "  Warning:"), "the dependency path prints flush, not indented")
	assert.Contains(t, line, "blocked for test")

	assert.Contains(t, stdout, "\n--- Summary ---\nInstalled: 2\n", "the checksum-save failure is non-fatal - both mods still install")
}
