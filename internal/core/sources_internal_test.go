package core

// Internal tests for the registry-mutation half of the source surface
// (sources.go). They are internal because the property under test is the
// MUTATION GATE: proving a swap cannot interleave with an in-flight
// mutation means holding the Service's single op slot by hand, which is
// exactly what an external test cannot do without racing a real flow.

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapTestSource is a minimal ModSource whose NAME carries a generation
// marker, so a test can tell one registration of an id from the next.
type swapTestSource struct {
	id   string
	name string
}

func (s *swapTestSource) ID() string      { return s.id }
func (s *swapTestSource) Name() string    { return s.name }
func (s *swapTestSource) AuthURL() string { return "" }

func (*swapTestSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (*swapTestSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (*swapTestSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, domain.ErrModNotFound
}

func (*swapTestSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (*swapTestSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}

func (*swapTestSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

func (*swapTestSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

var _ source.ModSource = (*swapTestSource)(nil)

// newSwapTestService builds a Service through NewService (not a struct
// literal) so its op semaphore is real - beginOp panics on a nil one.
func newSwapTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func TestReplaceSource_SwapsTheLiveSourceWhenNothingIsInFlight(t *testing.T) {
	svc := newSwapTestService(t)
	svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})

	replaced, err := svc.ReplaceSource(t.Context(), &swapTestSource{id: "demo", name: "after"})
	require.NoError(t, err)
	assert.True(t, replaced, "the swap displaced an existing registration")

	got, err := svc.GetSource("demo")
	require.NoError(t, err)
	assert.Equal(t, "after", got.Name(), "the registry hands out the NEW source")
}

func TestReplaceSource_ReportsNoDisplacementForANewID(t *testing.T) {
	svc := newSwapTestService(t)

	replaced, err := svc.ReplaceSource(t.Context(), &swapTestSource{id: "fresh", name: "only"})
	require.NoError(t, err)
	assert.False(t, replaced, "nothing was registered under this id")

	got, err := svc.GetSource("fresh")
	require.NoError(t, err)
	assert.Equal(t, "only", got.Name())
}

func TestReplaceSource_RefusesWhileAMutationHoldsTheGate(t *testing.T) {
	svc := newSwapTestService(t)
	svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})

	// Stand in for an in-flight mutation by taking the one slot beginOp
	// hands out; every Apply* in this package is holding exactly this.
	svc.opSem <- struct{}{}
	defer func() { <-svc.opSem }()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	replaced, err := svc.ReplaceSource(ctx, &swapTestSource{id: "demo", name: "after"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, replaced)

	got, err := svc.GetSource("demo")
	require.NoError(t, err)
	assert.Equal(t, "before", got.Name(), "a refused swap leaves the registry untouched")
}

func TestUnregisterSource_RemovesAndToleratesAbsence(t *testing.T) {
	svc := newSwapTestService(t)
	svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})

	removed, err := svc.UnregisterSource(t.Context(), "demo")
	require.NoError(t, err)
	assert.True(t, removed)
	_, err = svc.GetSource("demo")
	require.Error(t, err, "the source is gone from the registry")

	// A definition that never constructed successfully is registered
	// nowhere, and deleting its file must not fail on that account.
	removed, err = svc.UnregisterSource(t.Context(), "demo")
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestUnregisterSource_RefusesWhileAMutationHoldsTheGate(t *testing.T) {
	svc := newSwapTestService(t)
	svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})

	svc.opSem <- struct{}{}
	defer func() { <-svc.opSem }()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	removed, err := svc.UnregisterSource(ctx, "demo")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, removed)
	_, err = svc.GetSource("demo")
	require.NoError(t, err, "a refused removal leaves the registry untouched")
}

