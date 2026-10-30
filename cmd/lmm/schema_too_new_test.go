package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
)

// #516's downgrade guard at the frontends: a database a newer lmm migrated
// is refused by every command that opens the service - `lmm serve` before
// it listens - with the sentence that says what to do, not a later "no
// such column".
func TestCLI_ADatabaseFromANewerLMMIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"status"},
		{"status", "--json"},
		{"serve", "--addr", "127.0.0.1:0"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			setupAdapterWarningGames(t)
			svc, err := app.Open(context.Background(), app.Options{ConfigDir: configDir, DataDir: dataDir, WarnWriter: &strings.Builder{}})
			require.NoError(t, err)
			require.NoError(t, svc.Close())
			conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
			require.NoError(t, err)
			_, err = conn.Exec("INSERT INTO schema_migrations (version) SELECT MAX(version) + 1 FROM schema_migrations")
			require.NoError(t, err)
			require.NoError(t, conn.Close())

			rootCmd.SetArgs(args)
			_, _, err = captureStdoutAndStderr(t, func() error { return rootCmd.ExecuteContext(context.Background()) })
			require.Error(t, err)
			assert.Contains(t, err.Error(), "was written by a newer lmm")
			assert.Contains(t, err.Error(), "upgrade lmm to open it")
			assert.NotContains(t, err.Error(), "running migrations", "a refusal, not a failed migration")
		})
	}
}
