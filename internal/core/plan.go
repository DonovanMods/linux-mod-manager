package core

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ErrStalePlan is returned by every Apply whose plan was computed against an
// installed-mod set that has since changed. The frontend re-plans.
var ErrStalePlan = errors.New("plan is stale: installed mods changed since it was computed")

// installedSnapshot is the precondition a Plan records and an Apply
// re-derives: the set of (source_id, mod_id, version, enabled) for the
// profile at plan time. Unexported, json:"-" wherever a Plan embeds one.
type installedSnapshot map[string]string // key "source:id" -> "version|enabled"

// currentInstalledSnapshot builds gameID/profileName's current installed-mod
// snapshot, keyed by domain.ModKey (source:id), so a later checkPlanFresh
// can detect any version, enabled-state, addition, or removal since a Plan
// was computed.
func (s *Service) currentInstalledSnapshot(ctx context.Context, gameID, profileName string) (installedSnapshot, error) {
	mods, err := s.GetInstalledMods(ctx, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods: %w", err)
	}
	snap := make(installedSnapshot, len(mods))
	for _, m := range mods {
		snap[domain.ModKey(m.SourceID, m.ID)] = fmt.Sprintf("%s|%t", m.Version, m.Enabled)
	}
	return snap, nil
}

// checkPlanFresh re-derives gameID/profileName's CURRENT installed-mod
// snapshot and compares it against want (a Plan's recorded precondition),
// returning nil when they match and a wrapped ErrStalePlan otherwise. Called
// as the first statement inside each Apply's private twin, after beginOp.
func (s *Service) checkPlanFresh(ctx context.Context, gameID, profileName string, want installedSnapshot) error {
	got, err := s.currentInstalledSnapshot(ctx, gameID, profileName)
	if err != nil {
		return err
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("%w: %s/%s", ErrStalePlan, gameID, profileName)
	}
	return nil
}
