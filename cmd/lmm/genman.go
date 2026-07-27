package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// genManDate is a pinned generation date rather than time.Now(): the drift
// test in genman_test.go byte-compares a fresh regeneration against
// docs/man/man1, which only works if regenerating without a help-text change
// produces identical output. Bump this deliberately (and re-run `make man`)
// when cutting a release that regenerates the pages.
var genManDate = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

// defaultManDir is where `make man` and the goreleaser build expect the
// generated pages to live.
const defaultManDir = "docs/man/man1"

var genManCmd = &cobra.Command{
	Use:   "gen-man [dir]",
	Short: "Generate man pages from the command tree",
	Long: `Generate man pages for every visible command in the tree, one file per
command (e.g. lmm.1, lmm-install.1, lmm-game-add.1), written to dir
(default: docs/man/man1). Hidden commands, including this one, get no page.

This is the tooling behind 'make man'; it is not meant to be run directly
by end users.`,
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := defaultManDir
		if len(args) > 0 {
			dir = args[0]
		}
		return genManTree(dir)
	},
}

func init() {
	rootCmd.AddCommand(genManCmd)
}

// genManTree generates man pages for the full rootCmd tree into dir,
// creating dir if it doesn't already exist.
func genManTree(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating man page directory: %w", err)
	}

	// Cobra normally attaches the default "completion" subcommand tree
	// lazily, the first time ExecuteC() runs. Force it here so the
	// generated set is identical whether genManTree is reached via the
	// CLI (which has already gone through ExecuteC by the time this
	// RunE fires) or called directly, e.g. from a test that hasn't
	// executed rootCmd at all - without this, the page set generated
	// depends on incidental call order.
	rootCmd.InitDefaultCompletionCmd()

	header := &doc.GenManHeader{
		Title:   "LMM",
		Section: "1",
		Source:  "lmm " + version,
		Manual:  "User Commands",
		Date:    &genManDate,
	}
	if err := doc.GenManTree(rootCmd, header, dir); err != nil {
		return fmt.Errorf("generating man pages: %w", err)
	}
	return nil
}
