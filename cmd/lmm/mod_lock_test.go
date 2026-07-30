package main

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupDoModLockTest builds a *core.Service and a game configured for
// fakeInstallSource (cmd/lmm/install_test.go - a real, registered
// source.ModSource that implements GetModFiles but no CapabilityReporter, so
// source.CapabilitiesOf's fully-capable default applies, including
// Versions: true), and resets lock/unlock's package-level flag globals -
// mirrors setupDoInstallTest/setupDoDeployTest's pattern. Callers seed their
// own installed mods/profile refs via seedLockableMod.
func setupDoModLockTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeInstallSource("src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "g1"},
	}

	oldSource, oldProfile := modSource, modProfile
	modSource = "src"
	modProfile = ""
	t.Cleanup(func() { modSource, modProfile = oldSource, oldProfile })

	return svc, game, src
}

// seedLockableMod records modID as installed (DB) at version and adds a
// matching ModReference to the "default" profile (YAML) - the two records
// doModLock/doModUnlock each read from. Mirrors deploy_test.go's
// seedDeployableMod, minus the cache/deploy bits lock doesn't need.
func seedLockableMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, version string) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: version}))
}

// noVersionsCapSource is a real, registered ModSource that explicitly
// declares Versions: false (#97 static capability gate) - mirrors
// search_test.go's noSearchCapSource for the same "real source with one
// capability turned off" shape.
type noVersionsCapSource struct{ id string }

func (s *noVersionsCapSource) ID() string      { return s.id }
func (s *noVersionsCapSource) Name() string    { return s.id }
func (s *noVersionsCapSource) AuthURL() string { return "" }
func (s *noVersionsCapSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *noVersionsCapSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: false}
}
func (s *noVersionsCapSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *noVersionsCapSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (s *noVersionsCapSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *noVersionsCapSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("should never be called: capability gate must reject before this")
}
func (s *noVersionsCapSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *noVersionsCapSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// TestDoModLock_ExplicitVersion_WritesLockedAndVersionToProfile guards (a):
// locking at an explicit, upstream-valid version must both set the Locked
// marker and move the profile ref's Version to the resolved target, and must
// name the convergence command since the target (2.0) differs from what's
// actually installed (1.0).
func TestDoModLock_ExplicitVersion_WritesLockedAndVersionToProfile(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID}, []domain.DownloadableFile{
		{ID: "f1", Version: "1.0", Category: "MAIN"},
		{ID: "f2", Version: "2.0", Category: "MAIN"},
	})

	out := captureStdout(t, func() error {
		return doModLock(context.Background(), svc, game, "a", "2.0")
	})

	assert.Contains(t, out, "✓ Mod A locked at v2.0")
	assert.Contains(t, out, "Installed version is v1.0 — run 'lmm profile apply' (or 'lmm deploy') to converge.")

	profile, err := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Locked)
	assert.Equal(t, "2.0", profile.Mods[0].Version)
}

// TestDoModLock_ExplicitVersion_MapsGameIDPerSourceMapping guards a scoped
// PR review finding (Copilot, PR #142): doModLock's version-resolution call
// (service.ResolveModVersion(ctx, modSource, &mod.Mod, version)) passes
// mod.Mod straight from the DB, whose GameID is the LMM game ID - but
// Service.GetModFiles (which ResolveModVersion calls into) forwards
// straight to the source with NO game-ID translation, unlike Service.GetMod,
// which maps through game.SourceIDs[sourceID] first. Sources like NexusMods
// address games by their own domain (e.g. "skyrimspecialedition"), so
// whenever a game's per-source mapping differs from its LMM ID, version
// resolution would silently query the wrong upstream game. The fix is the
// same one verify.go already applies for exactly this reason
// (sourceMappedMod, used at verify.go:302/998): pass
// sourceMappedMod(game, &mod.Mod) instead of &mod.Mod.
//
// setupDoModLockTest's fixture maps game.SourceIDs["src"] to "g1" - the SAME
// as game.ID - which is exactly why none of the existing lock tests caught
// this: the bug is invisible when the mapped and unmapped IDs happen to
// coincide. This test deliberately overrides the mapping to a different
// value so the wrong ID becomes observable, mirroring
// TestDoVerify_VersionCheck_MapsGameIDPerSourceMapping's own pattern
// (fakeInstallSource.receivedGameFileIDs, the same test double both
// packages share).
func TestDoModLock_ExplicitVersion_MapsGameIDPerSourceMapping(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID}, []domain.DownloadableFile{
		{ID: "f1", Version: "1.0", Category: "MAIN"},
		{ID: "f2", Version: "2.0", Category: "MAIN"},
	})
	game.SourceIDs["src"] = "mapped-domain"

	var err error
	_ = captureStdout(t, func() error {
		err = doModLock(context.Background(), svc, game, "a", "2.0")
		return err
	})
	require.NoError(t, err)

	require.Len(t, src.receivedGameFileIDs, 1, "GetModFiles must have been called exactly once for version resolution")
	assert.Equal(t, "mapped-domain", src.receivedGameFileIDs[0], "version resolution must translate GameID through game.SourceIDs, same rule as Service.GetMod")
}

