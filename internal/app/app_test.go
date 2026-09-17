package app

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openForTest(t *testing.T, opts Options) {
	t.Helper()
	svc, err := Open(t.Context(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	// Built-ins are registered unconditionally.
	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "Nexus Mods", src.Name())
}

// TestOpen_DataDirIsOwnerOnly pins that the data directory is created 0700:
// it holds lmm.db (auth tokens in plaintext) and the downloads staging root.
func TestOpen_DataDirIsOwnerOnly(t *testing.T) {
	// Nest under the temp dir so Open does the creating — t.TempDir() itself
	// is already 0700, which would make the assertion vacuous.
	dataDir := filepath.Join(t.TempDir(), "lmm")
	openForTest(t, Options{ConfigDir: t.TempDir(), DataDir: dataDir})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "data dir must not be group- or world-readable")
}

// TestOpen_TightensExistingDataDir covers installs predating the 0700 rule:
// MkdirAll is a no-op on an existing directory, so a legacy 0755 data dir
// stays permissive unless explicitly tightened.
func TestOpen_TightensExistingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.MkdirAll(dataDir, 0755))
	openForTest(t, Options{ConfigDir: t.TempDir(), DataDir: dataDir})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "an existing permissive data dir should be tightened")
}

// TestOpen_CreatesConfigAndCacheDirs pins that a fresh install gets its whole
// layout created, cache included.
func TestOpen_CreatesConfigAndCacheDirs(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "cfg", "lmm")
	dataDir := filepath.Join(root, "data", "lmm")
	openForTest(t, Options{ConfigDir: cfgDir, DataDir: dataDir})

	for _, dir := range []string{cfgDir, dataDir, filepath.Join(dataDir, "cache")} {
		info, err := os.Stat(dir)
		require.NoError(t, err, dir)
		assert.True(t, info.IsDir(), dir)
	}
}

// TestOpen_AlreadyCancelledContext pins that a cancelled ctx aborts Open
// before any directory creation or service work, returning ctx.Err() rather
// than opening successfully or failing later with an unrelated error.
func TestOpen_AlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, Options{ConfigDir: t.TempDir(), DataDir: t.TempDir()})
	require.ErrorIs(t, err, context.Canceled)
}

// TestOpen_BackfillsProfileDisabledMarkers pins the call site of #431's
// one-time profile-document backfill, end to end through an upgrade: a
// database an older lmm wrote (the rows exist before migration v17 runs), a
// profile document with no key for a mod the database says is off, and the
// first open of the new binary. It has to run where an installation is
// opened: every converge flow READS the document, so a mod disabled before
// the marker existed would otherwise be switched back on by the first switch
// or apply after the upgrade. The notice goes to the caller's WarnWriter.
func TestOpen_BackfillsProfileDisabledMarkers(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), filepath.Join(t.TempDir(), "lmm")

	// Built through core directly, NOT through Open - opening is what
	// discharges the obligation, so the state has to exist before the
	// first one.
	paths, err := ResolvePaths(Options{ConfigDir: cfgDir, DataDir: dataDir})
	require.NoError(t, err)
	require.NoError(t, ensureDirs(paths))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: paths.ConfigDir, DataDir: paths.DataDir, CacheDir: paths.CacheDir,
	})
	require.NoError(t, err)
	pm := svc.NewProfileManager()
	_, err = pm.Create(t.Context(), "g1", "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(t.Context(), "g1", "default"))
	require.NoError(t, pm.AddMod(t.Context(), "g1", "default", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "off", SourceID: "src", Name: "Off Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
		Deployed:     false,
	}))
	require.NoError(t, svc.Close())

	// Back to the schema the older lmm left, so the next open migrates.
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	_, err = conn.Exec("DELETE FROM schema_migrations WHERE version >= 17")
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	var warnings bytes.Buffer
	svc, err = Open(t.Context(), Options{ConfigDir: cfgDir, DataDir: dataDir, WarnWriter: &warnings})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	profile, err := svc.NewProfileManager().Get(t.Context(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Disabled,
		"opening an upgraded installation must record what the database already says is off")
	assert.Contains(t, warnings.String(), "Off Mod")
	assert.Contains(t, warnings.String(), "lmm mod enable off --game g1 --source src --profile default")

	// And only once.
	warnings.Reset()
	require.NoError(t, svc.Close())
	svc, err = Open(t.Context(), Options{ConfigDir: cfgDir, DataDir: dataDir, WarnWriter: &warnings})
	require.NoError(t, err)
	assert.Empty(t, warnings.String())
}

// TestOpen_AConfigFileTheDecoderCannotReadIsAnError is #452 at startup: a
// hand-edited games.yaml or config.yaml holding a construct the YAML decoder
// once panicked on used to crash every command - `lmm serve` included -
// before it did anything. Open reports the file instead.
func TestOpen_AConfigFileTheDecoderCannotReadIsAnError(t *testing.T) {
	for _, name := range []string{"games.yaml", "config.yaml"} {
		t.Run(name, func(t *testing.T) {
			cfgDir := t.TempDir()
			path := filepath.Join(cfgDir, name)
			require.NoError(t, os.WriteFile(path, []byte("<<:\n? 0:"), 0o644))

			var err error
			require.NotPanics(t, func() {
				var svc *core.Service
				svc, err = Open(t.Context(), Options{ConfigDir: cfgDir, DataDir: t.TempDir()})
				if svc != nil {
					require.NoError(t, svc.Close())
				}
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), path)
		})
	}
}
