package config

// #452: a hand-edited games.yaml, config.yaml or source definition with a
// construct the YAML decoder panicked on crashed every lmm command at
// startup - `lmm serve` included - until the user found the file. Each
// reader is fed FuzzMarkModsDisabled's corpus, the known crasher among it,
// and has to answer with an error naming the file, never a panic.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decoderCrasher is the document gopkg.in/yaml.v3 v3.0.1 panicked on: a
// merge key over a mapping keyed by a mapping.
const decoderCrasher = "<<:\n? 0:"

// fuzzCorpus returns the documents in FuzzMarkModsDisabled's corpus - the
// []byte argument of each entry - for seeding the readers' own fuzz targets.
func fuzzCorpus(t testing.TB) [][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "fuzz", "FuzzMarkModsDisabled")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var docs [][]byte
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		for line := range strings.Lines(string(data)) {
			quoted, ok := strings.CutPrefix(strings.TrimSpace(line), "[]byte(")
			if !ok {
				continue
			}
			doc, err := strconv.Unquote(strings.TrimSuffix(quoted, ")"))
			require.NoError(t, err, entry.Name())
			docs = append(docs, []byte(doc))
		}
	}
	require.Contains(t, docs, []byte(decoderCrasher), "the corpus still holds the crasher")
	return docs
}

func TestLoad_AConfigTheDecoderCannotReadIsANamedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(decoderCrasher), 0o644))

	var err error
	require.NotPanics(t, func() { _, err = Load(dir) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config: "+path)
}

func TestLoadGames_AGamesFileTheDecoderCannotReadIsANamedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "games.yaml")

	// The crasher as the whole file, and nested inside a game's entry.
	for _, doc := range []string{decoderCrasher, "games:\n  g:\n    <<:\n    ? 0:\n"} {
		require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
		var err error
		require.NotPanics(t, func() { _, err = LoadGames(dir) }, doc)
		require.Error(t, err, doc)
		assert.Contains(t, err.Error(), path, doc)
	}
}

func TestLoadSourceDefinitions_ADefinitionTheDecoderCannotReadIsNamed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sources", "broken.yaml"), []byte(decoderCrasher), 0o644))

	var loadErrs []SourceLoadError
	require.NotPanics(t, func() {
		var err error
		_, loadErrs, err = LoadSourceDefinitions(dir)
		require.NoError(t, err, "one broken file never stops the rest loading")
	})
	require.Len(t, loadErrs, 1)
	assert.Equal(t, "broken.yaml", loadErrs[0].File)
	assert.ErrorContains(t, loadErrs[0].Err, "parsing YAML")

	require.NotPanics(t, func() {
		_, err := ParseSourceDefinition([]byte(decoderCrasher))
		require.ErrorContains(t, err, "parsing YAML")
	})
}

// FuzzLoadConfig, FuzzLoadGames and FuzzParseSourceDefinition only ask that
// a reader never panics, whatever the file holds. Their seeds are the
// profile editor's corpus, which is where the crasher was found.
func FuzzLoadConfig(f *testing.F) {
	for _, doc := range fuzzCorpus(f) {
		f.Add(doc)
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), doc, 0o644))
		_, _ = Load(dir)
	})
}

func FuzzLoadGames(f *testing.F) {
	for _, doc := range fuzzCorpus(f) {
		f.Add(doc)
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), doc, 0o644))
		_, _ = LoadGames(dir)
	})
}

func FuzzParseSourceDefinition(f *testing.F) {
	for _, doc := range fuzzCorpus(f) {
		f.Add(doc)
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		_, _ = ParseSourceDefinition(doc)
	})
}
