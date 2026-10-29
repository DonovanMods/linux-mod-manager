package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handWrittenGames is a games.yaml the way a person writes one (#362): a
// two-space indent, comments, blank lines between games, `~` paths, a
// relative mod_path, quoted scalars and a flow-style sources map - none of
// which lmm would write itself.
const handWrittenGames = `# lmm games - maintained by hand
games:
  skyrim:
    name: Skyrim Special Edition
    install_path: ~/Games/Skyrim
    mod_path: Data   # relative on purpose
    sources:
      nexusmods: skyrimspecialedition
    cache_path: ~/.cache/lmm-skyrim

  # Valheim lives on the other drive
  valheim:
    name: "Valheim"
    install_path: '~/Games/Valheim'
    mod_path: BepInEx/plugins
    sources: {thunderstore: valheim}
    link_method: copy

  icarus:
    name: Icarus
    install_path: /games/icarus
    mod_path: /games/icarus/Icarus/Content/Paks/mods
    sources:
      icarus: icarus
    deploy_mode: compile
`

// writeHandWrittenGames sandboxes HOME and writes handWrittenGames,
// returning the config dir.
func writeHandWrittenGames(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(handWrittenGames), 0o644))
	return dir
}

func readGamesYAML(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	return string(data)
}

// cut splits handWrittenGames around the lines of one game: before ends just
// ahead of its key line, after starts just past its last line's break.
func cut(t *testing.T, keyLine, lastLine string) (before, after string) {
	t.Helper()
	start := strings.Index(handWrittenGames, keyLine)
	end := strings.Index(handWrittenGames, lastLine)
	require.True(t, start >= 0 && end > start)
	return handWrittenGames[:start], handWrittenGames[end+len(lastLine):]
}

func TestSaveGame_LeavesOtherGamesByteIdentical(t *testing.T) {
	dir := writeHandWrittenGames(t)
	games, err := config.LoadGames(dir)
	require.NoError(t, err)

	valheim := games["valheim"]
	valheim.Name = "Valheim (modded)"
	require.NoError(t, config.SaveGame(dir, valheim))

	before, after := cut(t, "  valheim:\n", "    link_method: copy\n")
	out := readGamesYAML(t, dir)
	assert.True(t, strings.HasPrefix(out, before), "everything before valheim, its comment included, must be untouched:\n%s", out)
	assert.True(t, strings.HasSuffix(out, after), "everything after valheim must be untouched:\n%s", out)

	reloaded, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, "Valheim (modded)", reloaded["valheim"].Name)
	assert.Equal(t, games["skyrim"], reloaded["skyrim"])
	assert.Equal(t, games["icarus"], reloaded["icarus"])
}

func TestSaveGame_TheHandWrittenGameItselfIsRewrittenAlone(t *testing.T) {
	dir := writeHandWrittenGames(t)
	games, err := config.LoadGames(dir)
	require.NoError(t, err)

	require.NoError(t, config.SaveGame(dir, games["skyrim"]))

	before, after := cut(t, "  skyrim:\n", "    cache_path: ~/.cache/lmm-skyrim\n")
	out := readGamesYAML(t, dir)
	assert.True(t, strings.HasPrefix(out, before), out)
	assert.True(t, strings.HasSuffix(out, after), out)
	// The saved game is written from its resolved values, as it always was.
	home, _ := os.UserHomeDir()
	assert.Contains(t, out, "    install_path: "+filepath.Join(home, "Games/Skyrim")+"\n")

	reloaded, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, games, reloaded)
}

func TestSaveGame_ANewGameIsAppendedInTheFilesOwnIndent(t *testing.T) {
	dir := writeHandWrittenGames(t)

	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID:          "stardew",
		Name:        "Stardew Valley",
		InstallPath: "/games/stardew",
		ModPath:     "/games/stardew/Mods",
		SourceIDs:   map[string]string{"nexusmods": "stardewvalley"},
	}))

	out := readGamesYAML(t, dir)
	assert.True(t, strings.HasPrefix(out, handWrittenGames), "every existing line must be untouched:\n%s", out)
	assert.Equal(t, `  stardew:
    name: Stardew Valley
    install_path: /games/stardew
    mod_path: /games/stardew/Mods
    sources:
      nexusmods: stardewvalley
`, strings.TrimPrefix(out, handWrittenGames))

	reloaded, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Len(t, reloaded, 4)
	assert.Equal(t, "Stardew Valley", reloaded["stardew"].Name)
}

