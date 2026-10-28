package core_test

// The #445 final gate's F-D: `lmm profile apply` deploys a mod another
// profile has cached (cachedRowElsewhere) and gives the applied profile its
// own row. That row used to be the other profile's, copied - its link
// method, its update policy (an `auto` leaked across profiles) and its
// update history - while the deploy used this profile's link method. It is
// now built fresh, as a profile import builds one: this profile's link
// method, the notify policy, no previous version. And the game adapter's
// precondition, which a plan checks over the profile's own rows, covers
// the borrowed row too, at the plan and at the apply.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// borrowedFixture is a game whose active profile alt lists mod s at 2.0
// and has no row for it, while the non-active survival has s at 2.0 in the
// cache - its row carrying a link method, an update policy and a history of
// its own. altExtra is appended to alt's document.
func borrowedFixture(t *testing.T, game *domain.Game, svc *core.Service, altExtra string) *legacyFixture {
	t.Helper()
	ctx := context.Background()
	if svc == nil {
		svc = newFlowsTestService(t)
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	f := &legacyFixture{svc: svc, game: game}
	f.profile(t, "survival", false, "s")
	dir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alt.yaml"), []byte(
		"name: alt\ngame_id: "+game.ID+"\nmods:\n    - source_id: local\n      mod_id: s\n      version: \"2.0\"\n"+altExtra+"is_default: true\n"), 0o644))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "local", "s", "2.0", "Data/s.esp", []byte("s 2.0")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:             domain.Mod{ID: "s", SourceID: "local", Name: "Mod s", Version: "2.0", GameID: game.ID},
		ProfileName:     "survival",
		UpdatePolicy:    domain.UpdateAuto,
		LinkMethod:      domain.LinkCopy,
		PreviousVersion: "1.0",
		PreviousFileIDs: []string{"s-1.0.zip"},
		ManualDownload:  true,
	}))
	return f
}

func TestProfileApply_ABorrowedRowIsThisProfilesOwn(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		altExtra string
		method   domain.LinkMethod
	}{
		{name: "the game's link method", method: domain.LinkSymlink},
		{name: "the profile's own link method", altExtra: "link_method: hardlink\n", method: domain.LinkHardlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := borrowedFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink), nil, tc.altExtra)

			plan, result := applyProfile(t, f.svc, f.game, "alt")

			require.Len(t, plan.ToEnable, 1)
			assert.Equal(t, "survival", plan.ToEnable[0].ProfileName, "the plan says whose cache it deploys from")
			assert.Equal(t, "2.0", plan.ToEnable[0].Version)
			require.Len(t, result.Outcomes, 1)
			assert.Equal(t, "survival", result.Outcomes[0].FromProfile)

			row, err := f.svc.GetInstalledMod(ctx, "local", "s", "sky", "alt")
			require.NoError(t, err)
			assert.Equal(t, "2.0", row.Version)
			assert.Equal(t, tc.method, row.LinkMethod, "the method the file was deployed with")
			assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy, "survival's auto policy is survival's")
			assert.Empty(t, row.PreviousVersion, "alt has no update history for it")
			assert.Empty(t, row.PreviousFileIDs)
			assert.True(t, row.ManualDownload, "a fact about the mod, not the profile")
			assert.True(t, row.Enabled)
			assert.True(t, row.Deployed)

			info, err := os.Lstat(filepath.Join(f.game.ModPath, "Data", "s.esp"))
			require.NoError(t, err)
			assert.Equal(t, tc.method == domain.LinkSymlink, info.Mode()&os.ModeSymlink != 0)

			other, err := f.svc.GetInstalledMod(ctx, "local", "s", "sky", "survival")
			require.NoError(t, err)
			assert.Equal(t, domain.UpdateAuto, other.UpdatePolicy, "survival's own row is untouched")
			assert.Equal(t, "1.0", other.PreviousVersion)
		})
	}
}

// keyRefuser refuses, once armed, a flow whose mods include modID.
type keyRefuser struct {
	armed *atomic.Bool
	modID string
}

func (keyRefuser) ID() string    { return "keyrefuser" }
func (keyRefuser) Label() string { return "Key refuser" }
func (keyRefuser) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}
func (r keyRefuser) CheckPreconditions(_ *domain.Game, mods []domain.InstalledMod) error {
	if r.armed.Load() && slices.ContainsFunc(mods, func(m domain.InstalledMod) bool { return m.ID == r.modID }) {
		return errLoaderMissing
	}
	return nil
}

func TestProfileApply_TheAdapterHasItsSayOnABorrowedRow(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*legacyFixture, *atomic.Bool) {
		t.Helper()
		armed := &atomic.Bool{}
		svc := newFlowsTestService(t)
		svc.RegisterAdapter(keyRefuser{armed: armed, modID: "s"})
		game := handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink)
		game.Adapter = "keyrefuser"
		return borrowedFixture(t, game, svc, ""), armed
	}
	requireRefused := func(t *testing.T, f *legacyFixture, err error) {
		t.Helper()
		var refused *core.AdapterPreconditionError
		require.ErrorAs(t, err, &refused)
		assert.NoFileExists(t, filepath.Join(f.game.ModPath, "Data", "s.esp"))
		rows, err := f.svc.GetInstalledMods(ctx, "sky", "alt")
		require.NoError(t, err)
		assert.Empty(t, rows)
	}

	t.Run("at the plan", func(t *testing.T) {
		f, armed := setup(t)
		armed.Store(true)

		_, err := f.svc.PlanProfileApply(ctx, f.game, "alt")

		requireRefused(t, f, err)
	})

	t.Run("at the apply", func(t *testing.T) {
		f, armed := setup(t)
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
		require.NoError(t, err)
		require.Len(t, plan.ToEnable, 1)
		armed.Store(true)

		_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)

		requireRefused(t, f, err)
	})
}
