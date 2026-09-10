package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// committedManDir is the repo-tracked man page directory, relative to this
// package's working directory when tests run (cmd/lmm).
const committedManDir = "../../docs/man/man1"

// TestGenManTree_ProducesPageForEveryVisibleCommand pins gen-man's basic
// contract: a page per visible command in the tree, including the five
// previously-undocumented families (#104), a nested subcommand, and no page
// for hidden commands (gen-man itself).
func TestGenManTree_ProducesPageForEveryVisibleCommand(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	wantPages := []string{
		"lmm.1",
		"lmm-import.1",
		"lmm-source.1",
		"lmm-auth.1",
		"lmm-uninstall.1",
		"lmm-game-add.1", // nested: game -> game add
	}
	for _, page := range wantPages {
		_, err := os.Stat(filepath.Join(dir, page))
		assert.NoError(t, err, "expected %s to be generated", page)
	}

	_, err := os.Stat(filepath.Join(dir, "lmm-tui.1"))
	assert.True(t, os.IsNotExist(err), "lmm tui was removed in v2; gen-man must not produce a page for it")

	_, err = os.Stat(filepath.Join(dir, "lmm-gen-man.1"))
	assert.True(t, os.IsNotExist(err), "gen-man is Hidden and must not get its own page")
}

// TestGenManTree_MatchesCommittedPages is the drift guard: docs/man/man1 must
// always be byte-identical to what the current command tree generates. When
// help text changes without regenerating, this test fails and tells the
// developer to run `make man` (issue #104 - 19 releases of undetected drift
// with the old hand-written pages).
func TestGenManTree_MatchesCommittedPages(t *testing.T) {
	committed, err := os.ReadDir(committedManDir)
	require.NoError(t, err, "reading committed man page directory")

	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	generated, err := os.ReadDir(dir)
	require.NoError(t, err)

	committedNames := direntNames(committed)
	generatedNames := direntNames(generated)
	require.ElementsMatch(t, committedNames, generatedNames,
		"docs/man/man1 is out of sync with the generated command tree; run `make man` to regenerate")

	for _, name := range committedNames {
		wantBytes, err := os.ReadFile(filepath.Join(committedManDir, name))
		require.NoError(t, err)
		gotBytes, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, string(wantBytes), string(gotBytes),
			"docs/man/man1/%s is stale; run `make man` to regenerate", name)
	}
}

// TestGenManTree_SynopsisPreservesAngleBracketArgs pins the fix for a
// blackfriday/md2man quirk: Cobra's man doc generator renders each
// command's Use line through Markdown before emitting roff, and a bare
// "<mod-id>"-style positional-arg placeholder parses there as an
// unrecognized inline HTML tag and is silently dropped - so
// "lmm search <query> [flags]" rendered as "lmm search  [flags]" with
// the argument gone, while square-bracket ("[flags]") placeholders were
// unaffected. genManTree must escape "<"/">" in every command's Use before
// generating (and restore it afterward, so --help is unaffected).
func TestGenManTree_SynopsisPreservesAngleBracketArgs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	searchSynopsis := readSynopsis(t, filepath.Join(dir, "lmm-search.1"))
	assert.Contains(t, searchSynopsis, "<query>",
		"lmm-search.1 SYNOPSIS should keep the <query> placeholder")

	uninstallSynopsis := readSynopsis(t, filepath.Join(dir, "lmm-uninstall.1"))
	assert.Contains(t, uninstallSynopsis, "<mod-id>",
		"lmm-uninstall.1 SYNOPSIS should keep the <mod-id> placeholder")
}

