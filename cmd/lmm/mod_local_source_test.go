package main

// #447: every `lmm mod` subcommand addresses an `lmm import`ed mod - recorded
// under the `local` source, which no game maps - through ONE resolution
// path (resolveModTarget): `--source local` is accepted everywhere, and a
// bare id resolves to the source the installed mod carries, so the game's
// `sources:` map never has to list `local`.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLocalModTest is setupDoModLockTest (a game mapping only "src") plus
// an imported mod "imp", and the subcommands' flag globals reset.
func setupLocalModTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, _ := setupDoModLockTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedLocalMod(t, svc, game, "imp", "Imported Mod")

	oldAuto, oldNotify, oldPin := modSetAuto, modSetNotify, modSetPin
	t.Cleanup(func() { modSetAuto, modSetNotify, modSetPin = oldAuto, oldNotify, oldPin })
	modSetAuto, modSetNotify, modSetPin = false, false, false
	return svc, game
}

// bySource runs fn once with `--source local` and once with no --source;
// fn's setup runs first, and then passes the flag to set.
func bySource(t *testing.T, fn func(t *testing.T, flag string)) {
	for name, flag := range map[string]string{"--source local": domain.SourceLocal, "no --source": ""} {
		t.Run(name, func(t *testing.T) { fn(t, flag) })
	}
}

func TestModSetUpdate_AddressesAnImportedMod(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game := setupLocalModTest(t)
		modSource = flag
		modSetPin = true
		out := captureStdout(t, func() error { return doModSetUpdate(context.Background(), svc, game, "imp") })
		assert.Contains(t, out, "Imported Mod update policy: pinned")

		mod, err := svc.GetInstalledMod(context.Background(), domain.SourceLocal, "imp", game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, domain.UpdatePinned, mod.UpdatePolicy)
	})
}

// TestModLock_AnImportedModGetsTheRealAnswer: a local mod has no source to
// resolve versions against, so it cannot be locked - and the refusal says
// that, with the --pin remedy, rather than "source not found: local".
func TestModLock_AnImportedModGetsTheRealAnswer(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game := setupLocalModTest(t)
		modSource = flag
		err := doModLock(context.Background(), svc, game, "imp", "")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "not configured")
		assert.NotContains(t, err.Error(), "source not found")
		assert.Contains(t, err.Error(), `source "local" cannot resolve versions`)
		assert.Contains(t, err.Error(), "lmm mod set-update -s local -p default imp --pin")
	})
}

func TestModUnlock_AddressesAnImportedMod(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game := setupLocalModTest(t)
		modSource = flag
		out := captureStdout(t, func() error { return doModUnlock(context.Background(), svc, game, "imp") })
		assert.Contains(t, out, "Imported Mod unlocked")
	})
}

func TestModFiles_AddressesAnImportedMod(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game := setupLocalModTest(t)
		modSource = flag
		out := captureStdout(t, func() error { return doModFiles(context.Background(), svc, game, "imp") })
		assert.Contains(t, out, "Files deployed by Imported Mod (imp)")
	})
}

// TestModShow_AddressesAnImportedMod: no registry source answers for
// `local`, so the detail is the installed row itself.
func TestModShow_AddressesAnImportedMod(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game := setupLocalModTest(t)
		modSource = flag
		out := captureStdout(t, func() error { return doModShow(context.Background(), svc, game, "imp") })
		assert.Contains(t, out, "Imported Mod")
		assert.Contains(t, out, "Installed: v1.0 (profile: default)")
	})
}

func TestModConvert_AddressesAnImportedMod(t *testing.T) {
	bySource(t, func(t *testing.T, flag string) {
		svc, game, _ := setupDoModConvertTest(t)
		modSource = flag
		require.NoError(t, svc.SaveGame(context.Background(), game))
		_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
		require.NoError(t, err)
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:         domain.Mod{ID: "imp", SourceID: domain.SourceLocal, Name: "Imported Pak", Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
			FileIDs: []string{"pak"},
		}))

		captureStdout(t, func() error { return doModConvert(context.Background(), svc, game, "imp", false) })
		mod, err := svc.GetInstalledMod(context.Background(), domain.SourceLocal, "imp", game.ID, "default")
		require.NoError(t, err)
		assert.False(t, mod.ConvertPaks)
	})
}

// TestModEnableDisable_ABareIDNeedsNoLocalMapping is the #447 comment's
// case: with no --source, enable and disable picked the game's configured
// source and never found the imported mod.
func TestModEnableDisable_ABareIDNeedsNoLocalMapping(t *testing.T) {
	svc, game := setupLocalModTest(t)
	modSource = ""
	captureStdout(t, func() error { return doModDisable(context.Background(), svc, game, "imp") })
	mod, err := svc.GetInstalledMod(context.Background(), domain.SourceLocal, "imp", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)

	modSource = ""
	captureStdout(t, func() error { return doModEnable(context.Background(), svc, game, "imp") })
	mod, err = svc.GetInstalledMod(context.Background(), domain.SourceLocal, "imp", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
}

// TestModSubcommands_ABareIDUnderTwoSourcesIsRefused: a bare id installed
// under two sources is never guessed at (#373's rule, as `lmm uninstall`
// applies it).
func TestModSubcommands_ABareIDUnderTwoSourcesIsRefused(t *testing.T) {
	svc, game := setupLocalModTest(t)
	seedLockableMod(t, svc, game, "imp", "Remote Namesake", "2.0")
	modSource = ""

	for name, run := range map[string]func() error{
		"set-update": func() error { modSetPin = true; return doModSetUpdate(context.Background(), svc, game, "imp") },
		"unlock":     func() error { return doModUnlock(context.Background(), svc, game, "imp") },
		"show":       func() error { return doModShow(context.Background(), svc, game, "imp") },
		"disable":    func() error { return doModDisable(context.Background(), svc, game, "imp") },
	} {
		t.Run(name, func(t *testing.T) {
			modSource = ""
			err := run()
			var ambiguous *core.AmbiguousModError
			require.ErrorAs(t, err, &ambiguous)
			assert.Equal(t, []string{domain.SourceLocal, "src"}, ambiguous.Sources)
			assert.Equal(t, "-s/--source", ambiguous.Flag)
		})
	}
}
