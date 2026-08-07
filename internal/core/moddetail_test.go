package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newModDetailTestService returns a fresh *core.Service (temp config/data/
// cache dirs, matching newFlowsTestService's construction pattern), a game
// registered against it, and a mockSource ("src") already registered on the
// service.
func newModDetailTestService(t *testing.T) (*core.Service, *domain.Game, *mockSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	src := newMockSource("src")
	svc.RegisterSource(src)
	return svc, game, src
}

// seedModDetailInstalled records modID as installed in the "default" profile
// at version, both in the DB (SaveInstalledMod, mirroring seedInstalledMod)
// and in the profile YAML (ProfileManager.Create + AddMod), since
// SetModLock - used by TestModDetail_LockJoinedFromProfileYAML - requires
// the mod ref to already exist in the profile file.
func seedModDetailInstalled(t *testing.T, svc *core.Service, game *domain.Game, modID, version string) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: "src",
			Name:     "Mod A",
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{
		SourceID: "src",
		ModID:    modID,
		Version:  version,
	}))
}

// TestModDetail_NotInstalled: a mod that exists upstream but is absent from
// the profile yields source metadata with a nil Installed block - "not
// installed" is an ordinary state, never an error (doModShow's own
// convention, cmd/lmm/mod.go:618-621).
func TestModDetail_NotInstalled(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Mod)
	assert.Equal(t, "Mod A", detail.Mod.Name)
	assert.Nil(t, detail.Installed, "an uninstalled mod must not carry an Installed block")
}

// TestModDetail_InstalledCarriesPolicyAndProfile joins the DB row.
func TestModDetail_InstalledCarriesPolicyAndProfile(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.Equal(t, "1.5", detail.Installed.Version)
	assert.Equal(t, "default", detail.Installed.Profile)
	assert.Equal(t, domain.UpdateNotify, detail.Installed.UpdatePolicy)
	assert.False(t, detail.Installed.Locked)
	assert.Nil(t, detail.Installed.ConvertPaks, "a non-compile game must leave ConvertPaks unset, not false")
}

// TestModDetail_LockJoinedFromProfileYAML: the lock lives in the profile YAML,
// not the DB, and LockedVersion is the lock's TARGET (which may differ from
// the installed version - that difference is what drives mod show's converge
// hint).
func TestModDetail_LockJoinedFromProfileYAML(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", "1.2.3"))

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.True(t, detail.Installed.Locked)
	assert.Equal(t, "1.2.3", detail.Installed.LockedVersion)
}

// seedModDetailInstalledPak records modID as installed in the "default"
// profile with fileIDs set on the DB row (consulted by ModHasPakMergeSource),
// then explicitly sets convert_paks via SetModConvertPaks - SaveInstalledMod
// never writes that column itself (internal/storage/db/mods.go: "the schema
// default covers first insert, and SetModConvertPaks is the only writer"),
// so passing ConvertPaks on the InstalledMod literal alone would silently
// leave the DB row at its schema default (true) regardless of the argument.
func seedModDetailInstalledPak(t *testing.T, svc *core.Service, game *domain.Game, modID, version string, fileIDs []string, convertPaks bool) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: "src",
			Name:     "Mod A",
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      fileIDs,
	}))
	require.NoError(t, svc.SetModConvertPaks("src", modID, game.ID, "default", convertPaks))

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{
		SourceID: "src",
		ModID:    modID,
		Version:  version,
		FileIDs:  fileIDs,
	}))
}

// TestModDetail_ConvertPaks covers the three-state ConvertPaks distinction
// at the core seam both mod show and the TUI now share: nil means "does not
// apply to this mod at all" (not a DeployCompile game, or the mod has no
// pak-kind merge source per ModHasPakMergeSource), while a non-nil pointer
// distinguishes "applies, and is off" from "applies, and is on" -
// conflating any of these three would mislead either interface's render
// (mirrors the TUI-side internal/tui.TestModDetailsFromItem_ConvertPaks,
// which only covers the ModItem->ModDetails leg, not this DB-join leg).
func TestModDetail_ConvertPaks(t *testing.T) {
	t.Run("not applicable when not a compile-deploy game", func(t *testing.T) {
		svc, game, src := newModDetailTestService(t)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
		seedModDetailInstalledPak(t, svc, game, "a", "1.5", []string{"pak"}, true)

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		require.NotNil(t, detail.Installed)
		assert.Nil(t, detail.Installed.ConvertPaks, "nil = not applicable, not \"off\"")
	})

	t.Run("not applicable when the mod has no pak merge source", func(t *testing.T) {
		svc, game, src := newModDetailTestService(t)
		game.DeployMode = domain.DeployCompile
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
		seedModDetailInstalledPak(t, svc, game, "a", "1.5", []string{"exmodz-file"}, true)

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		require.NotNil(t, detail.Installed)
		assert.Nil(t, detail.Installed.ConvertPaks, "nil = not applicable, not \"off\"")
	})

	t.Run("applies and is on", func(t *testing.T) {
		svc, game, src := newModDetailTestService(t)
		game.DeployMode = domain.DeployCompile
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
		seedModDetailInstalledPak(t, svc, game, "a", "1.5", []string{"pak"}, true)

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		require.NotNil(t, detail.Installed)
		require.NotNil(t, detail.Installed.ConvertPaks)
		assert.True(t, *detail.Installed.ConvertPaks)
	})

	t.Run("applies and is off", func(t *testing.T) {
		svc, game, src := newModDetailTestService(t)
		game.DeployMode = domain.DeployCompile
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})
		seedModDetailInstalledPak(t, svc, game, "a", "1.5", []string{"pak"}, false)

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		require.NotNil(t, detail.Installed)
		require.NotNil(t, detail.Installed.ConvertPaks, "a non-nil pointer to false must survive")
		assert.False(t, *detail.Installed.ConvertPaks)
	})
}

// TestModDetail_UnknownModErrors: a source lookup failure is a real error.
func TestModDetail_UnknownModErrors(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)

	_, err := svc.ModDetail(context.Background(), game, "default", "src", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod not found")
}