// TestGenManTree_AngleBracketArgsSurviveForEveryCommand is the exhaustive
// form of the above: every command in the tree whose Use string contains an
// angle-bracket placeholder must have that placeholder verbatim in its own
// generated page's SYNOPSIS, not just search/uninstall. The command count
// pins how many pages were actually affected (17 at the #104 final review;
// 16 since install's query became optional "[query]"; 18 since #97 added
// `mod lock <mod-id> [version]` and `mod unlock <mod-id>`; 22 since #333
// added `source add <file>` and `source remove <id>`); if the command tree
// changes, update the count deliberately rather than silently.
func TestGenManTree_AngleBracketArgsSurviveForEveryCommand(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	checked := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.IsAvailableCommand() {
			if args := angleBracketArgsRE.FindAllString(c.Use, -1); len(args) > 0 {
				checked++
				page := strings.ReplaceAll(c.CommandPath(), " ", "-") + ".1"
				synopsis := readSynopsis(t, filepath.Join(dir, page))
				for _, arg := range args {
					assert.Contains(t, synopsis, arg,
						"%s SYNOPSIS should keep the %s placeholder", page, arg)
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	// 25: 23 originally, +2 since #351 (`lmm init`, `lmm snapshot *`),
	// -1 since #346 (`profile import <file>` became `[file]` when
	// --workshop-collection became its second input), +1 since #359
	// (`lmm game show <game-id>`).
	assert.Equal(t, 25, checked,
		"expected exactly 25 commands with angle-bracket Use args; update this count if the command tree changed")
}

// TestGenManTree_AngleBracketPlaceholdersSurviveInLongHelp is the same
// finding one section down (#368 review Minor 4): the Use-line escaping only
// covered SYNOPSIS, so a placeholder inside a command's Long or Short help
// was still parsed as inline HTML and dropped from DESCRIPTION - `lmm game
// detect`'s help pointed twice at "lmm game add --from-detected <app-id>",
// and the man page rendered "lmm game add --from-detected " with the
// placeholder gone, leaving an incomplete command in the one place a user
// copies from.
//
// Exhaustive over the tree, like its SYNOPSIS twin, and the count pins how
// many pages actually carry such a placeholder so a new one is a deliberate
// change rather than a silent one.
func TestGenManTree_AngleBracketPlaceholdersSurviveInLongHelp(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	checked := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.IsAvailableCommand() {
			if args := helpPlaceholderRE.FindAllString(c.Long+"\n"+c.Short, -1); len(args) > 0 {
				checked++
				page := strings.ReplaceAll(c.CommandPath(), " ", "-") + ".1"
				body := readSection(t, filepath.Join(dir, page), ".SH DESCRIPTION")
				for _, arg := range args {
					assert.Contains(t, body, arg,
						"%s DESCRIPTION should keep the %s placeholder its help text spells out", page, arg)
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	assert.Equal(t, 4, checked,
		"expected exactly 4 commands whose Long/Short help spells an angle-bracket placeholder (game detect, auth login, verify, snapshot); update this count if the help text changed")
}

// TestGenManTree_UnderscoredNamesSurviveMarkdown is the same generator
// defect one character over (#368 review Minor 4): a PAIR of underscores in
// prose is Markdown emphasis, so `lmm auth login`'s help rendered
// "CURSEFORGE_API_KEY, or the derived LMM_<ID>_API_KEY" as
// "CURSEFORGE_APIKEY ... LMM_API_KEY" - two environment variable names that
// do not exist, in the sentence telling a user which one to set.
func TestGenManTree_UnderscoredNamesSurviveMarkdown(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	body := readSection(t, filepath.Join(dir, "lmm-auth-login.1"), ".SH DESCRIPTION")
	assert.Contains(t, body, "CURSEFORGE_API_KEY")
	assert.Contains(t, body, "LMM_<ID>_API_KEY")
}

// TestGenManTree_PlaceholdersSurviveInFlagHelp is the same generator defect
// one section further down the page (#368 re-review N2): the escaping
// covered Use/Short/Long/Example, but a command's pflag USAGE strings go
// through the same Markdown pass on their way into OPTIONS, and nothing
// escaped those. `lmm game add --mod-path` says "(default: <install
// path>/mods)" and the page rendered "(default: /mods)" - an absolute path
// at the filesystem root, which is actively WRONG rather than merely
// incomplete, on the page a user reads while adding their first game.
//
// Exhaustive over the tree like its Use and Long/Short twins, and the count
// pins how many flags actually carry such a placeholder so a new one is a
// deliberate change rather than a silent one.
func TestGenManTree_PlaceholdersSurviveInFlagHelp(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	checked := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.IsAvailableCommand() {
			page := strings.ReplaceAll(c.CommandPath(), " ", "-") + ".1"
			// NonInherited, because that is exactly the set cobra renders
			// under .SH OPTIONS; a parent's persistent flags land in the
			// separate .SH OPTIONS INHERITED FROM PARENT COMMANDS section,
			// and are checked on the page that owns them.
			c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
				args := flagPlaceholderRE.FindAllString(f.Usage, -1)
				if len(args) == 0 {
					return
				}
				checked++
				options := readSection(t, filepath.Join(dir, page), ".SH OPTIONS")
				for _, arg := range args {
					assert.Contains(t, options, arg,
						"%s OPTIONS should keep the %s placeholder --%s's usage string spells out",
						page, arg, f.Name)
				}
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	assert.Equal(t, 1, checked,
		"expected exactly 1 flag whose usage string spells an angle-bracket placeholder (game add --mod-path); update this count if a flag's help changed")
}

var angleBracketArgsRE = regexp.MustCompile(`<[^>]+>`)

// helpPlaceholderRE is angleBracketArgsRE narrowed to a PLACEHOLDER token -
// no whitespace, so a shell redirect in `lmm completion`'s help ("lmm
// completion zsh > ...") is not read as one.
var helpPlaceholderRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9|._-]*>`)

// flagPlaceholderRE is helpPlaceholderRE widened to allow an inner SPACE, so
// `--mod-path`'s "<install path>" counts. Safe here where it would not be in
// prose: a flag usage string is one short line with no shell examples in it.
var flagPlaceholderRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9|._ -]*>`)

// readSynopsis extracts the roff SYNOPSIS section's body from a generated
// man page for content assertions.
func readSynopsis(t *testing.T, path string) string {
	t.Helper()
	return readSection(t, path, ".SH SYNOPSIS")
}

// readSection extracts one roff section's body (up to the next .SH) from a
// generated man page, so an assertion about DESCRIPTION cannot be satisfied
// by the same text appearing in SYNOPSIS.
func readSection(t *testing.T, path, marker string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)

	content := string(data)
	start := strings.Index(content, marker)
	require.NotEqual(t, -1, start, "%s has no %s section", path, marker)

	rest := content[start+len(marker):]
	end := strings.Index(rest, ".SH ")
	if end == -1 {
		end = len(rest)
	}
	return rest[:end]
}

// TestGenManTree_RemovesStalePages pins that genManTree cleans up orphaned
// pages before generating: if a command is removed or renamed, its old .1
// file would otherwise stay behind forever - `make man` wouldn't remove it,
// the drift test would fail on the extra file, and nothing would tell a
// developer what to do about it. Scoped strictly to lmm.1/lmm-*.1: gen-man
// accepts an arbitrary dir, so other packages' pages in a shared man path
// (and anything that isn't a man page) must survive untouched.
func TestGenManTree_RemovesStalePages(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, "lmm-no-such-command.1")
	require.NoError(t, os.WriteFile(stale, []byte("stale page"), 0644))

	keep := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(keep, []byte("not a man page"), 0644))

	foreign := filepath.Join(dir, "othertool.1")
	require.NoError(t, os.WriteFile(foreign, []byte("someone else's page"), 0644))

	require.NoError(t, genManTree(dir))

	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "stale lmm-no-such-command.1 should have been removed")

	_, err = os.Stat(keep)
	assert.NoError(t, err, "non-.1 files must not be touched")

	_, err = os.Stat(foreign)
	assert.NoError(t, err, "other packages' .1 pages must not be touched")

	_, err = os.Stat(filepath.Join(dir, "lmm.1"))
	assert.NoError(t, err, "real pages should still be generated")
}

func direntNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// changelogRelease matches a dated release heading in CHANGELOG.md,
// capturing the date. Keep-a-Changelog orders newest first, so the first
// match is the most recent release.
var changelogRelease = regexp.MustCompile(`(?m)^## \[[^\]]+\] - (\d{4}-\d{2}-\d{2})\s*$`)

// TestGenManDateIsNotOlderThanTheNewestRelease is #401. genManDate is
// PINNED rather than time.Now() so that regenerating without a help-text
// change produces identical bytes and the drift test above stays
// meaningful - that part is right and stays. What was missing is anything
// to make the pin move: the pages shipped with 2.0.0 would have been dated
// "Jul 2026", two months before the release they document.
//
// The release-prep commit already runs `make man` (the drift test forces
// it). This makes it bump the date too, by failing whenever the newest
// dated CHANGELOG section is later than the date the pages carry.
func TestGenManDateIsNotOlderThanTheNewestRelease(t *testing.T) {
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	require.NoError(t, err, "reading CHANGELOG.md")

	m := changelogRelease.FindSubmatch(changelog)
	require.NotNil(t, m, "CHANGELOG.md has no dated release heading to compare against")

	released, err := time.Parse("2006-01-02", string(m[1]))
	require.NoError(t, err)

	assert.False(t, genManDate.Before(released),
		"genManDate (%s) predates the newest release in CHANGELOG.md (%s): bump it in the release-prep commit, alongside `version` and the CHANGELOG section, and re-run `make man`",
		genManDate.Format("2006-01-02"), released.Format("2006-01-02"))
}
