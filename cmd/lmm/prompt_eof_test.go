package main

import (
	"bufio"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1b review F10: #385's title says "every ... prompt", but the fix reached
// only the three prompts that already had a --json remedy to mirror. The
// rest still handed a scripted or cron caller "reading input: EOF" - an
// implementation detail with no way forward, which is the whole defect.
//
// Each test below drives one of those prompts with a closed stdin and
// requires the SAME answer the non-interactive path gives: the flag or
// argument that supplies the value.

// TestGameAddSourcePicker_EOFNamesTheSourceFlag covers the first prompt
// `lmm game add` prints, which is where a piped run stops.
func TestGameAddSourcePicker_EOFNamesTheSourceFlag(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "alpha", name: "Alpha"})
	svc.RegisterSource(&mockGameAddSource{id: "beta", name: "Beta"})

	cmd, buf := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader("")), svc)

	require.Error(t, err, "output so far:\n%s", buf.String())
	assert.NotContains(t, err.Error(), "EOF")
	assert.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Contains(t, err.Error(), "--source")
}

// TestGameAddValuePrompt_EOFNamesItsOwnFlag covers the required-value
// prompts (missingGameAddValue): the run gets as far as the game name and
// then finds stdin closed.
func TestGameAddValuePrompt_EOFNamesItsOwnFlag(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "alpha", name: "Alpha"})
	gameAddGameID = "testgame"

	cmd, buf := newGameAddCmd()
	// Answers the source picker, then nothing: the name prompt hits EOF.
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader("1\n")), svc)

	require.Error(t, err, "output so far:\n%s", buf.String())
	assert.NotContains(t, err.Error(), "EOF")
	assert.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Contains(t, err.Error(), "--name")

	games, loadErr := config.LoadGames(configDir)
	require.NoError(t, loadErr)
	assert.Empty(t, games)
}

// TestReadMultiSelectionLine_EOFIsTheConfirmationSentinel covers `lmm
// install`'s file picker, whose --json path returns the bare
// ErrConfirmationRequired sentinel (with its own --yes/--force wording).
func TestReadMultiSelectionLine_EOFIsTheConfirmationSentinel(t *testing.T) {
	origJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSON })

	var err error
	captureStdout(t, func() error {
		_, _, err = readMultiSelectionLine(bufio.NewReader(strings.NewReader("")), "Select file(s)", 1, 3)
		return nil
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "EOF")
	assert.ErrorIs(t, err, core.ErrConfirmationRequired)
}

// TestPromptReorderFrom_EOFNamesThePositionalForm covers `lmm profile
// reorder -i` with nothing piped in: the same command takes the order as
// mod ID arguments, which is the way out.
func TestPromptReorderFrom_EOFNamesThePositionalForm(t *testing.T) {
	refs := []domain.ModReference{
		{SourceID: "src", ModID: "a"}, {SourceID: "src", ModID: "b"},
	}

	var err error
	captureStdout(t, func() error {
		_, err = promptReorderFrom(strings.NewReader(""), "default", refs, map[string]string{})
		return nil
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "EOF")
	assert.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Contains(t, err.Error(), "mod ID")
}

// TestReadAPIKey_EOFNamesTheKeyFlags covers the piped-stdin fallback in
// `lmm auth login`, which `lmm init` also drives - the one prompt in the
// wizard's path that could still report a bare EOF.
func TestReadAPIKey_EOFNamesTheKeyFlags(t *testing.T) {
	var out strings.Builder
	var err error
	withStdin(t, "", func() {
		_, err = readAPIKey(&out)
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "EOF")
	assert.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Contains(t, err.Error(), "--key-stdin")
}

// TestPromptReadsRouteThroughTheSharedHelpers is the ratchet under all of
// the above: "reading input: %w" may be written in exactly one file, so a
// new prompt cannot reintroduce the raw-EOF rendering by copying the old
// three-line shape. helpers.go is where the two wordings (the EOF remedy
// and a genuine stdin fault) are decided.
func TestPromptReadsRouteThroughTheSharedHelpers(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "helpers.go" {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, parseErr, "parsing %s", name)

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			assert.NotContains(t, lit.Value, "reading input: %w",
				"%s: a prompt read must be worded by promptReadError/promptReadErrorAs, so a closed stdin names the flag that answers it instead of reporting EOF (#385)",
				fset.Position(lit.Pos()))
			return true
		})
	}
}