// TestDoModLock_NoVersion_LocksAtCurrentRecordedVersion guards (b) and half
// of (g): omitting the version argument locks at the ref's current version,
// leaves Version untouched, and - since target equals what's installed -
// must NOT print the convergence hint.
func TestDoModLock_NoVersion_LocksAtCurrentRecordedVersion(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")

	out := captureStdout(t, func() error {
		return doModLock(context.Background(), svc, game, "a", "")
	})

	assert.Contains(t, out, "✓ Mod A locked at v1.0")
	assert.NotContains(t, out, "converge", "target matches installed version; no convergence hint expected")

	profile, err := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Locked)
	assert.Equal(t, "1.0", profile.Mods[0].Version)
}

// TestDoModLock_UnknownVersion_ErrorListsAvailable guards (c): an
// upstream-invalid version must be rejected before SetModLock ever runs
// (Task 2's sequencing constraint), surfacing core.ErrVersionNotFound's
// verbatim message naming the versions that ARE available.
func TestDoModLock_UnknownVersion_ErrorListsAvailable(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID}, []domain.DownloadableFile{
		{ID: "f1", Version: "1.0", Category: "MAIN"},
		{ID: "f2", Version: "2.0", Category: "MAIN"},
	})

	err := doModLock(context.Background(), svc, game, "a", "9.9")

	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrVersionNotFound)
	assert.Contains(t, err.Error(), "9.9")
	assert.Contains(t, err.Error(), "1.0, 2.0")

	profile, loadErr := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, loadErr)
	assert.False(t, profile.Mods[0].Locked, "a rejected version must never reach SetModLock")
	assert.Equal(t, "1.0", profile.Mods[0].Version)
}

// TestDoModLock_VersionlessSource_NamesPin guards (d): the static capability
// gate must reject before any upstream call, naming 'lmm mod set-update
// --pin' as the alternative for a source that cannot resolve versions.
func TestDoModLock_VersionlessSource_NamesPin(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)

	novers := &noVersionsCapSource{id: "novers"}
	svc.RegisterSource(novers)
	game.SourceIDs["novers"] = "g1"

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "a", SourceID: "novers", Name: "Mod A", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "novers", ModID: "a", Version: "1.0"}))

	modSource = "novers"

	err = doModLock(context.Background(), svc, game, "a", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `source "novers" cannot resolve versions`)
	assert.Contains(t, err.Error(), "lmm mod set-update -s novers -p default a --pin", "the remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile (#142 round 5)")
}

// TestDoModUnlock_ClearsMarkerLeavesVersionIntact guards (e): unlock must
// clear ONLY the Locked marker, leaving Version exactly as it was, and print
// the current update policy.
func TestDoModUnlock_ClearsMarkerLeavesVersionIntact(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "src", "a", "2.0"))

	out := captureStdout(t, func() error {
		return doModUnlock(svc, game, "a")
	})

	assert.Contains(t, out, "✓ Mod A unlocked (update policy: notify)")

	profile, err := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Locked)
	assert.Equal(t, "2.0", profile.Mods[0].Version, "unlock must not touch Version")
}

// TestDoModLock_NotInstalled_ModNotFound guards (f): locking a mod that
// isn't installed must fail at the same "mod not found: %s" idiom every
// other mod subcommand uses (doModSetUpdate/doModEnable/doModDisable), not
// some new/different message.
func TestDoModLock_NotInstalled_ModNotFound(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)

	err := doModLock(context.Background(), svc, game, "missing", "")

	require.Error(t, err)
	assert.Equal(t, "mod not found: missing", err.Error())
}

// TestDoModUnlock_NotInstalled_ModNotFound is doModLock's sibling above,
// unlock side.
func TestDoModUnlock_NotInstalled_ModNotFound(t *testing.T) {
	svc, game, _ := setupDoModLockTest(t)

	err := doModUnlock(svc, game, "missing")

	require.Error(t, err)
	assert.Equal(t, "mod not found: missing", err.Error())
}

// TestModLockCmd_Structure and TestModUnlockCmd_Structure pin the cobra
// wiring the brief specifies: lock accepts 1 or 2 positional args (mod-id,
// optional version), unlock accepts exactly 1.
func TestModLockCmd_Structure(t *testing.T) {
	assert.Equal(t, "lock <mod-id> [version]", modLockCmd.Use)
	assert.NotEmpty(t, modLockCmd.Short)
	assert.NoError(t, modLockCmd.Args(modLockCmd, []string{"a"}))
	assert.NoError(t, modLockCmd.Args(modLockCmd, []string{"a", "1.0"}))
	assert.Error(t, modLockCmd.Args(modLockCmd, []string{}))
	assert.Error(t, modLockCmd.Args(modLockCmd, []string{"a", "1.0", "extra"}))
}

func TestModUnlockCmd_Structure(t *testing.T) {
	assert.Equal(t, "unlock <mod-id>", modUnlockCmd.Use)
	assert.NotEmpty(t, modUnlockCmd.Short)
	assert.NoError(t, modUnlockCmd.Args(modUnlockCmd, []string{"a"}))
	assert.Error(t, modUnlockCmd.Args(modUnlockCmd, []string{}))
	assert.Error(t, modUnlockCmd.Args(modUnlockCmd, []string{"a", "extra"}))
}

// TestPinFlagHelp_ReframedAsCheckMute guards the --pin help-text reframe:
// pinning mutes update checks; holding an exact version is lock's job now.
func TestPinFlagHelp_ReframedAsCheckMute(t *testing.T) {
	flag := modSetUpdateCmd.Flags().Lookup("pin")
	require.NotNil(t, flag)
	assert.Equal(t, "mute update checks for this mod (to hold an exact version, use 'lmm mod lock')", flag.Usage)
	assert.Contains(t, modSetUpdateCmd.Long, "lmm mod lock")
}
