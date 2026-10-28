package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// NewUpdatePlanForApplyTest builds an UpdatePlan directly around an
// already-known domain.Update, snapshotting gameID/profileName's CURRENT
// installed-mod set - for core_test's ApplyUpdate mechanics coverage
// (flows_update_test.go), which drives ApplyUpdate's download/hook/file-
// selection/compensation machinery against hand-crafted domain.Update
// values independent of Updater.CheckUpdates (that discovery step, and
// PlanUpdate's own branch computation, have their own coverage in
// update_plan_test.go/updater_test.go). Test-only: package core so it can
// set UpdatePlan's unexported snapshot field, exported so core_test (an
// external test package in the same directory) can call it - go test
// compiles both against the SAME test-augmented core package.
func (s *Service) NewUpdatePlanForApplyTest(ctx context.Context, gameID, profileName string, upd domain.Update) (*UpdatePlan, error) {
	snapshot, err := s.currentInstalledSnapshot(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	return &UpdatePlan{Mod: upd.InstalledMod, Update: &upd, snapshot: snapshot}, nil
}
