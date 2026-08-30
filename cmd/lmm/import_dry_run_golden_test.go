package main

// Plain-text goldens for `lmm import <archive> --dry-run` (#314, R-B5).
//
// The transcript pins the whole preview - the "Importing:" announcement, the
// readout core.EmitImportArchiveReadout renders from the plan, and
// renderImportArchivePlan's "Would import:" block - plus the end state, so
// "a dry run changes nothing" is recorded rather than assumed. Same shape as
// deploy_dry_run_golden_test.go's transcripts (## stdout / ## stderr /
// ## error / ## tree), and the same one-flag rule: re-record ONLY with
// -update-import-dry-run.

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateImportDryRunGoldens = flag.Bool("update-import-dry-run", false,
	"rewrite `lmm import <archive> --dry-run` goldens from current output")

// importDryRunFixture is one scenario: a seeded service/game plus the archive
// doImport is called with. The import flag globals are set by the fixture
// itself (setupDoImportTest / setupDoImportCompileTest reset them all).
type importDryRunFixture struct {
	svc     *core.Service
	game    *domain.Game
	archive string
}

// runImportDryRunGolden drives doImport with --dry-run for one scenario and
// compares (or records) the transcript, asserting no side effect either way.
func runImportDryRunGolden(t *testing.T, name string, setup func(t *testing.T) importDryRunFixture) {
	t.Helper()
	fx := setup(t)
	importDryRun = true

	beforeGame := dumpTree(t, fx.game.ModPath)
	beforeCache := dumpTree(t, fx.svc.GetGameCachePath(fx.game))
	beforeMods, err := fx.svc.GetInstalledMods(context.Background(), fx.game.ID, "default")
	require.NoError(t, err)

	stdout, stderr, runErr := captureStdoutStderrErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, fx.svc, fx.game, []string{fx.archive})
	})

	errText := "<nil>"
	if runErr != nil {
		errText = runErr.Error()
	}

	afterGame := dumpTree(t, fx.game.ModPath)
	afterMods, err := fx.svc.GetInstalledMods(context.Background(), fx.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, beforeGame, afterGame, "a dry run must not touch the game directory")
	assert.Equal(t, beforeCache, dumpTree(t, fx.svc.GetGameCachePath(fx.game)),
		"a dry run must not touch the cache directory - the archive is LISTED, never ingested")
	assert.Equal(t, beforeMods, afterMods, "a dry run must not touch the installed-mods DB rows")

	// The archive lives in a per-run t.TempDir(), and an unlinked import
	// mints a fresh uuid per run, so both are scrubbed - the same
	// volatileUUID rule the --json goldens use, keeping the KEY pinned while
	// dropping the value.
	scrub := func(s string) string {
		s = strings.ReplaceAll(s, filepath.Dir(fx.archive), "<ARCHIVE-DIR>")
		return volatileUUID.ReplaceAllString(s, "<UUID>")
	}

	var buf bytes.Buffer
	buf.WriteString("## stdout\n")
	buf.WriteString(scrub(stdout))
	buf.WriteString("## stderr\n")
	buf.WriteString(scrub(stderr))
	buf.WriteString("## error\n")
	buf.WriteString(scrub(errText))
	buf.WriteString("\n## tree\n")
	buf.WriteString(afterGame)

	path := filepath.Join("testdata", "import_dry_run_golden", name+".golden")
	if *updateImportDryRunGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - record it with -update-import-dry-run")
	require.Equal(t, string(want), buf.String(), "`lmm import <archive> --dry-run` output drifted from the recorded golden")
}

func TestImportArchiveDryRunGoldens(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) importDryRunFixture
	}{
		{
			// A plain source-linked archive: the readout, the file count and
			// the profile line.
			"plain",
			func(t *testing.T) importDryRunFixture {
				svc, game := setupDoImportTest(t)
				src := newFakeMatchSource("acme-source")
				src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: game.ID}
				svc.RegisterSource(src)
				game.SourceIDs = map[string]string{"acme-source": game.ID}
				importModID, importSource = "999", "acme-source"

				archive := filepath.Join(t.TempDir(), "mymod.zip")
				createTestArchive(t, archive, map[string]string{"MyMod/mymod.esp": "data"})
				return importDryRunFixture{svc: svc, game: game, archive: archive}
			},
		},
		{
			// --verbose expands both lists (files and conflicts).
			"conflict_verbose",
			func(t *testing.T) importDryRunFixture {
				svc, game, archiveB := setupImportConflictTest(t)
				setVerboseForTest(t, true)
				return importDryRunFixture{svc: svc, game: game, archive: archiveB}
			},
		},
		{
			// A DeployCompile native merge source: zero files of its own,
			// and the merged artifact would be resynced afterwards.
			"compile_merge_source",
			func(t *testing.T) importDryRunFixture {
				svc, game, _ := setupDoImportCompileTest(t)
				archive := filepath.Join(t.TempDir(), "Bear_Mount.exmodz")
				require.NoError(t, os.WriteFile(archive, []byte("fake-exmodz-bytes"), 0o644))
				return importDryRunFixture{svc: svc, game: game, archive: archive}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runImportDryRunGolden(t, tt.name, tt.setup) })
	}
}
