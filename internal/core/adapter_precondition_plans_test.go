package core_test

// Every Plan must reach the game adapter's precondition (#411, I4).
//
// The precondition used to be checked inside currentInstalledSnapshot only,
// which every Apply reaches through checkPlanFresh but only some Plans
// reach: the eight that already held their installed set built their
// snapshot with the free function snapshotOf and skipped it entirely. So
// `lmm deploy` rendered a clean plan for a game whose loader was missing
// and its own Apply then refused - the single most-used flow.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// refusingAdapter is the smallest adapter with a Preconditioner, and it
// always refuses.
type refusingAdapter struct{ err error }

func (refusingAdapter) ID() string    { return "refuser" }
func (refusingAdapter) Label() string { return "Refuser" }
func (refusingAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}
func (r refusingAdapter) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	return r.err
}

// TestEveryPlanChecksTheAdapterPrecondition walks every Plan* entry point
// on the Service with a refusing adapter registered for the game, and
// requires each to refuse at PLAN time rather than leaving the refusal to
// its Apply.
func TestEveryPlanChecksTheAdapterPrecondition(t *testing.T) {
	plans := []struct {
		name string
		call func(t *testing.T, svc *core.Service, game *domain.Game, fx planFixture) error
	}{
		{"PlanAdopt", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanAdopt(context.Background(), g, "default", core.AdoptOptions{})
			return err
		}},
		{"PlanDeploy", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanDeploy(context.Background(), g, "default", core.DeployOptions{})
			return err
		}},
		{"PlanRelinkMod", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanRelinkMod(context.Background(), g, "default", "acme", "m1", "acme", "m2")
			return err
		}},
		{"PlanImportArchive", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanImportArchive(context.Background(), g, "default", fx.archive, core.ImportArchiveOptions{})
			return err
		}},
		{"PlanProfileApply", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanProfileApply(context.Background(), g, "default")
			return err
		}},
		{"PlanProfileSync", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanProfileSync(context.Background(), g, "default")
			return err
		}},
		{"PlanSnapshotRestore", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanSnapshotRestore(context.Background(), g, fx.snapshotName)
			return err
		}},
		{"PlanPurge", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanPurge(context.Background(), g, "default", core.PurgeOptions{})
			return err
		}},
		{"PlanInstall", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanInstall(context.Background(), g, "default", "acme", "m2", false)
			return err
		}},
		{"PlanInstallMany", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanInstallMany(context.Background(), g, "default",
				[]*domain.Mod{{ID: "m2", SourceID: "acme", Name: "M2", GameID: g.ID}}, false)
			return err
		}},
		{"PlanRollback", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanRollback(context.Background(), g, "default", "acme", "m1")
			return err
		}},
		{"PlanProfileSwitch", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanProfileSwitch(context.Background(), g, "other")
			return err
		}},
		{"PlanImport", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanImport(context.Background(), g, fx.profileDoc)
			return err
		}},
		{"PlanUninstall", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanUninstall(context.Background(), g, "default", "acme", "m1", core.UninstallOptions{})
			return err
		}},
		{"PlanUpdate", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanUpdate(context.Background(), g, "default", "acme", "m1")
			return err
		}},
		{"PlanUpdateFrom", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanUpdateFrom(context.Background(), g, "default", fx.update)
			return err
		}},
		{"PlanUpdateBatch", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanUpdateBatch(context.Background(), g, "default", nil)
			return err
		}},
		{"PlanUpdateBatchFrom", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanUpdateBatchFrom(context.Background(), g, "default", []domain.Update{fx.update}, nil)
			return err
		}},
		{"PlanWorkshopAdopt", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanWorkshopAdopt(context.Background(), g, "default", core.WorkshopAdoptOptions{})
			return err
		}},
		{"PlanWorkshopCollectionImport", func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) error {
			_, err := svc.PlanWorkshopCollectionImport(context.Background(), g, "default", fx.collection)
			return err
		}},
	}

	for _, tc := range plans {
		t.Run(tc.name, func(t *testing.T) {
			svc, game, fx := newPreconditionFixture(t)
			err := tc.call(t, svc, game, fx)

			require.Error(t, err, "%s must refuse at plan time, not leave it to its Apply", tc.name)
			assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)
			var typed *core.AdapterPreconditionError
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, "refuser", typed.Adapter)
			assert.Contains(t, typed.Reason, "install the loader first")
		})
	}
}

