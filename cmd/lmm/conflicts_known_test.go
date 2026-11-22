package main

// #668: known (intended) conflicts in the CLI - `lmm conflicts` folds the
// known ones into a count (--all lists them), `lmm conflicts ack/unack`
// marks and unmarks a pair, and `ack --all-current` accepts every present
// conflict as it stands.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedKnownConflictsFixture is the twin conflict (Mod B over Mod A on
// shared.esp) plus Mod C, which also ships other.esp with Mod B.
func seedKnownConflictsFixture(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	seedConflictMod(t, svc, game, "a", "Mod A", true, map[string][]byte{"shared.esp": []byte("A")})
	seedConflictMod(t, svc, game, "b", "Mod B", true, map[string][]byte{"shared.esp": []byte("B"), "other.esp": []byte("B")})
	seedConflictMod(t, svc, game, "c", "Mod C", true, map[string][]byte{"other.esp": []byte("C")})
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
}

// runConflictsCmd runs one lmm command line and returns its stdout and
// error, resetting the flags an earlier line in the same test set.
func runConflictsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cfg, data := configDir, dataDir
	resetChangedFlags(rootCmd)
	configDir, dataDir = cfg, data
	rootCmd.SetArgs(args)
	var runErr error
	out := captureStdout(t, func() error {
		runErr = rootCmd.ExecuteContext(context.Background())
		return nil
	})
	return out, runErr
}

func TestConflictsAck_FoldsTheKnownConflictIntoACount(t *testing.T) {
	restoreFlags(t)
	svc, game := setupConflictsCmdTest(t)
	seedKnownConflictsFixture(t, svc, game)
	require.NoError(t, svc.Close())

	out, err := runConflictsCmd(t, "conflicts", "ack", "b", "a", "--game", game.ID)
	require.NoError(t, err)
	assert.Equal(t, "Marked as known: src:b overrides src:a\n", out)

	out, err = runConflictsCmd(t, "conflicts", "--game", game.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "Found 1 conflicting file(s):")
	assert.Contains(t, out, "other.esp")
	assert.NotContains(t, out, "shared.esp", "the known conflict is not listed")
	assert.Contains(t, out, "1 known conflict not shown (--all lists them).")

	out, err = runConflictsCmd(t, "conflicts", "--game", game.ID, "--all")
	require.NoError(t, err)
	assert.Contains(t, out, "Known conflicts (1), marked as intended:")
	assert.Contains(t, out, "shared.esp")
	assert.NotContains(t, out, "not shown")
}

func TestConflictsAck_AllCurrentAndUnack(t *testing.T) {
	restoreFlags(t)
	svc, game := setupConflictsCmdTest(t)
	seedKnownConflictsFixture(t, svc, game)
	require.NoError(t, svc.Close())

	out, err := runConflictsCmd(t, "conflicts", "ack", "--all-current", "--game", game.ID)
	require.NoError(t, err)
	assert.Equal(t, "Marked as known: src:c overrides src:b\nMarked as known: src:b overrides src:a\n", out)

	out, err = runConflictsCmd(t, "conflicts", "--game", game.ID)
	require.NoError(t, err)
	assert.Equal(t, "No unresolved conflicts.\n2 known conflicts not shown (--all lists them).\n", out)

	out, err = runConflictsCmd(t, "conflicts", "unack", "src:a", "src:b", "--game", game.ID)
	require.NoError(t, err)
	assert.Equal(t, "Unmarked: src:b overrides src:a\n", out, "either way round names the pair")

	out, err = runConflictsCmd(t, "conflicts", "unack", "a", "b", "--game", game.ID)
	require.NoError(t, err)
	assert.Equal(t, "Nothing to unmark: no known conflict between a and b.\n", out)
}

func TestConflictsAck_JSONAndRefusals(t *testing.T) {
	restoreFlags(t)
	svc, game := setupConflictsCmdTest(t)
	seedKnownConflictsFixture(t, svc, game)
	require.NoError(t, svc.Close())

	out, err := runConflictsCmd(t, "conflicts", "ack", "b", "a", "--game", game.ID, "--json")
	require.NoError(t, err)
	var result core.KnownConflictsResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:b", Loser: "src:a", Origin: domain.KnownConflictUser}}, result.Added)

	out, err = runConflictsCmd(t, "conflicts", "--game", game.ID, "--json")
	require.NoError(t, err)
	var report core.ConflictReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Conflicts, 2, "--json always carries every conflict, known or not")
	known := map[string]bool{}
	for _, c := range report.Conflicts {
		known[c.Path] = c.Known
	}
	assert.Equal(t, map[string]bool{"shared.esp": true, "other.esp": false}, known)

	_, err = runConflictsCmd(t, "conflicts", "ack", "b", "--game", game.ID)
	assert.Error(t, err, "a pair needs two mods")
	_, err = runConflictsCmd(t, "conflicts", "ack", "b", "a", "--all-current", "--game", game.ID)
	assert.Error(t, err, "--all-current names no pair")
	_, err = runConflictsCmd(t, "conflicts", "ack", "b", "zzz", "--game", game.ID)
	assert.ErrorIs(t, err, core.ErrModNotInProfile)
}
