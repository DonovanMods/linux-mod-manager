package core_test

// The two failures the index surface reports as TYPED errors (#410, design
// §4.3), so a frontend branches on them and renders their details rather
// than matching a sentence: the index cannot be had (HTTP 502), and the
// game's identifier for the source is missing or malformed (HTTP 400).

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validatingIndexedSource is an indexed source whose identifiers have a
// shape - Thunderstore's lowercase community slug, in miniature.
type validatingIndexedSource struct {
	*indexedSource
	searchErr error
}

func (s *validatingIndexedSource) ValidateGameIdentifier(id string) error {
	for _, r := range id {
		if r < 'a' || r > 'z' {
			if r != '-' {
				return fmt.Errorf("%q is not a community slug: %w", id, source.ErrGameIdentifierInvalid)
			}
		}
	}
	return nil
}

func (s *validatingIndexedSource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return s.indexedSource.Search(ctx, q)
}

func newValidatingIndexService(t *testing.T, mapping string) (*core.Service, *validatingIndexedSource) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := &validatingIndexedSource{indexedSource: newIndexedSource("indexed")}
	svc.RegisterSource(src)
	svc.RegisterSource(newMockSource("other"))
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "lethal", Name: "Lethal Company", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"indexed": mapping},
	}))
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "unmapped", Name: "Unmapped", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"other": "x"},
	}))
	return svc, src
}

// TestGameIdentifierError_CarriesTheGameTheSourceAndTheValue covers all
// three ways a game can fail to name its index: empty, malformed, and not
// mapped at all. Each is the typed error with the data the web UI needs to
// point the user at the sources editor, and each still classifies.
func TestGameIdentifierError_CarriesTheGameTheSourceAndTheValue(t *testing.T) {
	tests := []struct {
		name, mapping, gameID, wantValue, wantText string
	}{
		{"empty", "", "lethal", "", "lmm game edit lethal --source indexed="},
		{"malformed", "Lethal_Company", "lethal", "Lethal_Company", "not a community slug"},
		{"not mapped", "lethal-company", "unmapped", "", "does not use source \"indexed\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newValidatingIndexService(t, tt.mapping)
			calls := map[string]func() error{
				"SourceIndexStatus": func() error {
					_, err := svc.SourceIndexStatus(t.Context(), "indexed", tt.gameID)
					return err
				},
				"RefreshSourceIndex": func() error {
					_, err := svc.RefreshSourceIndex(t.Context(), "indexed", tt.gameID, false, nil)
					return err
				},
			}
			if tt.name != "not mapped" { // an unscoped search never asks an unmapped source
				calls["SearchMods"] = func() error {
					_, err := svc.SearchMods(t.Context(), "indexed", tt.gameID, "q", "", nil, 0, 0)
					return err
				}
			}
			for name, call := range calls {
				err := call()
				require.Error(t, err, name)
				var typed *core.GameIdentifierError
				require.True(t, errors.As(err, &typed), "%s: %T %v", name, err, err)
				assert.Equal(t, tt.gameID, typed.GameID, name)
				assert.Equal(t, "indexed", typed.Source, name)
				assert.Equal(t, tt.wantValue, typed.Value, name)
				assert.Same(t, typed, typed.Details(), name)
				assert.True(t, core.IsGameIdentifierInvalid(err), name)
				assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid, name)
				assert.Contains(t, err.Error(), tt.wantText, name)
			}
		})
	}
}

// TestSourceIndex_AnUnknownGameIsNotFound: the index surface is about a
// game lmm manages; one it does not is a lookup failure, not a guess.
func TestSourceIndex_AnUnknownGameIsNotFound(t *testing.T) {
	svc, _ := newValidatingIndexService(t, "lethal-company")
	_, err := svc.SourceIndexStatus(t.Context(), "indexed", "nope")
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
	_, err = svc.RefreshSourceIndex(t.Context(), "indexed", "nope", false, nil)
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestIndexUnavailableError_NamesTheIndexAndWhy: no index and no way to
// build one is typed wherever it surfaces, carries the source, the
// community and the underlying reason - and, when a host is refusing to be
// asked, the moment it will be asked again.
func TestIndexUnavailableError_NamesTheIndexAndWhy(t *testing.T) {
	until := time.Date(2026, 9, 16, 12, 5, 0, 0, time.UTC)
	tests := []struct {
		name        string
		cause       error
		wantReason  string
		wantRetryAt time.Time
	}{
		{
			name:       "fetch failed",
			cause:      fmt.Errorf("source %q: the lethal-company index could not be built: %w: %w", "indexed", errors.New("rate limited by Thunderstore (HTTP 429) after 3 attempts"), source.ErrIndexUnavailable),
			wantReason: "rate limited by Thunderstore (HTTP 429) after 3 attempts",
		},
		{
			name:        "host suspended",
			cause:       fmt.Errorf("fetching: %w", &source.RetryLaterError{Source: "Thunderstore", Until: until, Reason: "suspended after repeated failures"}),
			wantReason:  "suspended after repeated failures",
			wantRetryAt: until,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, src := newValidatingIndexService(t, "lethal-company")
			src.err = tt.cause
			src.searchErr = tt.cause

			_, refreshErr := svc.RefreshSourceIndex(t.Context(), "indexed", "lethal", true, nil)
			_, searchErr := svc.SearchMods(t.Context(), "indexed", "lethal", "q", "", nil, 0, 0)
			for name, err := range map[string]error{"refresh": refreshErr, "search": searchErr} {
				var typed *core.IndexUnavailableError
				require.True(t, errors.As(err, &typed), "%s: %T %v", name, err, err)
				assert.Equal(t, "indexed", typed.Source, name)
				assert.Equal(t, "lethal-company", typed.Game, name)
				assert.Equal(t, tt.wantReason, typed.Reason, name)
				assert.Equal(t, tt.wantRetryAt, typed.RetryAt, name)
				assert.True(t, core.IsIndexUnavailable(err), name)
				assert.Equal(t, tt.cause.Error(), err.Error(), "%s: the sentence is the cause's own", name)
				assert.Same(t, typed, typed.Details(), name)
			}
		})
	}
}

// TestSearch_AStaleIndexIsAWarningOnBothPaths is design §2.4's frontend
// half: the named-source search and the unscoped one both report a source's
// own warnings beside its results.
func TestSearch_AStaleIndexIsAWarningOnBothPaths(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	stale := &staleWarningSource{mockSource: newMockSource("indexed")}
	svc.RegisterSource(stale)
	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"indexed": "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))

	for name, opts := range map[string]core.SearchOptions{
		"named":    {SourceID: "indexed"},
		"unscoped": {},
	} {
		report, err := svc.Search(t.Context(), game, "", "", opts)
		require.NoError(t, err, name)
		require.Len(t, report.Mods, 1, name)
		require.Len(t, report.Warnings, 1, name)
		assert.Equal(t, "indexed", report.Warnings[0].SourceID, name)
		assert.Contains(t, report.Warnings[0].ErrorMessage, "refreshing it failed", name)
	}
}

type staleWarningSource struct{ *mockSource }

func (s *staleWarningSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{
		Mods:     []domain.Mod{{ID: "A-B", SourceID: "indexed", Name: "B", GameID: "lethal-company"}},
		Warnings: []error{fmt.Errorf("results come from the lethal-company index fetched 2026-09-10T12:00:00Z, because refreshing it failed: %w", source.ErrIndexUnavailable)},
	}, nil
}
