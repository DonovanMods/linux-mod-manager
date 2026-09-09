package core

// #332 I1: create/delete/set-default were calling ProfileManager directly,
// bypassing beginOp - so a click that deletes a profile could interleave
// with a deploy job mid-flight for that very profile (`lmm serve` runs
// Applies in background goroutines; the CLI never has this race). These
// tests pin the gated Service seams (CreateProfile/DeleteProfile/
// SetDefaultProfile) using the same package-internal access to beginOp/
// opSem that ops_test.go's TestBeginOp_SecondMutationBlocksUntilRelease
// uses, since beginOp is unexported.

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/require"
)

// gatingFixture creates one game with one profile ("default"), ready for a
// create/delete/set-default call.
func gatingFixture(t *testing.T) (*Service, *domain.Game) {
	t.Helper()
	svc := newOpsService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return svc, game
}

func TestServiceCreateProfile_ReturnsTheProfileResultDocument(t *testing.T) {
	svc, game := gatingFixture(t)

	result, err := svc.CreateProfile(context.Background(), game.ID, "survival")
	require.NoError(t, err)
	require.Equal(t, "survival", result.Profile.Name)
	require.Empty(t, result.Profile.Mods)
}

func TestServiceCreateProfile_ExistingNameIsTypedError(t *testing.T) {
	svc, game := gatingFixture(t)

	_, err := svc.CreateProfile(context.Background(), game.ID, "default")
	require.ErrorIs(t, err, ErrProfileExists)
}

func TestServiceDeleteProfile_ReturnsThePreDeletionProfileDocument(t *testing.T) {
	svc, game := gatingFixture(t)
	require.NoError(t, svc.NewProfileManager().AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "m1", Version: "1.0"}))

	result, err := svc.DeleteProfile(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, "default", result.Profile.Name)
	require.Len(t, result.Profile.Mods, 1, "the pre-deletion document must carry the mods it held")

	_, err = svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestServiceDeleteProfile_UnknownProfile(t *testing.T) {
	svc, game := gatingFixture(t)

	_, err := svc.DeleteProfile(context.Background(), game.ID, "ghost")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestServiceSetDefaultProfile_ReturnsTheProfileResultDocument(t *testing.T) {
	svc, game := gatingFixture(t)
	_, err := svc.CreateProfile(context.Background(), game.ID, "survival")
	require.NoError(t, err)

	result, err := svc.SetDefaultProfile(context.Background(), game.ID, "survival")
	require.NoError(t, err)
	require.True(t, result.Profile.IsDefault)

	def, err := svc.NewProfileManager().GetDefault(context.Background(), game.ID)
	require.NoError(t, err)
	require.Equal(t, "survival", def.Name)
}

// TestServiceCreateDeleteSetDefaultProfile_SerialiseBehindAnInFlightMutation
// pins I1's headline claim with the existing beginOp test pattern
// (TestBeginOp_SecondMutationBlocksUntilRelease): each gated seam must wait
// for the service's single mutation slot rather than racing whatever
// currently holds it - a deploy included, since beginOp is service-wide and
// does not distinguish by operation kind.
func TestServiceCreateDeleteSetDefaultProfile_SerialiseBehindAnInFlightMutation(t *testing.T) {
	tests := []struct {
		name string
		call func(svc *Service, game *domain.Game) error
	}{
		{"create", func(svc *Service, game *domain.Game) error {
			_, err := svc.CreateProfile(context.Background(), game.ID, "survival")
			return err
		}},
		{"delete", func(svc *Service, game *domain.Game) error {
			_, err := svc.DeleteProfile(context.Background(), game.ID, "default")
			return err
		}},
		{"set-default", func(svc *Service, game *domain.Game) error {
			_, err := svc.SetDefaultProfile(context.Background(), game.ID, "default")
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := gatingFixture(t)

			// Simulate an in-flight deploy (or any other mutation): beginOp
			// is one slot for the whole Service, so holding it directly is
			// exactly what a real DeployProfile does for the duration of
			// its own Apply.
			release, err := svc.beginOp(context.Background())
			require.NoError(t, err)

			done := make(chan struct{})
			var callErr error
			go func() {
				callErr = tc.call(svc, game)
				close(done)
			}()

			select {
			case <-done:
				t.Fatal("the gated call completed while the slot was held by another mutation")
			case <-time.After(50 * time.Millisecond):
			}

			release()

			select {
			case <-done:
				require.NoError(t, callErr)
			case <-time.After(time.Second):
				t.Fatal("the gated call never completed after the slot was released")
			}
		})
	}
}
