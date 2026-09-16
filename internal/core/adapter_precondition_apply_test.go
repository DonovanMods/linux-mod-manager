package core_test

// The Apply half of I4 (#413 fix round 4, F8): a plan made while the
// game's adapter still said yes must not be applied once it says no. Every
// Apply re-checks its freshness snapshot through checkPlanFresh, which asks
// the adapter; the review showed that switching one Apply to the ungated
// checkRemovalPlanFresh passed the whole suite. This drives each Apply the
// fixture can plan, and TestEveryServiceApplyIsCheckedOrExempt keeps the
// list complete.

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"go/ast"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// switchableRefuser is refusingAdapter with its refusal switched on only
// after the plan is made.
type switchableRefuser struct{ refuse *atomic.Bool }

func (switchableRefuser) ID() string    { return "switchable" }
func (switchableRefuser) Label() string { return "Switchable" }
func (switchableRefuser) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}
func (r switchableRefuser) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	if r.refuse.Load() {
		return errLoaderMissing
	}
	return nil
}

// planThenApply plans under a consenting adapter and returns the Apply of
// that plan, for the caller to run once the adapter refuses.
type planThenApply func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error

// checkedApplies is every Apply the precondition must stop for a plan made
// before the refusal.
var checkedApplies = map[string]planThenApply{
	"ApplyDeploy": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanDeploy(context.Background(), g, "default", core.DeployOptions{})
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyDeploy(context.Background(), g, plan, core.DeployOptions{}, nil)
			return err
		}
	},
	"ApplyAdopt": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanAdopt(context.Background(), g, "default", core.AdoptOptions{})
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyAdopt(context.Background(), g, plan, nil)
			return err
		}
	},
	"ApplyAdoptBackfill": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanAdopt(context.Background(), g, "default", core.AdoptOptions{})
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyAdoptBackfill(context.Background(), g, plan, nil)
			return err
		}
	},
	"ApplyRelinkMod": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanRelinkMod(context.Background(), g, "default", "acme", "m1", "acme", "m2")
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyRelinkMod(context.Background(), g, plan, core.RelinkOptions{}, nil)
			return err
		}
	},
	"ApplyImportArchive": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanImportArchive(context.Background(), g, "default", fx.archive, core.ImportArchiveOptions{})
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyImportArchive(context.Background(), g, "default", plan, core.ImportArchiveOptions{}, nil)
			return err
		}
	},
	"ApplyProfileApply": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanProfileApply(context.Background(), g, "default")
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyProfileApply(context.Background(), g, plan, core.ProfileApplyOptions{}, nil)
			return err
		}
	},
	"ApplyProfileSync": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanProfileSync(context.Background(), g, "default")
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyProfileSync(context.Background(), g, plan, nil)
			return err
		}
	},
	"ApplySnapshotRestore": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanSnapshotRestore(context.Background(), g, fx.snapshotName)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplySnapshotRestore(context.Background(), g, plan, core.SnapshotRestoreOptions{}, nil)
			return err
		}
	},
	"ApplyInstall": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanInstall(context.Background(), g, "default", "acme", "m2", false)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyInstall(context.Background(), g, plan, core.InstallOptions{}, nil)
			return err
		}
	},
	"ApplyRollback": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanRollback(context.Background(), g, "default", "acme", "m1")
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyRollback(context.Background(), g, plan, core.RollbackOptions{}, nil)
			return err
		}
	},
	"ApplyProfileSwitch": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanProfileSwitch(context.Background(), g, "other")
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyProfileSwitch(context.Background(), g, plan, nil)
			return err
		}
	},
	"ApplyImport": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanImport(context.Background(), g, fx.profileDoc)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyImport(context.Background(), g, plan, core.ProfileImportOptions{}, nil)
			return err
		}
	},
	"ApplyUpdate": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanUpdateFrom(context.Background(), g, "default", fx.update)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyUpdate(context.Background(), g, plan, core.UpdateOptions{}, nil)
			return err
		}
	},
	"ApplyUpdateBatch": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanUpdateBatchFrom(context.Background(), g, "default", []domain.Update{fx.update}, nil)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyUpdateBatch(context.Background(), g, plan, core.UpdateBatchOptions{}, nil)
			return err
		}
	},
	"ApplyWorkshopAdopt": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanWorkshopAdopt(context.Background(), g, "default", core.WorkshopAdoptOptions{})
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyWorkshopAdopt(context.Background(), g, plan, nil)
			return err
		}
	},
	"ApplyWorkshopCollectionImport": func(t *testing.T, svc *core.Service, g *domain.Game, fx planFixture) func() error {
		plan, err := svc.PlanWorkshopCollectionImport(context.Background(), g, "default", fx.collection)
		require.NoError(t, err)
		return func() error {
			_, err := svc.ApplyWorkshopCollectionImport(context.Background(), g, plan, core.ProfileImportOptions{}, nil)
			return err
		}
	},
}

// uncheckedApplies are the Apply methods that carry no plan snapshot for
// the precondition to re-check, each with why.
var uncheckedApplies = map[string]string{
	"ApplyPurge":           "a removal: exempt by design (TestRemovalPlansIgnoreTheAdapterPrecondition)",
	"ApplyUninstall":       "a removal: exempt by design (TestRemovalPlansIgnoreTheAdapterPrecondition)",
	"ApplyGameDetect":      "adds games.yaml entries; no game's mods are acted on",
	"ApplyDetectSelection": "adds games.yaml entries; no game's mods are acted on",
	"ApplyMergedPakRegen":  "no plan: it needs the adapter's compiler, which a refused adapter does not resolve",
}

// TestEveryApplyRechecksTheAdapterPrecondition plans each flow while the
// adapter consents, then makes it refuse, and requires the Apply to refuse
// too.
func TestEveryApplyRechecksTheAdapterPrecondition(t *testing.T) {
	names := make([]string, 0, len(checkedApplies))
	for name := range checkedApplies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			svc, game, fx := newPlanFixtureWithAdapter(t, nil)
			refuse := &atomic.Bool{}
			svc.RegisterAdapter(switchableRefuser{refuse: refuse})
			game.Adapter = "switchable"
			require.NoError(t, svc.SaveGame(context.Background(), game))

			apply := checkedApplies[name](t, svc, game, fx)
			refuse.Store(true)
			err := apply()
			require.Error(t, err, "%s must refuse a plan made before the adapter refused", name)
			assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)
		})
	}
}

// serviceApplyMethods lists every exported Apply* method on *Service.
func serviceApplyMethods(files []*ast.File) []string {
	var names []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() || !strings.HasPrefix(fn.Name.Name, "Apply") {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Service" {
				names = append(names, fn.Name.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// TestEveryServiceApplyIsCheckedOrExempt keeps checkedApplies complete.
func TestEveryServiceApplyIsCheckedOrExempt(t *testing.T) {
	methods := serviceApplyMethods(parseCorePackage(t))
	require.NotEmpty(t, methods)
	exists := map[string]bool{}
	for _, m := range methods {
		exists[m] = true
		_, checked := checkedApplies[m]
		_, exempt := uncheckedApplies[m]
		assert.True(t, checked != exempt, "Service.%s must be in exactly one of checkedApplies and uncheckedApplies", m)
	}
	for name := range checkedApplies {
		assert.True(t, exists[name], "checkedApplies names %s, which Service does not have", name)
	}
	for name := range uncheckedApplies {
		assert.True(t, exists[name], "uncheckedApplies names %s, which Service does not have", name)
	}
}
