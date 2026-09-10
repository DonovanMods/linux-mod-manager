package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Remove existing *.1 pages before generating, so a command that gets
	// removed or renamed doesn't leave its old page behind forever: without
	// this, the orphaned file survives every future `make man` run (nothing
	// deletes what generation no longer writes), the drift test would fail
	// on it, and there'd be nothing telling a developer what to do about
	// it. Scoped strictly to *.1 - anything else in dir is left alone.
	if err := removeStaleManPages(dir); err != nil {
		return fmt.Errorf("removing stale man pages: %w", err)
	}

	// Cobra normally attaches the default "completion" subcommand tree
	// lazily, the first time ExecuteC() runs. Force it here so the
	// generated set is identical whether genManTree is reached via the
	// CLI (which has already gone through ExecuteC by the time this
	// RunE fires) or called directly, e.g. from a test that hasn't
	// executed rootCmd at all - without this, the page set generated
	// depends on incidental call order.
	rootCmd.InitDefaultCompletionCmd()

	// Cobra's man doc generator renders each command's Use line (e.g.
	// "install <query>") through Markdown before emitting roff, and a bare
	// "<query>" parses there as an unrecognized inline HTML tag, which gets
	// silently dropped - "lmm install <query> [flags]" was rendering as
	// "lmm install  [flags]" in SYNOPSIS, argument gone (17 of 49 pages
	// affected; square-bracket placeholders like "[flags]" were unaffected).
	// Escaping "<"/">" as their Markdown backslash-escapes survives that
	// pass and round-trips to the literal character. Restore afterward so
	// --help (which reads these fields directly, unescaped) is never
	// affected, and so this is safe to call repeatedly, e.g. from tests.
	//
	// The SAME pass runs over Short/Long/Example, because DESCRIPTION is
	// rendered through the same Markdown step (#368 review Minor 4): `lmm
	// game detect`'s help pointed twice at `lmm game add --from-detected
	// <app-id>` and the page rendered "--from-detected " - an incomplete
	// command in the one place a user copies from. Underscores go with them,
	// for the same reason one step further on: a PAIR of them is Markdown
	// emphasis, so `lmm auth login`'s "CURSEFORGE_API_KEY, or the derived
	// LMM_<ID>_API_KEY" rendered as "CURSEFORGE_APIKEY ... LMM_API_KEY" -
	// two environment variable names that do not exist.
	restore := escapeHelpAngleBrackets(rootCmd)
	defer restore()

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

// removeStaleManPages deletes this project's pages (lmm.1 and lmm-*.1)
// directly inside dir. Scoped to the lmm prefix rather than *.1 because
// `gen-man [dir]` accepts an arbitrary directory - pointed at a shared man
// path, a bare *.1 sweep would delete other packages' pages. dir is
// expected to already exist (genManTree creates it via MkdirAll before
// calling this).
func removeStaleManPages(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "lmm-*.1"))
	if err != nil {
		return err
	}
	root := filepath.Join(dir, "lmm.1")
	if _, statErr := os.Stat(root); statErr == nil {
		matches = append(matches, root)
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			return err
		}
	}
	return nil
}

// helpMarkdownEscaper rewrites the characters blackfriday (via go-md2man)
// reads as markup to their Markdown backslash-escaped form, so they reach
// roff as the literal characters the help text wrote: "<"/">" would parse as
// inline HTML and be dropped, and a pair of "_" as emphasis.
var helpMarkdownEscaper = strings.NewReplacer("<", `\<`, ">", `\>`, "_", `\_`)

// helpMarkdownUnescaper undoes that first, so a help string that already
// spells the escape by hand (`lmm verify`'s Long has `\<mod-id\>`, written
// back when only Use was escaped) is escaped ONCE rather than rendering a
// literal backslash.
var helpMarkdownUnescaper = strings.NewReplacer(`\<`, "<", `\>`, ">", `\_`, "_")

// escapeHelpText escapes a help string line by line, leaving CODE lines
// alone: an indented line is a Markdown code block, where nothing is parsed
// as markup - "<mod-id>" already survives verbatim there (`lmm verify`'s
// report legend) and a backslash would render AS a backslash. Prose is where
// the text gets eaten, and prose is what gets escaped.
//
// A string that already spells the escape by hand is normalized first, so it
// is escaped exactly once.
func escapeHelpText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		lines[i] = helpMarkdownEscaper.Replace(helpMarkdownUnescaper.Replace(line))
	}
	return strings.Join(lines, "\n")
}

// escapeHelpAngleBrackets walks cmd and its descendants, escaping every
// help string the man generator renders through Markdown - Use (SYNOPSIS)
// plus Short/Long/Example (NAME and DESCRIPTION) - in place. Returns a
// restore func - callers must defer it - that puts every changed string back
// exactly as it was, so --help is never affected.
func escapeHelpAngleBrackets(cmd *cobra.Command) (restore func()) {
	type saved struct {
		field   *string
		content string
	}
	var originals []saved

	escape := func(field *string) {
		if !strings.ContainsAny(*field, "<>_") {
			return
		}
		originals = append(originals, saved{field, *field})
		*field = escapeHelpText(*field)
	}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		escape(&c.Use)
		escape(&c.Short)
		escape(&c.Long)
		escape(&c.Example)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)

	return func() {
		for _, o := range originals {
			*o.field = o.content
		}
	}
}
