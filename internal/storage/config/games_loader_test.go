package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadGamesReadsTheLoaderBlock: #359's `loader:` block round-trips
// through games.yaml the way deploy_mode does - parsed by the domain's own
// fail-loud parsers, so a typo names the field and the game rather than
// silently defaulting.
func TestLoadGamesReadsTheLoaderBlock(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "Lethal Company")
	yaml := "games:\n  lethal-company:\n    name: Lethal Company\n" +
		"    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "\"\n" +
		"    sources:\n      nexusmods: lethalcompany\n" +
		"    loader:\n      kind: bepinex\n      version: 5.4.23.5\n" +
		"      runtime: mono\n      bootstrap: proton\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	game := games["lethal-company"]
	require.NotNil(t, game)
	require.NotNil(t, game.Loader)
	assert.Equal(t, domain.LoaderKindBepInEx, game.Loader.Kind)
	assert.Equal(t, "5.4.23.5", game.Loader.Version)
	assert.Equal(t, domain.LoaderRuntimeMono, game.Loader.Runtime)
	assert.Equal(t, domain.LoaderBootstrapProton, game.Loader.Bootstrap)
	assert.True(t, game.DeclaresBepInEx())
}

// A game with no loader block has no loader - not a zero-valued one, which
// would make DeclaresBepInEx' answer depend on a struct nobody wrote.
func TestLoadGamesLeavesTheLoaderNilWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "g")
	yaml := "games:\n  g:\n    name: G\n    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "\"\n    sources:\n      nexusmods: g\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Nil(t, games["g"].Loader)
	assert.False(t, games["g"].DeclaresBepInEx())
}

// An unrecognised runtime or bootstrap fails the LOAD, naming the field, the
// value, the game and the valid set - exactly what link_method and
// deploy_mode do. Silently defaulting would leave a game verifying against
// the wrong bootstrap files forever.
func TestLoadGamesRefusesAnUnknownLoaderValue(t *testing.T) {
	for _, tc := range []struct{ block, want string }{
		{"      runtime: coreclr\n", "mono, il2cpp"},
		{"      bootstrap: wine\n", "native, proton"},
	} {
		dir := t.TempDir()
		install := filepath.Join(dir, "g")
		yaml := "games:\n  g:\n    name: G\n    install_path: \"" + install + "\"\n" +
			"    mod_path: \"" + install + "\"\n    sources:\n      nexusmods: g\n" +
			"    loader:\n      kind: bepinex\n" + tc.block
		require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

		_, err := config.LoadGames(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `game "g"`)
		assert.Contains(t, err.Error(), tc.want)
	}
}

// A loader block a mod source, `lmm game edit --loader` or the web form set
// is written back verbatim, with the enums as their wire NAMES - and a
// loaderless game gains no empty `loader:` key, so an existing games.yaml is
// untouched by any other command's whole-file re-marshal.
func TestSaveGameWritesTheLoaderBlock(t *testing.T) {
	dir := t.TempDir()
	install := t.TempDir()
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "lethal-company", Name: "Lethal Company",
		InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "lethalcompany"},
		Loader: &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
			Runtime: domain.LoaderRuntimeIL2CPP, Bootstrap: domain.LoaderBootstrapNative,
		},
	}))
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "plain", Name: "Plain",
		InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "plain"},
	}))

	raw, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "kind: bepinex")
	assert.Contains(t, text, "runtime: il2cpp")
	assert.Contains(t, text, "bootstrap: native")
	assert.Equal(t, 1, strings.Count(text, "loader:"), "a loaderless game must gain no loader key")

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.NotNil(t, games["lethal-company"].Loader)
	assert.Equal(t, domain.LoaderRuntimeIL2CPP, games["lethal-company"].Loader.Runtime)
	assert.Equal(t, domain.LoaderBootstrapNative, games["lethal-company"].Loader.Bootstrap)
	assert.Nil(t, games["plain"].Loader)
}

// An unknown runtime/bootstrap only ever reaches games.yaml through a
// hand-edit, because the domain enums cannot hold one - so the write side's
// job is just to emit the empty string for "not answered yet" rather than a
// value nobody chose.
func TestSaveGameOmitsUnansweredLoaderFields(t *testing.T) {
	dir := t.TempDir()
	install := t.TempDir()
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "g", Name: "G", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "g"},
		Loader:    &domain.GameLoader{Kind: domain.LoaderKindBepInEx},
	}))

	raw, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "kind: bepinex")
	assert.NotContains(t, string(raw), "runtime:")
	assert.NotContains(t, string(raw), "bootstrap:")
	assert.NotContains(t, string(raw), "version:")
}

// TestLoadGamesRefusesAnEmptyLoaderKind is review F8: a hand-written block
// with a version and no kind loaded as a declaration that declares nothing -
// IsBepInEx false, so no rule fires - and re-marshalled as `kind: ""`, while
// core.LoaderSpec.loader refuses the identical input naming loader.kind.
// Two doors into the same field must not give two answers.
func TestLoadGamesRefusesAnEmptyLoaderKind(t *testing.T) {
	for _, block := range []string{
		"    loader:\n      version: 5.4.23.5\n",
		"    loader:\n      kind: \"   \"\n",
	} {
		dir := t.TempDir()
		install := filepath.Join(dir, "g")
		yaml := "games:\n  g:\n    name: G\n    install_path: \"" + install + "\"\n" +
			"    mod_path: \"" + install + "\"\n    sources:\n      nexusmods: g\n" + block
		require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

		_, err := config.LoadGames(dir)
		require.Error(t, err, "block %q", block)
		assert.ErrorIs(t, err, domain.ErrInvalidLoaderKind)
		assert.Contains(t, err.Error(), `game "g"`)
		assert.Contains(t, err.Error(), "loader.kind")
	}
}
