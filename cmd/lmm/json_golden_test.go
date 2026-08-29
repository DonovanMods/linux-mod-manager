package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateJSONCLIGoldens re-records the CLI's --json goldens. Run ONCE per
// command, in the task that switches that command onto its core type:
//
//	go test ./cmd/lmm -run TestJSONGolden_List -update-json-cli
//
// After that the files are frozen: they are the v2 JSON contract (Ruling 3 -
// "JSON shapes change once"), so a later shape drift shows up as a diff here
// instead of silently reaching a consumer. Named -update-json-cli rather than
// -update: verify_golden_test.go already registers a package-level "-update"
// and mod_show_golden_test.go an "-update-modshow", and Go's flag package
// panics on a duplicate registration within one test binary.
var updateJSONCLIGoldens = flag.Bool("update-json-cli", false, "re-record cmd/lmm/testdata/json_golden/*.golden")

// recordJSONLegacy writes each scenario's CURRENT output to
// testdata/json_legacy/<name>.json instead of comparing it, capturing the
// pre-v2 shapes as a review reference. Recorded once, before the switch;
// deleted at phase close (Unit S).
var recordJSONLegacy = flag.Bool("record-json-legacy", false, "record cmd/lmm/testdata/json_legacy/*.json (pre-v2 shapes)")

// volatileTime matches an RFC 3339 timestamp anywhere in a document. Several
// core types carry real clock values (installed_at, last_deploy, updated_at),
// so goldens would drift every run without this; the substitution keeps the
// KEY pinned - what a golden is actually for - while dropping the value.
var volatileTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)

// zeroTime is Go's zero time.Time on the wire. It is NOT volatile - it is
// exactly what a never-set timestamp field must serialize as - so it is
// parked before the volatileTime pass and restored afterwards, keeping the
// "unset" signal visible in the goldens.
const zeroTime = "0001-01-01T00:00:00Z"

// scrubJSON makes a captured document byte-stable: every RFC 3339 timestamp
// becomes "<TIME>", and each subs pair (old, new) replaces a per-run path
// (t.TempDir()) with a fixed placeholder. subs is applied first so a path
// containing digits can never be half-eaten by the timestamp rule.
func scrubJSON(actual string, subs ...string) string {
	if len(subs)%2 != 0 {
		panic("scrubJSON: subs must be (old, new) pairs")
	}
	for i := 0; i < len(subs); i += 2 {
		actual = strings.ReplaceAll(actual, subs[i], subs[i+1])
	}
	actual = strings.ReplaceAll(actual, zeroTime, "<ZERO-TIME>")
	actual = volatileTime.ReplaceAllString(actual, "<TIME>")
	return strings.ReplaceAll(actual, "<ZERO-TIME>", zeroTime)
}

// assertJSONCLIGolden compares one command's --json document against its
// recorded golden (or records it under -update-json-cli / captures the
// pre-v2 shape under -record-json-legacy).
func assertJSONCLIGolden(t *testing.T, name, actual string, subs ...string) {
	t.Helper()
	scrubbed := scrubJSON(actual, subs...)

	if *recordJSONLegacy {
		path := filepath.Join("testdata", "json_legacy", name+".json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(scrubbed), 0o644))
		return
	}

	path := filepath.Join("testdata", "json_golden", name+".golden")
	if *updateJSONCLIGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(scrubbed), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-json-cli", path)
	assert.Equal(t, string(want), scrubbed, "--json output drifted from the recorded v2 contract")
}

// --- list ---

func TestJSONGolden_List(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

		assertJSONCLIGolden(t, "list_populated", listVerbose(t, svc, game, true))
	})

	t.Run("empty", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)

		assertJSONCLIGolden(t, "list_empty", listVerbose(t, svc, game, true))
	})

	t.Run("profiles", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		_, err := svc.NewProfileManager().Create(game.ID, "survival")
		require.NoError(t, err)

		old := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = old })

		out := captureStdout(t, func() error {
			return runListProfiles(&cobra.Command{}, svc, game.ID, game.Name)
		})
		assertJSONCLIGolden(t, "list_profiles", out)
	})
}

// --- status ---

// goldenStatusGame is a fully-populated game whose every path is a literal,
// so a status golden never has to scrub one.
func goldenStatusGame(id, name string) *domain.Game {
	return &domain.Game{
		ID: id, Name: name,
		InstallPath: "/games/" + id,
		ModPath:     "/games/" + id + "/Mods",
		CachePath:   "/cache/" + id,
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{"src": id},
	}
}

func TestJSONGolden_Status(t *testing.T) {
	withStatusFlags := func(t *testing.T, id string) {
		t.Helper()
		oldGame, oldJSON, oldVerbose := gameID, jsonOutput, verbose
		gameID, jsonOutput, verbose = id, true, false
		t.Cleanup(func() { gameID, jsonOutput, verbose = oldGame, oldJSON, oldVerbose })
	}

	t.Run("summary", func(t *testing.T) {
		svc := setupGameAddTest(t)
		withStatusFlags(t, "")
		for _, g := range []*domain.Game{goldenStatusGame("zulu", "Zulu"), goldenStatusGame("alpha", "Alpha")} {
			require.NoError(t, svc.SaveGame(context.Background(), g))
			_, err := svc.NewProfileManager().Create(g.ID, "default")
			require.NoError(t, err)
		}

		out := captureStdout(t, func() error { return doStatus(context.Background(), svc) })
		assertJSONCLIGolden(t, "status_summary", out)
	})

	t.Run("no_games", func(t *testing.T) {
		svc := setupGameAddTest(t)
		withStatusFlags(t, "")

		out := captureStdout(t, func() error { return doStatus(context.Background(), svc) })
		assertJSONCLIGolden(t, "status_no_games", out)
	})

	t.Run("game_detail", func(t *testing.T) {
		svc := setupGameAddTest(t)
		withStatusFlags(t, "alpha")
		game := goldenStatusGame("alpha", "Alpha")
		require.NoError(t, svc.SaveGame(context.Background(), game))
		pm := svc.NewProfileManager()
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
		_, err = pm.Create(game.ID, "survival")
		require.NoError(t, err)

		out := captureStdout(t, func() error {
			return showGameStatusJSON(context.Background(), svc, game.ID)
		})
		assertJSONCLIGolden(t, "status_game_detail", out)
	})
}
