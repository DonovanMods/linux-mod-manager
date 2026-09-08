package core

// Internal tests for the registry-mutation half of the source surface
// (sources.go). They are internal because the property under test is the
// MUTATION GATE: proving a swap cannot interleave with an in-flight
// mutation means holding the Service's single op slot by hand, which is
// exactly what an external test cannot do without racing a real flow.

import (
	"context"
	"sync"
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
// It races a real AddGame (which maps "demo") against a real
// UnregisterSourceIfUnused rather than asserting one fixed interleaving,
// because the whole point of gating both under the same slot is that
// whichever wins the slot decides the outcome for BOTH callers - there is
// no window left for a mapping to appear only to one of them:
//   - AddGame's beginOp wins: the game gets added, and
//     UnregisterSourceIfUnused - checking under the SAME gate slot AddGame
//     just held - must see the mapping and refuse.
//   - UnregisterSourceIfUnused's beginOp wins: nothing was mapped yet, so
//     it removes the source; AddGame then finds no such source registered
//     and fails on its own gated check (#333 Important #1), never landing
//     a game that maps a source which no longer exists.
//
// #333 Minor #10 (whole-branch gate review): asserting only "one of these
// two branches happened" is not load-bearing, because removing the
// in-use check steers EVERY run into the second branch regardless of
// which beginOp call actually won - Unregister always succeeds once the
// check is gone, so the mapping written by an add that ran first is
// simply deleted out from under it too, landing on the exact same
// "unregErr == nil" shape the second branch already asserts. 25 runs of
// the pre-fix race against a tree with the check deleted came back
// 25/25 on that branch. Racing the SAME goroutines runN times and
// requiring that BOTH branches actually fire at least once closes that:
// with the check present, real scheduling non-determinism produces both
// outcomes across enough runs; with it removed, the first branch can
// never fire again, and this test goes RED instead of silently staying
// green under -race.
func TestUnregisterSourceIfUnused_RefusesAGameMappedBetweenCheckAndDelete(t *testing.T) {
	const runs = 30
	var addWon, unregWon int

	for i := 0; i < runs; i++ {
		svc := newSwapTestService(t)
		svc.RegisterSource(&swapTestSource{id: "demo", name: "before"})
		install := t.TempDir()

		var addErr, unregErr error
		var removed bool

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, addErr = svc.AddGame(context.Background(), GameSpec{
				SourceID: "demo", Identifier: "g1", Name: "G1", InstallPath: install,
			})
		}()
		go func() {
			defer wg.Done()
			removed, unregErr = svc.UnregisterSourceIfUnused(context.Background(), "demo")
		}()
		wg.Wait()

		_, getErr := svc.GetSource("demo")
		if getErr == nil {
			// AddGame's beginOp ran first: the mapping it wrote must be
			// visible to the unregister that checked under the same gate
			// afterwards.
			addWon++
			require.NoError(t, addErr)
			require.Error(t, unregErr, "the mapping AddGame just wrote must not be missed")
			var inUse *SourceInUseError
			require.ErrorAs(t, unregErr, &inUse)
			assert.Equal(t, []string{"g1"}, inUse.Games)
			assert.False(t, removed)
		} else {
			// UnregisterSourceIfUnused's beginOp ran first: nothing was
			// mapped yet, so it succeeded, and AddGame must then refuse -
			// never park a game mapping a source that no longer exists.
			unregWon++
			require.NoError(t, unregErr)
			assert.True(t, removed)
			require.Error(t, addErr)
			var specErr *GameSpecError
			require.ErrorAs(t, addErr, &specErr)
			assert.Equal(t, "source_id", specErr.Field)
		}
	}

	assert.Positive(t, addWon, "AddGame's beginOp never won the gate across %d runs - this branch is untested", runs)
	assert.Positive(t, unregWon, "UnregisterSourceIfUnused's beginOp never won the gate across %d runs - this branch is untested", runs)
}
