package core

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestService_logger_NilLogFieldReturnsDiscard pins review finding Important
// #2: a Service built by a raw struct literal (bypassing NewService, as five
// existing white-box tests in this package already do) has a nil log field,
// and logger() must still hand back a usable, discarding *slog.Logger rather
// than a caller dereferencing nil.
func TestService_logger_NilLogFieldReturnsDiscard(t *testing.T) {
	svc := &Service{}

	log := svc.logger()

	require.NotNil(t, log)
	assert.False(t, log.Enabled(context.Background(), slog.LevelError), "nil log field must mean discard")
}

// TestService_lockedInstallRefusal_RawServiceDoesNotPanic pins the same
// finding against the actual call path: a Service reached via a raw struct
// literal, calling lockedInstallRefusal with a ProfileManager that fails to
// load the profile, must not panic on the nil log field.
func TestService_lockedInstallRefusal_RawServiceDoesNotPanic(t *testing.T) {
	svc := &Service{configDir: t.TempDir()}
	plan := &InstallPlan{GameID: "game1", Profile: "missing"}

	var err error
	require.NotPanics(t, func() {
		err = svc.lockedInstallRefusal(context.Background(), plan, InstallOptions{})
	})
	assert.NoError(t, err, "a missing profile cannot hold a lock")
}

// TestService_lockedInstallRefusal_LogLevelByErrorKind pins review finding
// Important #1: domain.ErrProfileNotFound is a tolerated, everyday outcome
// (any profile that hasn't been materialized as a YAML file yet) and must
// log at Debug, not Warn - the prior behavior fired a spurious "profile load
// failed" warning on ordinary first-time installs. Any other error reading
// the profile is a genuine fault and stays at Warn.
func TestService_lockedInstallRefusal_LogLevelByErrorKind(t *testing.T) {
	plan := &InstallPlan{GameID: "game1", Profile: "default"}

	t.Run("profile not found logs at debug, not warn", func(t *testing.T) {
		var buf bytes.Buffer
		svc := &Service{
			configDir: t.TempDir(),
			log:       slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		}

		err := svc.lockedInstallRefusal(context.Background(), plan, InstallOptions{})

		require.NoError(t, err)
		assert.Contains(t, buf.String(), `level=DEBUG msg="profile not found while checking lock"`)
		assert.NotContains(t, buf.String(), "level=WARN")
	})

	t.Run("other profile load errors log at warn", func(t *testing.T) {
		var buf bytes.Buffer
		configDir := t.TempDir()
		// A directory where the profile file is expected forces LoadProfile
		// into its generic "reading profile" branch instead of the
		// os.ErrNotExist -> domain.ErrProfileNotFound branch - a real,
		// self-contained stand-in for a disk/permission fault.
		profilePath := filepath.Join(configDir, "games", plan.GameID, "profiles", plan.Profile+".yaml")
		require.NoError(t, os.MkdirAll(profilePath, 0755))

		svc := &Service{
			configDir: configDir,
			log:       slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		}

		err := svc.lockedInstallRefusal(context.Background(), plan, InstallOptions{})

		require.NoError(t, err)
		assert.Contains(t, buf.String(), `level=WARN msg="profile load failed while checking lock"`)
		assert.NotContains(t, buf.String(), "level=DEBUG")
	})
}
