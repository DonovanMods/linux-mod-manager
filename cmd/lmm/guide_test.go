package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// guideDir is the user guide published to GitHub Pages (#553), relative to
// this package's working directory when tests run (cmd/lmm).
const guideDir = "../../docs/guide"

// guideInlineCommand matches an inline code span that starts with "lmm ".
var guideInlineCommand = regexp.MustCompile("`(lmm [^`]+)`")

// guideSubcommandWord matches a word that can name a subcommand.
var guideSubcommandWord = regexp.MustCompile(`^[a-z][a-z-]*$`)

// guideCommandLines returns every lmm command line the guide's pages show:
// the lines in fenced code blocks that start with "lmm " (after an optional
// "$ " prompt), and every inline code span that does. Each is keyed by
// "page:line" so a failure names where to look.
func guideCommandLines(t *testing.T) map[string]string {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join(guideDir, "*.md"))
	require.NoError(t, err)
	require.NotEmpty(t, pages, "no guide pages under %s", guideDir)

	found := map[string]string{}
	for _, page := range pages {
		f, err := os.Open(page)
		require.NoError(t, err)
		scanner := bufio.NewScanner(f)
		inFence := false
		for n := 1; scanner.Scan(); n++ {
			line := scanner.Text()
			where := filepath.Base(page) + ":" + strconv.Itoa(n)
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				cmd := strings.TrimPrefix(strings.TrimSpace(line), "$ ")
				if strings.HasPrefix(cmd, "lmm ") {
					found[where] = cmd
				}
				continue
			}
			for i, m := range guideInlineCommand.FindAllStringSubmatch(line, -1) {
				found[where+"#"+strconv.Itoa(i+1)] = m[1]
			}
		}
		require.NoError(t, scanner.Err())
		require.NoError(t, f.Close())
	}
	return found
}

// guideCheckCommand resolves one guide command line against the real
// command tree: every subcommand it names must exist, and every flag it
// passes must be one that command accepts. It returns "" when the line is
// sound, or what is wrong with it.
func guideCheckCommand(line string) string {
	var words []string
	for _, tok := range strings.Fields(line)[1:] {
		if tok == "|" || tok == ">" || tok == ">>" || tok == "&&" || tok == ";" || strings.HasPrefix(tok, "#") {
			break
		}
		words = append(words, tok)
	}

	// The subcommand path is the leading run of plain words; a flag, a
	// placeholder or an argument ends it.
	var path []string
	for _, w := range words {
		if !guideSubcommandWord.MatchString(w) {
			break
		}
		path = append(path, w)
	}
	cmd, rest, err := rootCmd.Find(path)
	if err != nil {
		return err.Error()
	}
	if len(rest) > 0 && !cmd.Runnable() {
		return "\"" + cmd.CommandPath() + "\" has no subcommand \"" + rest[0] + "\""
	}

	for _, w := range words {
		if !strings.HasPrefix(w, "-") || w == "-" {
			continue
		}
		if msg := guideCheckFlag(cmd, w); msg != "" {
			return msg
		}
	}
	return ""
}

// guideCheckFlag reports whether cmd accepts the flag token w ("--name",
// "--name=value" or "-x").
func guideCheckFlag(cmd *cobra.Command, w string) string {
	// cobra adds --help (and the root's --version) only when a command
	// runs, so add them here the same way before looking flags up.
	cmd.InitDefaultHelpFlag()
	if cmd == rootCmd {
		cmd.InitDefaultVersionFlag()
	}
	if strings.HasPrefix(w, "--") {
		name, _, _ := strings.Cut(strings.TrimPrefix(w, "--"), "=")
		if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
			return "\"" + cmd.CommandPath() + "\" has no flag --" + name
		}
		return ""
	}
	short := strings.TrimPrefix(w, "-")[:1]
	if cmd.Flags().ShorthandLookup(short) == nil && cmd.InheritedFlags().ShorthandLookup(short) == nil {
		return "\"" + cmd.CommandPath() + "\" has no flag -" + short
	}
	return ""
}

// TestGuideCommandsExist holds the user guide to the CLI it documents
// (#553): every lmm command the guide shows must name real subcommands and
// real flags, so a renamed command or flag fails the build instead of
// leaving the published guide wrong.
func TestGuideCommandsExist(t *testing.T) {
	lines := guideCommandLines(t)
	require.NotEmpty(t, lines, "the guide shows no lmm commands; is the parser broken?")
	for where, line := range lines {
		if msg := guideCheckCommand(line); msg != "" {
			t.Errorf("%s: %q: %s", where, line, msg)
		}
	}
}

// TestGuideCheckCommand pins the checker itself: a sound line passes, and
// a misspelt subcommand or flag is caught.
func TestGuideCheckCommand(t *testing.T) {
	for _, line := range []string{
		"lmm profile switch survival --yes",
		"lmm install --id 12345 -s nexusmods",
		"lmm update rollback <mod-id>",
		"lmm serve --no-open # comment --bogus",
		"lmm profile export survival > survival.yaml",
		"lmm game add --name=Foo",
	} {
		require.Empty(t, guideCheckCommand(line), line)
	}
	for _, line := range []string{
		"lmm profil list",
		"lmm profile lst",
		"lmm install --bogus",
		"lmm purge -Q",
	} {
		require.NotEmpty(t, guideCheckCommand(line), line)
	}
}
