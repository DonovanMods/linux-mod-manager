package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTruncate tests the string truncation helper function
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "no truncation needed",
			input:    "short",
			maxLen:   10,
			expected: "short",
		},
		{
			name:     "exact length",
			input:    "exactly10!",
			maxLen:   10,
			expected: "exactly10!",
		},
		{
			name:     "needs truncation",
			input:    "this is a long string that needs truncation",
			maxLen:   20,
			expected: "this is a long st...",
		},
		{
			name:     "very short maxLen",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen equals 3",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen of 4",
			input:    "hello world",
			maxLen:   4,
			expected: "h...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSearchCmd_Structure tests the search command structure
func TestSearchCmd_Structure(t *testing.T) {
	assert.Equal(t, "search <query>", searchCmd.Use)
	assert.NotEmpty(t, searchCmd.Short)
	assert.NotEmpty(t, searchCmd.Long)

	// Check flags exist
	assert.NotNil(t, searchCmd.Flags().Lookup("source"))
	assert.NotNil(t, searchCmd.Flags().Lookup("limit"))
}

// TestSearchCmd_NoGame tests search without game flag
func TestSearchCmd_NoGame(t *testing.T) {
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(searchCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(searchCmd); rootCmd.AddCommand(searchCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search", "test-query"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestSearchCmd_NoQuery tests search without query argument
func TestSearchCmd_NoQuery(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(searchCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(searchCmd); rootCmd.AddCommand(searchCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

// TestSearchCmd_DefaultFlags tests that default flag values are set
func TestSearchCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := searchCmd.Flags().Lookup("source")
	assert.Equal(t, "", sourceFlag.DefValue)

	limitFlag := searchCmd.Flags().Lookup("limit")
	assert.Equal(t, "10", limitFlag.DefValue)
}

func TestSearchCmdStructure(t *testing.T) {
	assert.Equal(t, "search <query>", searchCmd.Use)
	flag := searchCmd.Flags().Lookup("source")
	if assert.NotNil(t, flag) {
		assert.Contains(t, flag.Usage, "all configured sources",
			"help text must reflect the new aggregate default")
	}
}

// TestLimitResults_NegativeLimitDoesNotPanic reproduces a pre-existing panic:
// `lmm search --limit -1` reaches `mods[:searchLimit]` with a negative
// index, which is a slice-bounds panic in Go. A negative (or otherwise
// non-positive) limit should mean "no truncation," not "truncate to a
// nonsensical bound."
func TestLimitResults_NegativeLimitDoesNotPanic(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	assert.NotPanics(t, func() {
		result := limitResults(mods, -1)
		assert.Equal(t, mods, result, "a negative limit must not truncate")
	})
}

func TestLimitResults_ZeroLimitDoesNotPanic(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}}

	assert.NotPanics(t, func() {
		result := limitResults(mods, 0)
		assert.Equal(t, mods, result, "a zero limit must not truncate")
	})
}

func TestLimitResults_PositiveLimitTruncates(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	result := limitResults(mods, 2)
	assert.Equal(t, []domain.Mod{{ID: "a"}, {ID: "b"}}, result)
}

func TestLimitResults_LimitAboveLenIsNoop(t *testing.T) {
	mods := []domain.Mod{{ID: "a"}}

	result := limitResults(mods, 10)
	assert.Equal(t, mods, result)
}

func TestCapabilityGapNotice(t *testing.T) {
	err := fmt.Errorf("source %q: searching: %w", "id-only", source.ErrNotSupported)
	notice, ok := capabilityGapNotice("id-only", err)
	assert.True(t, ok)
	assert.Contains(t, notice, "does not support searching")
	assert.Contains(t, notice, "lmm install --source id-only")
	assert.NotContains(t, notice, "operation not supported by this source",
		"the raw wrapped error must not leak into the notice")

	_, ok = capabilityGapNotice("x", errors.New("network down"))
	assert.False(t, ok)
}

// TestNoSourcesConfiguredErr tests the no-sources-configured guard
func TestNoSourcesConfiguredErr(t *testing.T) {
	tests := []struct {
		name    string
		game    *domain.Game
		wantErr bool
		wantMsg string
	}{
		{
			name: "empty sources returns error",
			game: &domain.Game{
				ID:        "test-game",
				Name:      "Test Game",
				SourceIDs: map[string]string{},
			},
			wantErr: true,
			wantMsg: "no mod sources configured",
		},
		{
			name: "non-empty sources returns nil",
			game: &domain.Game{
				ID:   "test-game",
				Name: "Test Game",
				SourceIDs: map[string]string{
					"nexusmods": "skyrimspecialedition",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := noSourcesConfiguredErr(tt.game)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantMsg)
				assert.Contains(t, err.Error(), "add sources with 'lmm game add' or edit games.yaml")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// pageSizeSpySource is a minimal ModSource that records the Page/PageSize it
// was queried with. It exists to prove (or disprove) that the CLI's --limit
// flag actually reaches the source as a requested page size, rather than
// being silently discarded after the source applies its own fixed default
// (see internal/source/custom/search.go and internal/source/nexusmods,
// which both default an unset PageSize to 20 — a page 0 with no --page flag
// therefore hard-caps every search at 20 results no matter what --limit is).
type pageSizeSpySource struct {
	id          string
	gotPage     int
	gotPageSize int
	calls       int
}

func (s *pageSizeSpySource) ID() string      { return s.id }
func (s *pageSizeSpySource) Name() string    { return s.id }
func (s *pageSizeSpySource) AuthURL() string { return "" }
func (s *pageSizeSpySource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *pageSizeSpySource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	s.calls++
	s.gotPage = q.Page
	s.gotPageSize = q.PageSize
	return source.SearchResult{Mods: []domain.Mod{{ID: "m1", SourceID: s.id, Name: "Mod One"}}, TotalCount: 1}, nil
}
func (s *pageSizeSpySource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (s *pageSizeSpySource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *pageSizeSpySource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *pageSizeSpySource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *pageSizeSpySource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// newPageSizeSpyService wires a real core.Service and game around a single
// pageSizeSpySource, so doSearch runs its real code path (not a mock of
// doSearch itself).
func newPageSizeSpyService(t *testing.T, spy *pageSizeSpySource) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(spy)

	game := &domain.Game{
		ID:        "testgame",
		Name:      "Test Game",
		ModPath:   t.TempDir(),
		SourceIDs: map[string]string{spy.id: ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// withSearchFlags saves and restores the package-level search flag globals
// doSearch reads, so tests can drive them without leaking state.
func withSearchFlags(t *testing.T, source string, limit int) {
	t.Helper()
	origSource, origLimit := searchSource, searchLimit
	t.Cleanup(func() { searchSource, searchLimit = origSource, origLimit })
	searchSource, searchLimit = source, limit
}

// TestDoSearch_AggregateDefault_RequestsSearchLimitAsPageSize reproduces the
// user-reported regression on PR #57: `lmm search <query> --limit 30` (no
// --source, so the aggregate default path) must fetch up to 30 results per
// source. Before the fix, doSearch always called SearchAllSources with a
// literal pageSize of 0, so every source's own default (20) silently
// capped results regardless of --limit, and there is no --page flag to
// reach anything beyond that.
func TestDoSearch_AggregateDefault_RequestsSearchLimitAsPageSize(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-agg"}
	svc, game := newPageSizeSpyService(t, spy)
	withSearchFlags(t, "", 30)

	require.NoError(t, doSearch(context.Background(), svc, game, []string{"query"}))

	require.Equal(t, 1, spy.calls)
	assert.Equal(t, 30, spy.gotPageSize, "--limit 30 must be requested as the page size, not discarded")
}

// TestDoSearch_ExplicitSource_RequestsSearchLimitAsPageSize is the
// apples-to-apples single-source counterpart: `--source <id> --limit 30`
// must also request a page size of 30 from the source.
func TestDoSearch_ExplicitSource_RequestsSearchLimitAsPageSize(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-single"}
	svc, game := newPageSizeSpyService(t, spy)
	withSearchFlags(t, "spy-single", 30)

	require.NoError(t, doSearch(context.Background(), svc, game, []string{"query"}))

	require.Equal(t, 1, spy.calls)
	assert.Equal(t, 30, spy.gotPageSize, "--limit 30 must be requested as the page size, not discarded")
}

// TestDoSearch_NonPositiveLimit_FallsBackToSourceDefaultPageSize pins the
// edge case at the boundary of the fix: --limit 0 (explicitly unset) or a
// negative --limit (the historical --limit -1 panic case, cmd/lmm/search.go
// limitResults) must not be forwarded as a nonsensical or unbounded page
// size request — it falls back to 0, letting each source apply its own
// default, exactly like before this fix.
func TestDoSearch_NonPositiveLimit_FallsBackToSourceDefaultPageSize(t *testing.T) {
	for _, limit := range []int{0, -1} {
		spy := &pageSizeSpySource{id: "spy-nonpositive"}
		svc, game := newPageSizeSpyService(t, spy)
		withSearchFlags(t, "spy-nonpositive", limit)

		require.NoError(t, doSearch(context.Background(), svc, game, []string{"query"}))

		require.Equal(t, 1, spy.calls)
		assert.Equal(t, 0, spy.gotPageSize, "limit %d must not be forwarded as the requested page size", limit)
	}
}

// --- #58 item 3: honesty notice when no configured source supports search ---

// noSearchCapSource is a real, registered ModSource with NO search
// capability (design §5 skips it silently in the aggregate path) - it
// exists to reproduce the "attempted == 0" case that used to be
// indistinguishable from a genuine zero-result search.
type noSearchCapSource struct{ id string }

func (s *noSearchCapSource) ID() string      { return s.id }
func (s *noSearchCapSource) Name() string    { return s.id }
func (s *noSearchCapSource) AuthURL() string { return "" }
func (s *noSearchCapSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *noSearchCapSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: false, Updates: true}
}
func (s *noSearchCapSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, errors.New("should never be called: Search capability is false")
}
func (s *noSearchCapSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (s *noSearchCapSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *noSearchCapSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *noSearchCapSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *noSearchCapSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// newNoSearchCapService wires a real core.Service and game around a single
// source that is configured but does not support searching.
func newNoSearchCapService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := &noSearchCapSource{id: "id-only-api"}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID:        "testgame",
		Name:      "Test Game",
		ModPath:   t.TempDir(),
		SourceIDs: map[string]string{src.id: ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// TestDoSearch_NoSearchableSources_PrintsHonestNotice reproduces the
// pre-fix bug: a game whose only configured source doesn't support search
// (attempted == 0) rendered the SAME "No mods found." text as a genuine
// zero-result search from a capable source, giving the user no hint that
// searching isn't even possible here.
func TestDoSearch_NoSearchableSources_PrintsHonestNotice(t *testing.T) {
	svc, game := newNoSearchCapService(t)
	withSearchFlags(t, "", 10)
	origJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSON })

	out, err := captureStdoutErr(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"query"})
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "No mods found.", "must not look like an ordinary empty search")
	assert.Contains(t, out, "Test Game")
	assert.Contains(t, out, "support searching")
}

// TestDoSearch_NoSearchableSources_JSON_NoticeGoesToStderr guards the
// one-document-on-stdout invariant (mirroring the update --json contract):
// --json's stdout must stay a single parseable JSON document, so the human
// notice goes to stderr instead.
func TestDoSearch_NoSearchableSources_JSON_NoticeGoesToStderr(t *testing.T) {
	svc, game := newNoSearchCapService(t)
	withSearchFlags(t, "", 10)
	withJSONOutput(t)

	// Both streams captured around ONE invocation: the same run must satisfy
	// both halves of the contract (a second run would also leak its stdout
	// JSON past the stderr-only capture into the test output).
	var stderr string
	var innerErr error
	stdout, err := captureStdoutErr(t, func() error {
		stderr, innerErr = captureStderrErr(t, func() error {
			return doSearch(context.Background(), svc, game, []string{"query"})
		})
		return innerErr
	})
	require.NoError(t, err)

	var out searchJSONOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), "stdout must stay a single valid JSON document")
	assert.Empty(t, out.Mods)
	assert.Contains(t, stderr, "support searching")
}