type planFixture struct {
	archive      string
	snapshotName string
	profileDoc   []byte
	collection   string
	update       domain.Update
}

func newPreconditionFixture(t *testing.T) (*core.Service, *domain.Game, planFixture) {
	t.Helper()
	svc, game, fx := newPlanFixtureWithAdapter(t, nil)
	svc.RegisterAdapter(refusingAdapter{err: errLoaderMissing})
	game.Adapter = "refuser"
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, fx
}

var errLoaderMissing = &loaderMissing{}

type loaderMissing struct{}

func (*loaderMissing) Error() string { return "install the loader first" }

// newPlanFixtureWithAdapter builds a service with enough state for every
// Plan to reach its installed-set read: a game with a profile, one enabled
// installed mod with a cached file, a registered source, a saved snapshot
// and an archive on disk.
func newPlanFixtureWithAdapter(t *testing.T, _ adapter.GameAdapter) (*core.Service, *domain.Game, planFixture) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{
		ID: "g1", Name: "Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{"acme": "g1", "steamworkshop": "440"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	src := newAdoptTestSource("acme")
	src.mods["m1"] = &domain.Mod{ID: "m1", SourceID: "acme", Name: "M1", GameID: "g1", Version: "1.0"}
	src.mods["m2"] = &domain.Mod{ID: "m2", SourceID: "acme", Name: "M2", GameID: "g1", Version: "1.0"}
	svc.RegisterSource(src)

	// The real workshop fakes, so PlanWorkshopAdopt and
	// PlanWorkshopCollectionImport both get past their source gate and
	// reach their installed-set read.
	ws := newCollectionTestSource()
	ws.scan = source.WorkshopScan{Roots: []string{t.TempDir()}}
	ws.collection = source.Collection{ID: "1234", Name: "Coll", ItemIDs: []string{"9001"}}
	svc.RegisterSource(ws)

	seedInstalledMod(t, svc, game, "acme", "m1", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedProfileWithMod(t, svc, game.ID, "default", "acme", "m1", "1.0")
	// PlanRollback refuses "no previous version" ahead of its snapshot read,
	// so the row needs one for that Plan to reach the precondition at all.
	installed, err := svc.GetInstalledMod(context.Background(), "acme", "m1", game.ID, "default")
	require.NoError(t, err)
	installed.PreviousVersion = "0.9"
	require.NoError(t, svc.SaveInstalledMod(context.Background(), installed))

	_, err = svc.CreateProfile(context.Background(), game.ID, "other")
	require.NoError(t, err)

	snap, err := svc.CreateSnapshot(context.Background(), game, "default", "snap1")
	require.NoError(t, err)

	exported, err := svc.ExportProfile(context.Background(), game.ID, "default")
	require.NoError(t, err)
	doc, err := yaml.Marshal(exported)
	require.NoError(t, err)

	archive := filepath.Join(t.TempDir(), "Mod-1.0.zip")
	createImportTestZip(t, archive, map[string]string{"b.esp": "b"})
	require.FileExists(t, archive)

	return svc, game, planFixture{
		archive:      archive,
		snapshotName: snap.Name,
		profileDoc:   doc,
		collection:   "1234",
		update: domain.Update{
			InstalledMod: domain.InstalledMod{
				Mod:         domain.Mod{ID: "m1", SourceID: "acme", Name: "M1", GameID: "g1", Version: "1.0"},
				ProfileName: "default",
				Enabled:     true,
			},
			NewVersion: "2.0",
		},
	}
}
