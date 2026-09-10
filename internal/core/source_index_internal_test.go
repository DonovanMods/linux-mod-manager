package core

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A source that answers Search from a local index builds that index inside
// Search when there is none - a 34 MB download and a second of decoding,
// on a READ. This file pins the consequence the Service's concurrency
// contract turns on: that work must never take the mutation slot.
//
// It is asserted against beginOp directly rather than through a real
// mutation, because beginOp IS the slot: a test that raced two flows would
// be asserting the same thing with more moving parts and a worse failure
// message.

// blockingIndexSource stalls inside Search until it is released, which is
// what a cold index build looks like from outside.
type blockingIndexSource struct {
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (s *blockingIndexSource) ID() string      { return "blocking" }
func (s *blockingIndexSource) Name() string    { return "Blocking" }
func (s *blockingIndexSource) AuthURL() string { return "" }
func (s *blockingIndexSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *blockingIndexSource) Search(ctx context.Context, _ source.SearchQuery) (source.SearchResult, error) {
	if !s.once {
		s.once = true
		close(s.entered)
	}
	select {
	case <-s.release:
		return source.SearchResult{}, nil
	case <-ctx.Done():
		return source.SearchResult{}, ctx.Err()
	}
}

func (s *blockingIndexSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}

func (s *blockingIndexSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}

func (s *blockingIndexSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}

func (s *blockingIndexSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

func (s *blockingIndexSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func (s *blockingIndexSource) IndexStatus(context.Context, string) (source.IndexStatus, error) {
	return source.IndexStatus{GameID: "lethal-company"}, nil
}

func (s *blockingIndexSource) RefreshIndex(ctx context.Context, _ string, _ bool, _ source.IndexProgressFunc) (source.IndexStatus, error) {
	if !s.once {
		s.once = true
		close(s.entered)
	}
	select {
	case <-s.release:
		return source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 1}, nil
	case <-ctx.Done():
		return source.IndexStatus{}, ctx.Err()
	}
}

func newBlockingIndexService(t *testing.T) (*Service, *blockingIndexSource) {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &blockingIndexSource{entered: make(chan struct{}), release: make(chan struct{})}
	svc.RegisterSource(src)
	return svc, src
}

// TestIndexBuildInsideSearchNeverTakesTheMutationSlot is the load-bearing
// concurrency claim of the local-index seam: `lmm serve` builds a cold
// index synchronously inside GET /api/v1/search, and if that took the
// mutation slot (or the cross-process lock), one user's first search would
// block every mutation in the installation for as long as the download
// takes.
func TestIndexBuildInsideSearchNeverTakesTheMutationSlot(t *testing.T) {
	svc, src := newBlockingIndexService(t)

	done := make(chan error, 1)
	go func() {
		_, err := svc.SearchMods(t.Context(), "blocking", "lethal", "skinwalkers", "", nil, 1, 20)
		done <- err
	}()
	<-src.entered

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	release, err := svc.beginOp(ctx)
	require.NoError(t, err, "a mutation must not queue behind an index build running inside a search")
	release()

	close(src.release)
	require.NoError(t, <-done)
}

// TestIndexBuildInsideSearchIsCancellable: the other half of the same
// claim. A closed browser tab must be able to stop a build; the write-then-
// rename discipline is what keeps the half-written files off disk.
func TestIndexBuildInsideSearchIsCancellable(t *testing.T) {
	svc, src := newBlockingIndexService(t)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := svc.SearchMods(ctx, "blocking", "lethal", "skinwalkers", "", nil, 1, 20)
		done <- err
	}()
	<-src.entered
	cancel()

	err := <-done
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestRefreshSourceIndexDoesTakeTheMutationSlot is the deliberate
// difference: an EXPLICIT rebuild writes under the cache root on the user's
// instruction, so it serializes with every other mutation exactly as a
// deploy or an install does.
func TestRefreshSourceIndexDoesTakeTheMutationSlot(t *testing.T) {
	svc, src := newBlockingIndexService(t)

	done := make(chan error, 1)
	go func() {
		_, err := svc.RefreshSourceIndex(t.Context(), "blocking", "lethal", true, nil)
		done <- err
	}()
	<-src.entered

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := svc.beginOp(ctx)
	require.Error(t, err, "an explicit index rebuild holds the slot")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(src.release)
	require.NoError(t, <-done)
}