// TestUnregisterSourceIfUnused_RefusesAGameMappedBetweenCheckAndDelete pins
// #333 Minor #1's fix: the in-use check and the removal share ONE beginOp,
// so a game mapping the source cannot land in a gap between them the way it
// could when app.DeleteSourceDefinition ran an ungated GamesUsingSource
// before a separately-gated UnregisterSource.
//
// #333 fix-wave N1: the original version of this test raced a real AddGame
// against a real UnregisterSourceIfUnused over 30 iterations and asserted
// that BOTH branches (AddGame's beginOp wins / UnregisterSourceIfUnused's
// beginOp wins) fired at least once - which made it load-bearing (it goes
// RED when the in-use check is deleted) but also genuinely flaky: on this
// machine AddGame has enough extra setup work before it reaches beginOp
// that, run alone, its branch frequently never won across 30 attempts
// (measured 4/12 alone, 2/6 file-scoped; only reliable under full-package
// load). A flaky test cannot merge, so this forces both properties the old
// test HOPED scheduling would demonstrate, deterministically:
//
//   - "mapped_before_the_gate_is_acquired" proves the check sees a mapping
//     that was written (by a real, completed AddGame) before
//     UnregisterSourceIfUnused ever ran - no race required, since
//     gamesUsingSource always reads the live snapshot at check time.
//   - "check_runs_inside_the_gate" proves the check cannot be pried apart
//     from the gate that also serializes AddGame's write, using the
//     unregisterSourceIfUnusedBeforeCheck test hook to pause
//     UnregisterSourceIfUnused at the exact program point between
//     acquiring the gate and running the check, then asserting - by a
//     non-blocking send on the real semaphore, not a sleep - that the gate
//     is held at that instant. That is the structural property the old
//     app.DeleteSourceDefinition bug violated (an ungated check followed
//     by a separately-gated delete): if the check ever moves outside the
//     gate again, this assertion fails immediately, with no dependence on
//     scheduler luck.
//
// Both subtests are fully deterministic: run alone, file-scoped, or under
// -count=30, they cannot flake, and the first goes RED the moment the
// in-use check is deleted (Unregister would then always succeed).
func TestUnregisterSourceIfUnused_RefusesAGameMappedBetweenCheckAndDelete(t *testing.T) {
	t.Run("mapped_before_the_gate_is_acquired", func(t *testing.T) {
		svc := newSwapTestService(t)
		svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})
		install := t.TempDir()

		_, err := svc.AddGame(context.Background(), GameSpec{
			SourceID: "demo", Identifier: "g1", Name: "G1", InstallPath: install,
		})
		require.NoError(t, err, "the mapping must land before Unregister ever runs")

		removed, err := svc.UnregisterSourceIfUnused(context.Background(), "demo")
		require.Error(t, err, "a mapping that landed before the gate was acquired must still be seen")
		var inUse *SourceInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, []string{"g1"}, inUse.Games)
		assert.False(t, removed)

		_, err = svc.GetSource("demo")
		require.NoError(t, err, "a refused removal leaves the registry untouched")
	})

	t.Run("check_runs_inside_the_gate", func(t *testing.T) {
		svc := newSwapTestService(t)
		svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})
		install := t.TempDir()

		gateHeld := make(chan struct{})
		proceed := make(chan struct{})
		unregisterSourceIfUnusedBeforeCheck = func() {
			close(gateHeld)
			<-proceed
		}
		t.Cleanup(func() { unregisterSourceIfUnusedBeforeCheck = nil })

		unregDone := make(chan struct{})
		var removed bool
		var unregErr error
		go func() {
			defer close(unregDone)
			removed, unregErr = svc.UnregisterSourceIfUnused(context.Background(), "demo")
		}()

		<-gateHeld // Unregister has the gate and is paused just before the check.

		// Prove the gate is actually held right now, structurally: the
		// semaphore (capacity 1) must refuse a second sender. This is what
		// would let a concurrent AddGame slip a mapping in during the old
		// ungated-check-then-gated-delete bug; it must not be possible here.
		select {
		case svc.opSem <- struct{}{}:
			t.Fatal("the op semaphore accepted a second holder while paused before the check - the check does not run inside the gate")
		default:
		}

		close(proceed)
		<-unregDone

		require.NoError(t, unregErr)
		assert.True(t, removed, "nothing was mapped while the gate was held, so the unused source is removed")

		// The gate is released now: AddGame must find the source gone,
		// never parking a game that maps a source which no longer exists.
		_, err := svc.AddGame(context.Background(), GameSpec{
			SourceID: "demo", Identifier: "g1", Name: "G1", InstallPath: install,
		})
		require.Error(t, err)
		var specErr *GameSpecError
		require.ErrorAs(t, err, &specErr)
		assert.Equal(t, "source_id", specErr.Field)
	})
}
