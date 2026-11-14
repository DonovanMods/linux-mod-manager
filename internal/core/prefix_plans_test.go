package core_test

// #636 review: every Plan that may deploy refuses a game whose adapter
// deploys into a Wine/Proton prefix lmm cannot resolve - at plan time, as
// its Apply would (#451 F3) - through the one shared check the adapter gate
// makes (checkAdapterPreconditions -> refuseUnresolvedPrefix). gatedPlans
// is every such Plan (TestEveryServicePlanIsGatedOrExempt keeps it
// complete); the removals are exempt, as from the adapter's own refusal.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/require"
)

func TestEveryDeployingPlanRefusesAnUnresolvedPrefix(t *testing.T) {
	for _, tc := range gatedPlans {
		t.Run(tc.name, func(t *testing.T) {
			svc, game, fx := newPlanFixtureWithAdapter(t, nil)
			svc.RegisterAdapter(modtypes.BaldursGate3())
			game.Adapter = modtypes.BaldursGate3ID
			require.NoError(t, svc.SaveGame(context.Background(), game))
			err := tc.call(t, svc, game, fx)
			var refused *core.PrefixUnresolvedError
			require.True(t, errors.As(err, &refused), "%s must refuse an unresolved prefix at plan time, got %v", tc.name, err)
		})
	}
}
