package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoModShow_InstalledSection_NotLocked guards #97/#92: mod show was
// policy-blind (#92's last unshipped row) - an installed, unlocked mod now
// gets an Installed section naming its update policy and "Lock: none".
func TestDoModShow_InstalledSection_NotLocked(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Installed: v1.5 (profile: default)")
	assert.Contains(t, out, "Update policy: notify")
	assert.Contains(t, out, "Lock: none")
}

// TestDoModShow_InstalledSection_Locked guards the locked half: a locked mod
// whose lock target differs from the installed version must name the
// convergence command, and the Lock line's version is the lock's target
// (from the profile ref), not the DB-recorded installed version.
func TestDoModShow_InstalledSection_Locked(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", "1.2.3"))

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Lock: locked at v1.2.3 — run 'lmm profile apply' to converge")
}

// TestDoModShow_Locked_TargetMatchesInstalled_NoConvergeHint: when the lock
// target equals what's actually installed there's nothing to converge, so
// the hint must not appear (mirrors doModLock's own "target == installed"
// case, TestDoModLock_NoVersion_LocksAtCurrentRecordedVersion).
func TestDoModShow_Locked_TargetMatchesInstalled_NoConvergeHint(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Lock: locked at v1.5")
	assert.NotContains(t, out, "converge", "target matches installed version; no convergence hint expected")
}

// TestDoModShow_NotInstalled_OmitsInstalledSection: mod show works for any
// mod on the source, so "not installed" is a normal case, not an error - the
// section must simply not appear.
func TestDoModShow_NotInstalled_OmitsInstalledSection(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.NotContains(t, out, "Installed:")
}

// TestDoModShow_JSON_IncludesInstalledObject guards --json's additive
// installed object: version, profile, update_policy, locked, locked_version.
func TestDoModShow_JSON_IncludesInstalledObject(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", "1.2.3"))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	var decoded struct {
		Installed *struct {
			Version       string `json:"version"`
			Profile       string `json:"profile"`
			UpdatePolicy  string `json:"update_policy"`
			Locked        bool   `json:"locked"`
			LockedVersion string `json:"locked_version"`
		} `json:"installed"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.NotNil(t, decoded.Installed)
	assert.Equal(t, "1.5", decoded.Installed.Version)
	assert.Equal(t, "default", decoded.Installed.Profile)
	assert.Equal(t, "notify", decoded.Installed.UpdatePolicy)
	assert.True(t, decoded.Installed.Locked)
	assert.Equal(t, "1.2.3", decoded.Installed.LockedVersion)
}

// TestDoModShow_JSON_OmitsInstalledWhenNotInstalled guards the negative
// case: the installed object is entirely absent (not present-but-null) when
// the mod isn't installed.
func TestDoModShow_JSON_OmitsInstalledWhenNotInstalled(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.NotContains(t, out, `"installed"`)
}
