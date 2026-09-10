package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexedSource is a mock source that keeps a local index - the shape core
// type-asserts for, standing in for Thunderstore so this package never
// imports a concrete source.
type indexedSource struct {
	*mockSource

	status  source.IndexStatus
	after   source.IndexStatus
	err     error
	seenID  string
	forced  bool
	refresh int
}

func newIndexedSource(id string) *indexedSource {
	return &indexedSource{mockSource: newMockSource(id)}
}

func (s *indexedSource) IndexStatus(_ context.Context, sourceGameID string) (source.IndexStatus, error) {
	s.seenID = sourceGameID
	return s.status, nil
}

func (s *indexedSource) RefreshIndex(_ context.Context, sourceGameID string, force bool, progress source.IndexProgressFunc) (source.IndexStatus, error) {
	s.seenID = sourceGameID
	s.forced = force
	s.refresh++
	if progress != nil {
		progress(source.FetchPhaseStarted, "fetching the index", 0)
		progress(source.FetchPhaseProgress, "indexed 1000 packages", 1024)
		progress(source.FetchPhaseDone, "indexed 12 packages", 0)
	}
	s.status = s.after
	return s.after, s.err
}

// newIndexService builds a Service with an indexed source and a plain one
// registered, and a game mapping both.
func newIndexService(t *testing.T) (*core.Service, *indexedSource) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	indexed := newIndexedSource("indexed")
	svc.RegisterSource(indexed)
	svc.RegisterSource(newMockSource("plain"))

	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company", ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"indexed": "lethal-company", "plain": ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return svc, indexed
}

// TestSourceIndexStatus_NilForASourceWithNoIndex is how a frontend decides
// the index surface does not exist for a source: nil, not an error.
func TestSourceIndexStatus_NilForASourceWithNoIndex(t *testing.T) {
	svc, _ := newIndexService(t)

	status, err := svc.SourceIndexStatus(t.Context(), "plain", "lethal")
	require.NoError(t, err)
	assert.Nil(t, status)
}

// TestSourceIndexStatus_UnknownSourceIsAnError separates "this source keeps
// no index" from "there is no such source".
func TestSourceIndexStatus_UnknownSourceIsAnError(t *testing.T) {
	svc, _ := newIndexService(t)

	_, err := svc.SourceIndexStatus(t.Context(), "nope", "lethal")
	require.Error(t, err)
}

// TestSourceIndexStatus_TranslatesTheGameIdentifier pins that the source is
// asked about ITS OWN game id - the community slug from games.yaml - not
// lmm's.
func TestSourceIndexStatus_TranslatesTheGameIdentifier(t *testing.T) {
	svc, indexed := newIndexService(t)
	indexed.status = source.IndexStatus{
		GameID: "lethal-company", Present: true, Packages: 50707,
		FetchedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), Bytes: 229 << 20, Stale: true,
	}

	status, err := svc.SourceIndexStatus(t.Context(), "indexed", "lethal")
	require.NoError(t, err)
	require.NotNil(t, status)

	assert.Equal(t, "lethal-company", indexed.seenID)
	assert.Equal(t, "indexed", status.Source)
	assert.Equal(t, "lethal-company", status.Game)
	assert.True(t, status.Present)
	assert.Equal(t, 50707, status.Packages)
	assert.True(t, status.Stale)
	assert.Equal(t, int64(229<<20), status.Bytes)
}

