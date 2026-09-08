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
