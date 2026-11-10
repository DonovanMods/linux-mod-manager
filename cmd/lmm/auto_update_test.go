package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auto_update_test.go - #586's CLI half: a command whose release check
// knows of a newer release starts a detached `lmm self-update --auto`,
// and the command after an automatic update announces it, once.

// spawnRecorder replaces spawnAutoUpdate for one test, recording each start
// instead of making it.
type spawnRecorder struct {
	exe  string
	args [][]string
	logs []string
}

func recordSpawns(t *testing.T) *spawnRecorder {
	t.Helper()
	rec := &spawnRecorder{}
	old := spawnAutoUpdate
	spawnAutoUpdate = func(exe string, args []string, logPath string) error {
		rec.exe = exe
		rec.args = append(rec.args, args)
		rec.logs = append(rec.logs, logPath)
		return nil
	}
	t.Cleanup(func() { spawnAutoUpdate = old })
	return rec
}

func TestAutoUpdate_ACommandStartsTheDetachedUpdate(t *testing.T) {
	cliReleaseServer(t, "v2.1.0")
	armReleaseNotice(t, true)
	spawns := recordSpawns(t)

	notice := afterCommand(t, "game", "list")
	require.Len(t, spawns.args, 1, notice)
	assert.Equal(t, []string{"self-update", "--auto", "--config", configDir, "--data", dataDir}, spawns.args[0],
		"the run uses this command's directories")
	assert.Equal(t, filepath.Join(dataDir, "auto-update.log"), spawns.logs[0])
	assert.Contains(t, notice, "lmm 2.1.0 is available (you have 2.0.0); lmm is installing it in the background\n")

	notice = afterCommand(t, "game", "list")
	assert.Len(t, spawns.args, 1, "the start holds off the next command's for an hour")
	assert.Contains(t, notice, "lmm 2.1.0 is available (you have 2.0.0): https://", "the plain line until it lands")
}

func TestAutoUpdate_NoStartWhenTurnedOffOrNotApplicable(t *testing.T) {
	t.Run("auto_update: false", func(t *testing.T) {
		cliReleaseServer(t, "v2.1.0")
		armReleaseNotice(t, true)
		spawns := recordSpawns(t)
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("auto_update: false\n"), 0o644))
		notice := afterCommand(t, "game", "list")
		assert.Empty(t, spawns.args)
		assert.Contains(t, notice, "lmm 2.1.0 is available (you have 2.0.0): https://")
	})
	t.Run("a package install", func(t *testing.T) {
		cliReleaseServer(t, "v2.1.0")
		releaseInstallProbe.Executable = func() (string, error) { return "/usr/bin/lmm", nil }
		releaseInstallProbe.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
		releaseInstallProbe.Run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "pacman" {
				return []byte("lmm\n"), nil
			}
			return nil, os.ErrNotExist
		}
		armReleaseNotice(t, true)
		spawns := recordSpawns(t)
		notice := afterCommand(t, "game", "list")
		assert.Empty(t, spawns.args, "a package's lmm is never auto-updated")
		assert.Contains(t, notice, "upgrade with: sudo pacman -Syu")
	})
	t.Run("not a terminal", func(t *testing.T) {
		hits := cliReleaseServer(t, "v2.1.0")
		armReleaseNotice(t, false)
		spawns := recordSpawns(t)
		assert.Empty(t, afterCommand(t, "game", "list"))
		assert.Empty(t, spawns.args, "a script never has lmm replaced under it")
		assert.Zero(t, hits.Load())
	})
	t.Run("up to date", func(t *testing.T) {
		cliReleaseServer(t, "v2.0.0")
		armReleaseNotice(t, true)
		spawns := recordSpawns(t)
		afterCommand(t, "game", "list")
		assert.Empty(t, spawns.args)
	})
}

// writeAutoUpdateState puts an automatic update on record, as a finished
// `lmm self-update --auto` leaves it.
func writeAutoUpdateState(t *testing.T, first bool) {
	t.Helper()
	at := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC).Format(time.RFC3339)
	firstJSON := "false"
	if first {
		firstJSON = "true"
	}
	doc := `{"last":{"from":"1.9.0","to":"2.0.0","at":"` + at + `","first":` + firstJSON + `},"updates":1}`
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "auto-update.json"), []byte(doc), 0o644))
}

func TestAutoUpdate_TheNextCommandAnnouncesItOnce(t *testing.T) {
	cliReleaseServer(t, "v2.0.0")
	armReleaseNotice(t, true)
	writeAutoUpdateState(t, true)

	notice := afterCommand(t, "game", "list")
	lines := strings.Split(strings.TrimSpace(notice), "\n")
	assert.Equal(t, "lmm updated itself to v2.0.0. To turn this off: auto_update: false in config.yaml, or Setup > Updates.", lines[0])

	notice = afterCommand(t, "game", "list")
	assert.NotContains(t, notice, "updated itself", "told once")
}

func TestAutoUpdate_ALaterUpdateIsOneLine(t *testing.T) {
	cliReleaseServer(t, "v2.0.0")
	armReleaseNotice(t, true)
	writeAutoUpdateState(t, false)
	notice := afterCommand(t, "game", "list")
	assert.Contains(t, notice, "lmm updated itself to v2.0.0.\n")
	assert.NotContains(t, notice, "To turn this off")
}

// lmm-update.timer updates lmm with the release check off too, and that
// update is announced all the same; a script is not told.
func TestAutoUpdate_TheNoticeDoesNotNeedTheReleaseCheck(t *testing.T) {
	hits := cliReleaseServer(t, "v2.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("update_check: false\n"), 0o644))
	writeAutoUpdateState(t, true)

	armReleaseNotice(t, false)
	assert.Empty(t, afterCommand(t, "game", "list"), "not to a script")

	armReleaseNotice(t, true)
	assert.Contains(t, afterCommand(t, "game", "list"), "lmm updated itself to v2.0.0.")
	assert.Zero(t, hits.Load())
}

// startDetached's own contract, with a shell rather than lmm: the child
// runs in its own session with its output in the log, and the caller does
// not wait for it.
func TestStartDetached_RunsInItsOwnSessionIntoTheLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sub", "auto-update.log")
	require.NoError(t, startDetached("/bin/sh", []string{"-c", `[ "$(ps -o sid= -p $$ | tr -d ' ')" = "$$" ] && echo own-session || echo shared-session`}, logPath))
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(logPath)
		return strings.Contains(string(data), "session")
	}, 10*time.Second, 20*time.Millisecond)
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "starting an automatic update")
	assert.Contains(t, string(data), "own-session")
}