// TestRefreshSourceIndex_Outcomes is the report vocabulary as a table:
// Status is what the refresh DID, Changed whether the index the user
// searches is now different.
func TestRefreshSourceIndex_Outcomes(t *testing.T) {
	tests := []struct {
		name        string
		before      source.IndexStatus
		after       source.IndexStatus
		refreshErr  error
		wantStatus  string
		wantChanged bool
		wantWarning string
	}{
		{
			name:        "a cold build",
			before:      source.IndexStatus{GameID: "lethal-company"},
			after:       source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096},
			wantStatus:  core.IndexStatusBuilt,
			wantChanged: true,
		},
		{
			name:        "upstream had moved",
			before:      source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096},
			after:       source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 14, Bytes: 5000},
			wantStatus:  core.IndexStatusBuilt,
			wantChanged: true,
		},
		{
			name:        "already current",
			before:      source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096},
			after:       source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096},
			wantStatus:  core.IndexStatusCurrent,
			wantChanged: false,
		},
		{
			name:        "the refresh failed over a usable index",
			before:      source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096},
			after:       source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096, Stale: true},
			refreshErr:  errors.New("thunderstore.invalid: connection refused"),
			wantStatus:  core.IndexStatusStale,
			wantWarning: "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, indexed := newIndexService(t)
			indexed.status, indexed.after, indexed.err = tt.before, tt.after, tt.refreshErr

			report, err := svc.RefreshSourceIndex(t.Context(), "indexed", "lethal", true, nil)
			require.NoError(t, err, "a refresh over a usable index is never an error")
			require.NotNil(t, report)

			assert.Equal(t, tt.wantStatus, report.Status)
			assert.Equal(t, tt.wantChanged, report.Changed)
			assert.Equal(t, "indexed", report.Source)
			assert.Equal(t, "lethal-company", report.Game)
			assert.Equal(t, tt.after.Packages, report.Packages)
			assert.Equal(t, tt.after.Bytes, report.Bytes)
			assert.True(t, indexed.forced, "force must reach the source")
			if tt.wantWarning == "" {
				assert.Empty(t, report.Warnings)
			} else {
				require.Len(t, report.Warnings, 1)
				assert.Contains(t, report.Warnings[0], tt.wantWarning)
			}
		})
	}
}

// TestRefreshSourceIndex_NoIndexAndNoUpstreamIsAnError pins the other half
// of the stale-serve rule: a failure that leaves the caller with NO index
// is a failure.
func TestRefreshSourceIndex_NoIndexAndNoUpstreamIsAnError(t *testing.T) {
	svc, indexed := newIndexService(t)
	indexed.err = source.ErrIndexUnavailable

	report, err := svc.RefreshSourceIndex(t.Context(), "indexed", "lethal", false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	assert.Nil(t, report)
}

// TestRefreshSourceIndex_EmitsStepEvents pins what a frontend renders while
// a cold index is building.
func TestRefreshSourceIndex_EmitsStepEvents(t *testing.T) {
	svc, indexed := newIndexService(t)
	indexed.after = source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096}

	sink, events := core.RecordEvents()
	_, err := svc.RefreshSourceIndex(t.Context(), "indexed", "lethal", false, sink)
	require.NoError(t, err)

	var phases []core.DeployPhase
	var details []string
	for _, ev := range *events {
		step, ok := ev.(core.StepEvent)
		require.True(t, ok, "an index refresh reports steps and nothing else")
		assert.Equal(t, core.OpSourceIndex, step.Op)
		phases = append(phases, step.Phase)
		details = append(details, step.Detail)
	}
	assert.Equal(t, []core.DeployPhase{
		core.IndexRefreshStarted, core.IndexRefreshProgress, core.IndexRefreshDone,
	}, phases)
	assert.Equal(t, []string{"fetching the index", "indexed 1000 packages", "indexed 12 packages"}, details)
}

// TestRefreshSourceIndex_RefusesASourceWithNoIndex: nil is the answer for a
// READ, but asking to rebuild something that does not exist is a caller
// error the frontend should never have made.
func TestRefreshSourceIndex_RefusesASourceWithNoIndex(t *testing.T) {
	svc, _ := newIndexService(t)

	_, err := svc.RefreshSourceIndex(t.Context(), "plain", "lethal", false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrNotSupported)
}

// TestRefreshSourceIndex_IsCancellable pins that the caller's context gates
// the mutation slot, like every other mutation.
func TestRefreshSourceIndex_IsCancellable(t *testing.T) {
	svc, indexed := newIndexService(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.RefreshSourceIndex(ctx, "indexed", "lethal", false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, indexed.refresh, "a cancelled caller never reaches the source")
}
