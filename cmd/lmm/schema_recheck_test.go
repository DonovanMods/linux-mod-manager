package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportError_JSON_SchemaUpgradedError pins #648's refusal in --json:
// the remedy is the sentence, and the code and both schema versions are
// data, so a script tells it from every other failure without parsing.
func TestReportError_JSON_SchemaUpgradedError(t *testing.T) {
	withJSONOutput(t)

	err := &core.SchemaUpgradedError{Code: core.SchemaTooNewCode, Version: 29, Supported: 28}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"another, newer lmm upgraded the database (schema version 29; this lmm reads up to 28); restart this lmm (for the web UI: `lmm service install` or restart lmm serve on the newer binary)\",\n"+
		"  \"details\": {\n"+
		"    \"code\": \"schema_too_new\",\n"+
		"    \"version\": 29,\n"+
		"    \"supported\": 28\n"+
		"  }\n"+
		"}\n", out)
}

// TestCommandWaitingOnTheLock_RefusesAfterANewerLMMMigrated is #648 in the
// terminal: a command that opened the database and then waited on the
// mutation lock while a newer lmm migrated the same file refuses once it
// gets the lock, in both output modes, instead of writing to a schema it
// does not know.
func TestCommandWaitingOnTheLock_RefusesAfterANewerLMMMigrated(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "json"}[asJSON], func(t *testing.T) {
			configDir = t.TempDir()
			dataDir = t.TempDir()
			oldGameID, oldJSON := gameID, jsonOutput
			gameID, jsonOutput = "", false
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				resetChangedFlags(rootCmd)
				gameID, jsonOutput = oldGameID, oldJSON
			})

			opts := app.Options{ConfigDir: configDir, DataDir: dataDir, WarnWriter: &strings.Builder{}}
			svc, err := app.Open(context.Background(), opts)
			require.NoError(t, err)
			root := t.TempDir()
			require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{ID: "g1", Name: "Game", InstallPath: root, ModPath: root}))
			require.NoError(t, svc.Close())

			// Another lmm holds the mutation lock.
			paths, err := app.ResolvePaths(opts)
			require.NoError(t, err)
			holder, err := os.OpenFile(app.OpLockPath(paths), os.O_RDWR|os.O_CREATE, 0o600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = holder.Close() })
			require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))

			args := []string{"game", "set-default", "g1"}
			if asJSON {
				args = append(args, "--json")
			}
			restoreFlags(t)
			cfgDir, datDir := configDir, dataDir
			resetChangedFlags(rootCmd)
			configDir, dataDir = cfgDir, datDir
			rootCmd.SetArgs(args)

			type outcome struct {
				stdout string
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				var runErr error
				stdout, _, _ := captureStdoutAndStderr(t, func() error {
					runErr = rootCmd.ExecuteContext(context.Background())
					if runErr != nil {
						reportError(runErr)
					}
					return nil
				})
				done <- outcome{stdout, runErr}
			}()

			// While it waits, a newer lmm migrates the database; then the
			// holder lets go.
			waitForLockWaiter(t, app.OpLockPath(paths))
			newer := migrateAsNewerLMM(t, filepath.Join(paths.DataDir, "lmm.db"))
			require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_UN))

			var got outcome
			select {
			case got = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("the waiting command never returned")
			}
			require.ErrorIs(t, got.err, core.ErrSchemaTooNew)
			var upgraded *core.SchemaUpgradedError
			require.ErrorAs(t, got.err, &upgraded, "refused by the re-check after the wait, not by an open")
			assert.Equal(t, newer, upgraded.Version)
			if asJSON {
				var doc struct {
					Error   string         `json:"error"`
					Details map[string]any `json:"details"`
				}
				require.NoError(t, json.Unmarshal([]byte(got.stdout), &doc), "one error document: %q", got.stdout)
				assert.Contains(t, doc.Error, "restart this lmm")
				assert.Equal(t, core.SchemaTooNewCode, doc.Details["code"])
			} else {
				assert.Contains(t, got.err.Error(), "another, newer lmm upgraded the database")
			}

			// Nothing was written: the default game is still unset.
			cfg, _ := os.ReadFile(filepath.Join(configDir, "config.yaml"))
			assert.NotContains(t, string(cfg), "g1", "a refused command writes nothing")
		})
	}
}

// migrateAsNewerLMM records, through a database handle of its own, one
// migration past what this lmm knows - a newer lmm upgrading the file. It
// returns the version recorded.
func migrateAsNewerLMM(t *testing.T, path string) int {
	t.Helper()
	other, err := db.New(path)
	require.NoError(t, err)
	version := other.SupportedSchemaVersion() + 1
	_, err = other.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version)
	require.NoError(t, err)
	require.NoError(t, other.Close())
	return version
}

// waitForLockWaiter returns once a second descriptor in this process has
// path open - the command, past its open, polling the lock the test holds.
// A sleep would race a slow open: one that finished after the migration
// would be refused by the open instead of the re-check under test.
func waitForLockWaiter(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/proc/self/fd")
		require.NoError(t, err)
		open := 0
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == path {
				open++
			}
		}
		if open >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the command never started waiting on the lock")
}

// TestReportError_JSON_SchemaTooNewError pins the open-time refusal's
// --json envelope (#648 review): its sentence as ever, and the same
// details as the running refusal, so a script branches on one code.
func TestReportError_JSON_SchemaTooNewError(t *testing.T) {
	withJSONOutput(t)

	err := &core.SchemaTooNewError{
		Code: core.SchemaTooNewCode, Version: 29, Supported: 28,
		Err: &db.SchemaTooNewError{Path: "/home/u/.local/share/lmm/lmm.db", Version: 29, Supported: 28},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"opening database: the database at /home/u/.local/share/lmm/lmm.db was written by a newer lmm (schema version 29; this lmm reads up to 28): upgrade lmm to open it\",\n"+
		"  \"details\": {\n"+
		"    \"code\": \"schema_too_new\",\n"+
		"    \"version\": 29,\n"+
		"    \"supported\": 28\n"+
		"  }\n"+
		"}\n", out)
}

// TestCLI_ADatabaseFromANewerLMMCarriesTheCode: a real command against an
// already-too-new database reports that envelope under --json.
func TestCLI_ADatabaseFromANewerLMMCarriesTheCode(t *testing.T) {
	setupAdapterWarningGames(t)
	newer := migrateAsNewerLMM(t, filepath.Join(dataDir, "lmm.db"))

	restoreFlags(t)
	cfgDir, datDir := configDir, dataDir
	resetChangedFlags(rootCmd)
	configDir, dataDir = cfgDir, datDir
	rootCmd.SetArgs([]string{"status", "--json"})
	var runErr error
	stdout, _, _ := captureStdoutAndStderr(t, func() error {
		runErr = rootCmd.ExecuteContext(context.Background())
		if runErr != nil {
			reportError(runErr)
		}
		return nil
	})
	require.ErrorIs(t, runErr, core.ErrSchemaTooNew)

	var doc struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc), "one error document: %q", stdout)
	assert.Contains(t, doc.Error, "was written by a newer lmm")
	assert.Equal(t, core.SchemaTooNewCode, doc.Details["code"])
	assert.EqualValues(t, newer, doc.Details["version"])
	assert.EqualValues(t, newer-1, doc.Details["supported"])
}