func TestDeleteGame_LeavesOtherGamesByteIdentical(t *testing.T) {
	for _, tc := range []struct{ id, keyLine, lastLine string }{
		{"skyrim", "  skyrim:\n", "    cache_path: ~/.cache/lmm-skyrim\n"},
		{"valheim", "  valheim:\n", "    link_method: copy\n"},
		{"icarus", "  icarus:\n", "    deploy_mode: compile\n"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			dir := writeHandWrittenGames(t)
			require.NoError(t, config.DeleteGame(dir, tc.id))

			before, after := cut(t, tc.keyLine, tc.lastLine)
			assert.Equal(t, before+after, readGamesYAML(t, dir))

			reloaded, err := config.LoadGames(dir)
			require.NoError(t, err)
			assert.Len(t, reloaded, 2)
			assert.NotContains(t, reloaded, tc.id)
		})
	}
}

func TestDeleteGame_TheLastGameLeavesALoadableFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte("games:\n  only:\n    name: Only\n    install_path: ~/only\n"), 0o644))

	require.NoError(t, config.DeleteGame(dir, "only"))
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Empty(t, games)

	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "next", Name: "Next", InstallPath: "/next"}))
	games, err = config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"next"}, keys(games))
}

// TestSaveGame_EmptyOrMissingGames covers the files there is no game to
// preserve in: missing, empty, `games:` with no value, and an empty flow
// mapping.
func TestSaveGame_EmptyOrMissingGames(t *testing.T) {
	for name, content := range map[string]*string{
		"missing":       nil,
		"empty":         ptr(""),
		"comment only":  ptr("# nothing yet\n"),
		"games null":    ptr("games:\n"),
		"games flow {}": ptr("games: {}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if content != nil {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(*content), 0o644))
			}
			require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "a", Name: "A", InstallPath: "/a", ModPath: "/a/mods"}))
			games, err := config.LoadGames(dir)
			require.NoError(t, err)
			require.Contains(t, games, "a")
			assert.Equal(t, "/a/mods", games["a"].ModPath)
		})
	}
}

// TestSaveGame_AFlowStyleFileKeepsItsHandWrittenValues: a layout the save
// cannot patch line by line is patched as a document instead, which still
// keeps every other game's own spelling of its values.
func TestSaveGame_AFlowStyleFileKeepsItsHandWrittenValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"),
		[]byte("games: {a: {name: A, install_path: ~/a, mod_path: Data}, b: {name: B, install_path: /b}}\n"), 0o644))

	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "b", Name: "B2", InstallPath: "/b"}))

	var file config.GamesFile
	require.NoError(t, safeyaml.Unmarshal([]byte(readGamesYAML(t, dir)), &file))
	assert.Equal(t, "~/a", file.Games["a"].InstallPath)
	assert.Equal(t, "Data", file.Games["a"].ModPath)
	assert.Equal(t, "B2", file.Games["b"].Name)
}

func ptr[T any](v T) *T { return &v }

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSaveGame_KeepsCRLFLineBreaks: lines lmm adds end the way the file's
// own do.
func TestSaveGame_KeepsCRLFLineBreaks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	original := strings.ReplaceAll(handWrittenGames, "\n", "\r\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(original), 0o644))

	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "new", Name: "New", InstallPath: "/new"}))

	out := readGamesYAML(t, dir)
	require.True(t, strings.HasPrefix(out, original), out)
	assert.NotContains(t, strings.ReplaceAll(out, "\r\n", ""), "\n", "every added line must end in CRLF")
}
