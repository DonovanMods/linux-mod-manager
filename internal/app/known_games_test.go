package app

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The known-games list (internal/source/steam/data/steam-games.yaml) is
// curated by hand, one entry per game, and every entry is four independent
// facts a contributor looked up: a slug, a mod path, a deploy mode and a
// source map. Three of those can be wrong in ways nothing else in the build
// notices - a mod path that is absolute deploys into somebody's root, a
// source id nothing registers fails at install time rather than at add
// time, a duplicated slug quietly shadows another game.
//
// This is the one check that reads the WHOLE shipped list and says so.
// It lives in internal/app rather than internal/source/steam because it is
// the only package that has both: the list, and the catalogue of source ids
// the process actually registers (builtinSourceIDs, kept honest by
// TestBuiltinSourceIDsMatchTheFactories).

// slugPattern is the shape deriveSlug produces and the shape a curated slug
// must match: lowercase alphanumerics in dash-separated runs, no leading,
// trailing or doubled dash.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// appIDPattern: Steam app ids are decimal, and the yaml key must be quoted
// so it stays a string. A key that is not all digits is a typo, not a game.
var appIDPattern = regexp.MustCompile(`^[0-9]+$`)

// checkKnownGame returns every way one entry violates the curation rules,
// as sentences. It takes the source-id catalogue rather than reaching for
// the package variable so the bad-input table below can exercise it.
func checkKnownGame(appID string, info steam.GameInfo, knownSourceIDs map[string]bool) []string {
	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !appIDPattern.MatchString(appID) {
		report("app id %q is not a decimal Steam app id (quote the yaml key)", appID)
	}
	if info.Name == "" {
		report("app id %s has no name", appID)
	}
	switch {
	case info.Slug == "":
		report("app id %s has no slug", appID)
	case !slugPattern.MatchString(info.Slug):
		report("app id %s has slug %q, which is not a lowercase dashed slug", appID, info.Slug)
	}

	// #313: mod_path here is relative to the game's install directory, and
	// detection joins the two. An absolute path, a Windows path, or one
	// that climbs out with ".." would be joined anyway and land somewhere
	// nobody asked for.
	switch {
	case strings.HasPrefix(info.ModPath, "/"):
		report("app id %s has an absolute mod_path %q; it must be relative to the install directory", appID, info.ModPath)
	case strings.Contains(info.ModPath, `\`):
		report("app id %s has a backslash in mod_path %q; write it with forward slashes", appID, info.ModPath)
	case strings.Contains(info.ModPath, ".."):
		report("app id %s has a mod_path %q that climbs out of the install directory", appID, info.ModPath)
	case strings.HasPrefix(info.ModPath, "./"):
		report("app id %s has a mod_path %q with a redundant leading ./", appID, info.ModPath)
	}

	if _, ok := domain.ParseDeployMode(info.DeployMode); !ok {
		report("app id %s has deploy_mode %q, which domain.ParseDeployMode rejects", appID, info.DeployMode)
	}

	// An entry that names no source resolves to a game with no source at
	// all (core.GameSpecFromDetected derives {nexusmods: NexusID} only when
	// NexusID is set), which is a curated entry that cannot install
	// anything - worse than leaving the game detect-only.
	if info.NexusID == "" && len(info.Sources) == 0 {
		report("app id %s names no source: set nexus_id, or a sources map", appID)
	}
	for id, gameID := range info.Sources {
		if !knownSourceIDs[id] {
			report("app id %s maps source %q, which no built-in source registers", appID, id)
		}
		if gameID == "" {
			report("app id %s maps source %q to an empty game id", appID, id)
		}
	}
	return problems
}

// TestCheckKnownGame exercises the helper itself against hand-built
// entries, good and bad. It does not read the shipped list - that is
// TestKnownGamesListIsWellFormed's job - so a real violation cannot make
// this one green by accident, and a helper that silently stopped checking
// anything cannot make that one green either.
func TestCheckKnownGame(t *testing.T) {
	registered := map[string]bool{"nexusmods": true, "steamworkshop": true, "icarus": true}
	good := steam.GameInfo{Slug: "my-game", Name: "My Game", NexusID: "mygame", ModPath: "Data"}

	tests := []struct {
		name  string
		appID string
		info  steam.GameInfo
		want  string // substring the one expected problem must contain; "" means none
	}{
		{"a well-formed entry", "123456", good, ""},
		{"the game root is a legitimate mod path", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: ""}, ""},
		{"a sources map instead of a nexus id", "123456",
			steam.GameInfo{Slug: "g", Name: "G", ModPath: "Mods", Sources: map[string]string{"steamworkshop": "123456"}}, ""},
		{"a non-default deploy mode", "123456",
			steam.GameInfo{Slug: "g", Name: "G", ModPath: "Mods", DeployMode: "compile", Sources: map[string]string{"icarus": "icarus"}}, ""},

		{"a non-numeric app id", "not-an-app", good, "not a decimal Steam app id"},
		{"no name", "123456", steam.GameInfo{Slug: "g", NexusID: "g"}, "has no name"},
		{"no slug", "123456", steam.GameInfo{Name: "G", NexusID: "g"}, "has no slug"},
		{"a capitalised slug", "123456",
			steam.GameInfo{Slug: "My-Game", Name: "G", NexusID: "g"}, "not a lowercase dashed slug"},
		{"an absolute mod path", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: "/home/me/mods"}, "absolute mod_path"},
		{"a windows mod path", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: `Data\Mods`}, "backslash in mod_path"},
		{"a mod path that climbs out", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: "../mods"}, "climbs out of the install directory"},
		{"a redundant leading dot-slash", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: "./mods"}, "redundant leading ./"},
		{"an unparseable deploy mode", "123456",
			steam.GameInfo{Slug: "g", Name: "G", NexusID: "g", ModPath: "Data", DeployMode: "Compile"}, "ParseDeployMode rejects"},
		{"no source at all", "123456",
			steam.GameInfo{Slug: "g", Name: "G", ModPath: "Data"}, "names no source"},
		{"a source nothing registers", "123456",
			steam.GameInfo{Slug: "g", Name: "G", ModPath: "Data", Sources: map[string]string{"modrinth": "g"}},
			"no built-in source registers"},
		{"a source mapped to nothing", "123456",
			steam.GameInfo{Slug: "g", Name: "G", ModPath: "Data", Sources: map[string]string{"nexusmods": ""}},
			"empty game id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkKnownGame(tt.appID, tt.info, registered)
			if tt.want == "" {
				assert.Empty(t, problems)
				return
			}
			require.Len(t, problems, 1, "%v", problems)
			assert.Contains(t, problems[0], tt.want)
		})
	}
}

// TestKnownGamesListIsWellFormed is the ratchet: it runs the rules over the
// list lmm actually ships, against the source ids lmm actually registers.
// Adding an entry that breaks any of them fails the build here rather than
// on somebody's machine.
func TestKnownGamesListIsWellFormed(t *testing.T) {
	sandboxHome(t)

	registered := make(map[string]bool, len(builtinSourceIDs))
	for _, id := range builtinSourceIDs {
		registered[id] = true
	}

	// A sandboxed config dir, so a steam-games.yaml on the machine running
	// the suite cannot add entries to (or hide entries from) the check.
	games, err := steam.LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, games)

	slugOwner := map[string]string{}
	for appID, info := range games {
		for _, problem := range checkKnownGame(appID, info, registered) {
			t.Error(problem)
		}
		if other, taken := slugOwner[info.Slug]; taken {
			t.Errorf("slug %q is used by both app id %s and app id %s", info.Slug, other, appID)
		}
		slugOwner[info.Slug] = appID
	}
}

// sandboxHome points HOME and every XDG variable this project reads at a
// throwaway directory, so nothing here can reach the developer's real
// config, data or Steam library.
func sandboxHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv("XDG_STATE_HOME", home+"/state")
	t.Setenv("STEAM_ROOT", home+"/steam")
}
