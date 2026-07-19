# Custom Sources — Phase 5 (Aggregate Search + TUI Sources) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One search across all of a game's sources — `lmm search` with no `--source` queries every configured source concurrently, merges labeled results, and degrades per-source failures to warnings; the TUI gets an "All sources" default with per-row source labels and a read-only Sources screen.

**Architecture:** New `Service.SearchAllSources` fans out over the game's `sources:` map with `errgroup` (promoting the already-present indirect `golang.org/x/sync`), skips non-searching sources via `CapabilitiesOf`, collects per-source warnings, and merges with name-match-then-downloads ranking. The CLI defaults to aggregate mode and gains a SOURCE column plus design-§7 clean capability-gap notices. The TUI reuses its `DataProvider` seam: `""` becomes the "All sources" sentinel in `Search`, `SearchPage` carries warnings, and a new `SourceInfos()` method backs the Sources screen (registered via the standard keys.go/app.go pattern).

**Tech Stack:** Go, golang.org/x/sync/errgroup (module already in the dependency graph — promotion, not addition; same precedent as charmbracelet/x/ansi in TUI Phase 4), Bubble Tea (existing), testify.

**Spec:** `docs/plans/2026-07-13-custom-sources-design.md` §5 (aggregate search), §7 (graceful degradation UX), §8 (Sources screen). Issue #50 (epic #45).

**Scope note on acceptance criterion 2** ("unsupported actions hidden/disabled in the TUI"): the TUI is read-only today — no per-mod actions exist to gate (verified; TUI mutations are #37/#42). This criterion is satisfied by (a) the Sources screen displaying per-source capabilities and (b) a recorded requirement on #37 that future mutation UI must consult `CapabilitiesOf`. The final PR must state this explicitly.

## Global Constraints

- TDD: every task starts with a failing test.
- Error wrapping with context: `fmt.Errorf("doing X: %w", err)` (GO.md); `ctx` first param; no ctx in structs.
- No NEW module dependencies (`golang.org/x/sync` is already in go.sum via the module graph — adding the import promotes it to direct; anything else is off-limits).
- `go fmt ./...` and `go vet ./...` clean before every commit; run `-race` on TUI/core concurrency tests.
- Design §5 semantics (binding): per-source failures are warnings — one flaky API must not hide local modlets; only all-sources-failed is an error; sources without search capability are skipped silently; pagination is per-source (page N = page N from each source, merged; no global re-ranking beyond the sort below); merged sort = name-match relevance first, then Downloads descending, then Name ascending (stable).
- Design §7 (CLI): `errors.Is(err, source.ErrNotSupported)` ⇒ a clean one-line notice, never a wrapped-error dump.
- CLI and TUI are equally first-class (repo CLAUDE.md): the aggregate default and source labels ship in both.
- Commit after each task; conventional commit messages.

---

## Task 1: Core aggregate search (+ the deferred negative-page clamp)

**Files:**
- Modify: `internal/core/service.go` (new types + `SearchAllSources`), `internal/source/custom/api.go` (negative-page clamp, from the Phase 4 review triage), `go.mod` (x/sync promotion happens automatically via `go mod tidy`)
- Test: `internal/core/service_aggregate_search_test.go` (create), `internal/source/custom/api_test.go` (append one case)

**Interfaces:**
- Produces:

```go
// SourceWarning reports a per-source failure during an aggregate operation.
type SourceWarning struct {
	SourceID string
	Err      error
}

// AggregateSearchResult is the merged outcome of searching every source
// configured for a game.
type AggregateSearchResult struct {
	Mods       []domain.Mod    // merged, ranked; each Mod carries its SourceID
	TotalCount int             // sum of per-source totals (sources reporting 0/unknown contribute 0)
	Warnings   []SourceWarning // per-source failures (design §5: warnings, not errors)
}

func (s *Service) SearchAllSources(ctx context.Context, gameID, query, category string, tags []string, page, pageSize int) (AggregateSearchResult, error)
```

- Semantics later tasks rely on: candidate sources = the game's `SourceIDs` keys, sorted; unregistered mapped source → warning; `CapabilitiesOf(src).Search == false` → silent skip; runtime `ErrNotSupported` from Search → silent skip too (belt and suspenders); other error → warning; each candidate queried via the existing `s.SearchMods` (so per-source game-ID mapping applies) concurrently under `errgroup.WithContext`; all-attempted-failed (≥1 attempted, 0 succeeded) → `(partial result with warnings, error)`; zero attemptable sources → empty result, nil error.

- [ ] **Step 1: Write the failing tests**

Append to `internal/source/custom/api_test.go` (the clamp case):

```go
func TestAPISearchNegativePageClamped(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)

	_, err = a.Search(context.Background(), source.SearchQuery{Query: "x", Page: -3, PageSize: 10})
	require.NoError(t, err)
	// Negative pages clamp to page 0: {page} = 0 + pageStart(1) = 1, {offset} = 0.
	assert.Contains(t, gotPath, "page=1")
	assert.Contains(t, gotPath, "skip=0")
}
```

Create `internal/core/service_aggregate_search_test.go` (`package core_test`; reuse the existing mock-source helpers in service_test.go — read them first; the mock must let each fake source return canned `SearchResult`s or errors. If the existing `mockSource` can't return search results, extend the TEST helpers (test files only) with a small `searchStubSource` implementing `source.ModSource` + optional `source.CapabilityReporter`):

```go
package core_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchStubSource is a minimal ModSource whose Search returns canned data.
type searchStubSource struct {
	id      string
	caps    *source.Capabilities // nil = no CapabilityReporter (assumed fully capable)
	result  source.SearchResult
	err     error
	gotGame string // records the GameID the source was queried with
}

func (s *searchStubSource) ID() string      { return s.id }
func (s *searchStubSource) Name() string    { return s.id }
func (s *searchStubSource) AuthURL() string { return "" }
func (s *searchStubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *searchStubSource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	s.gotGame = q.GameID
	if s.err != nil {
		return source.SearchResult{}, s.err
	}
	return s.result, nil
}
func (s *searchStubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (s *searchStubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *searchStubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *searchStubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *searchStubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// capsStubSource adds a CapabilityReporter to searchStubSource.
type capsStubSource struct{ *searchStubSource }

func (c *capsStubSource) Capabilities() source.Capabilities { return *c.caps }

func newAggregateTestService(t *testing.T, sources map[string]string, srcs ...source.ModSource) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	for _, s := range srcs {
		svc.RegisterSource(s)
	}
	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), SourceIDs: sources}
	require.NoError(t, svc.AddGame(game))
	return svc, game
}

func mods(sourceID string, entries ...string) []domain.Mod {
	out := make([]domain.Mod, 0, len(entries))
	for _, e := range entries {
		out = append(out, domain.Mod{ID: e, SourceID: sourceID, Name: e})
	}
	return out
}

func TestSearchAllSourcesMergesAndTags(t *testing.T) {
	a := &searchStubSource{id: "alpha", result: source.SearchResult{
		Mods: []domain.Mod{
			{ID: "cool-a", SourceID: "alpha", Name: "Cool A", Downloads: 5},
			{ID: "other-a", SourceID: "alpha", Name: "Unrelated", Downloads: 100},
		},
		TotalCount: 2,
	}}
	b := &searchStubSource{id: "beta", result: source.SearchResult{
		Mods:       []domain.Mod{{ID: "cool-b", SourceID: "beta", Name: "Cool B", Downloads: 50}},
		TotalCount: 7,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": "mapped-beta"}, a, b)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "cool", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, 9, res.TotalCount)

	// Ranking: name matches ("Cool B" 50 > "Cool A" 5 by downloads) before
	// non-matches ("Unrelated"), stable and deterministic.
	require.Len(t, res.Mods, 3)
	assert.Equal(t, "cool-b", res.Mods[0].ID)
	assert.Equal(t, "cool-a", res.Mods[1].ID)
	assert.Equal(t, "other-a", res.Mods[2].ID)

	// Per-source game-ID mapping applied (empty mapping -> lmm game id).
	assert.Equal(t, "testgame", a.gotGame)
	assert.Equal(t, "mapped-beta", b.gotGame)
}

func TestSearchAllSourcesFailureIsWarning(t *testing.T) {
	ok := &searchStubSource{id: "local", result: source.SearchResult{
		Mods: mods("local", "modlet"), TotalCount: 1,
	}}
	flaky := &searchStubSource{id: "remote", err: fmt.Errorf("dial tcp: connection refused")}
	svc, game := newAggregateTestService(t, map[string]string{"local": "", "remote": ""}, ok, flaky)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "mod", "", nil, 0, 10)
	require.NoError(t, err, "one flaky source must not fail the aggregate")
	require.Len(t, res.Mods, 1)
	assert.Equal(t, "local", res.Mods[0].SourceID)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "remote", res.Warnings[0].SourceID)
}

func TestSearchAllSourcesSkipsNonSearching(t *testing.T) {
	searcher := &searchStubSource{id: "manifest", result: source.SearchResult{Mods: mods("manifest", "m1"), TotalCount: 1}}
	caps := source.Capabilities{Search: false, Updates: true}
	idOnly := &capsStubSource{&searchStubSource{id: "id-only-api", caps: &caps,
		err: fmt.Errorf("should never be called")}}
	svc, game := newAggregateTestService(t, map[string]string{"manifest": "", "id-only-api": ""}, searcher, idOnly)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "non-searching sources are skipped silently, not warned")
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesUnregisteredMappedSourceWarns(t *testing.T) {
	ok := &searchStubSource{id: "real", result: source.SearchResult{Mods: mods("real", "x"), TotalCount: 1}}
	svc, game := newAggregateTestService(t, map[string]string{"real": "", "ghost": ""}, ok)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "ghost", res.Warnings[0].SourceID)
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesAllFailedIsError(t *testing.T) {
	f1 := &searchStubSource{id: "s1", err: errors.New("boom1")}
	f2 := &searchStubSource{id: "s2", err: errors.New("boom2")}
	svc, game := newAggregateTestService(t, map[string]string{"s1": "", "s2": ""}, f1, f2)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.Error(t, err)
	assert.Len(t, res.Warnings, 2)
	assert.Empty(t, res.Mods)
}

func TestSearchAllSourcesUnknownGame(t *testing.T) {
	svc, _ := newAggregateTestService(t, map[string]string{})
	_, err := svc.SearchAllSources(context.Background(), "nope", "x", "", nil, 0, 10)
	assert.Error(t, err)
}
```

Note for the implementer: `mockSource` helpers already exist in service_test.go — if embedding/reusing them is cleaner than the standalone `searchStubSource` above, adapt the construction; every assertion stays.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run TestSearchAllSources -v; go test ./internal/source/custom/ -run TestAPISearchNegativePageClamped -v`
Expected: FAIL — `undefined: svc.SearchAllSources`; clamp test gets `page=-2`/`skip=-30`.

- [ ] **Step 3: Implement**

`internal/source/custom/api.go` — at the top of `Search`, right after the endpoint-nil check:

```go
	page := query.Page
	if page < 0 {
		page = 0 // match the shared searchMods clamp; never send negative paging upstream
	}
```

and use `page` instead of `query.Page` in the `vals` map and the returned `SearchResult.Page`.

`internal/core/service.go` — add the types from the Interfaces block and:

```go
// SearchAllSources searches every source configured for a game concurrently
// and merges the results (design §5). Per-source failures become Warnings —
// one flaky API must not hide local modlets; only all-sources-failed is an
// error. Sources without search capability are skipped silently. Pagination
// is per-source: page N requests page N from each source and merges.
func (s *Service) SearchAllSources(ctx context.Context, gameID, query, category string, tags []string, page, pageSize int) (AggregateSearchResult, error) {
	game, ok := s.games[gameID]
	if !ok {
		return AggregateSearchResult{}, fmt.Errorf("game not found: %s", gameID)
	}

	sourceIDs := make([]string, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)

	var result AggregateSearchResult
	type slot struct {
		res source.SearchResult
		err error
	}
	slots := make([]slot, len(sourceIDs))
	attempted := make([]bool, len(sourceIDs))

	g, gctx := errgroup.WithContext(ctx)
	for i, sourceID := range sourceIDs {
		src, err := s.registry.Get(sourceID)
		if err != nil {
			slots[i].err = err
			attempted[i] = true
			continue
		}
		if !source.CapabilitiesOf(src).Search {
			continue // silent skip (design §5)
		}
		attempted[i] = true
		i, sourceID := i, sourceID
		g.Go(func() error {
			res, err := s.SearchMods(gctx, sourceID, gameID, query, category, tags, page, pageSize)
			if err != nil {
				if errors.Is(err, source.ErrNotSupported) {
					attempted[i] = false // runtime capability gap: silent skip, not a warning
					return nil
				}
				slots[i].err = err
				return nil // never abort the group: siblings keep searching
			}
			slots[i].res = res
			return nil
		})
	}
	_ = g.Wait() // goroutines always return nil; errors live in slots

	succeeded := 0
	for i, sourceID := range sourceIDs {
		if !attempted[i] {
			continue
		}
		if slots[i].err != nil {
			result.Warnings = append(result.Warnings, SourceWarning{SourceID: sourceID, Err: slots[i].err})
			continue
		}
		succeeded++
		result.Mods = append(result.Mods, slots[i].res.Mods...)
		result.TotalCount += slots[i].res.TotalCount
	}

	rankAggregate(result.Mods, query)

	attemptedCount := 0
	for _, a := range attempted {
		if a {
			attemptedCount++
		}
	}
	if attemptedCount > 0 && succeeded == 0 {
		errs := make([]error, 0, len(result.Warnings))
		for _, w := range result.Warnings {
			errs = append(errs, fmt.Errorf("source %s: %w", w.SourceID, w.Err))
		}
		return result, fmt.Errorf("all %d source(s) failed: %w", attemptedCount, errors.Join(errs...))
	}
	return result, nil
}

// rankAggregate orders merged results: query-name matches first, then by
// Downloads descending, then Name ascending — deterministic regardless of
// which source responded first (design §5: no global re-ranking beyond this).
func rankAggregate(mods []domain.Mod, query string) {
	q := strings.ToLower(query)
	nameMatch := func(m domain.Mod) bool {
		return q != "" && (strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.ID), q))
	}
	sort.SliceStable(mods, func(i, j int) bool {
		mi, mj := nameMatch(mods[i]), nameMatch(mods[j])
		if mi != mj {
			return mi
		}
		if mods[i].Downloads != mods[j].Downloads {
			return mods[i].Downloads > mods[j].Downloads
		}
		return mods[i].Name < mods[j].Name
	})
}
```

Imports for service.go: add `"sort"`, `"errors"`, `"golang.org/x/sync/errgroup"` (verify which are already present). Run `go mod tidy` — x/sync moves to the direct require block; commit go.mod/go.sum with this task.

Note the `attempted[i] = false` write inside the goroutine: it's only read after `g.Wait()`, and each goroutine touches only its own index — race-free by construction; the `-race` run in Step 4 is the proof.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run TestSearchAllSources -race -v && go test ./internal/source/custom/ -v && go test ./... && go build ./...`
Expected: PASS everywhere, `-race` clean.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/service.go internal/core/service_aggregate_search_test.go internal/source/custom/api.go internal/source/custom/api_test.go go.mod go.sum
git commit -m "feat(core): aggregate search across all of a game's sources"
```

---

## Task 2: CLI aggregate default, source column, and §7 notices

**Files:**
- Modify: `cmd/lmm/search.go`
- Test: `cmd/lmm/search_test.go` (create or append — check for an existing file and follow the light cmd-test pattern)

**Interfaces:**
- Consumes: `service.SearchAllSources` (Task 1), `source.ErrNotSupported`, `domain.ModKey`.
- Produces: `lmm search <query>` with no `--source` runs aggregate mode; with `--source` keeps single-source mode. Both modes render a SOURCE column. Aggregate warnings print to stderr as `warning: source <id>: <err>`. JSON output gains `"source"` per mod and a top-level `"warnings"` array (aggregate mode). Explicit `--source` against a non-searching source prints the §7 clean notice and exits non-zero WITHOUT a wrapped-error dump.

- [ ] **Step 1: Write the failing test**

The cmd layer uses light tests. Create/append `cmd/lmm/search_test.go`:

```go
package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
)

func TestSearchCmdStructure(t *testing.T) {
	assert.Equal(t, "search <query>", searchCmd.Use)
	flag := searchCmd.Flags().Lookup("source")
	if assert.NotNil(t, flag) {
		assert.Contains(t, flag.Usage, "all configured sources",
			"help text must reflect the new aggregate default")
	}
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/lmm/ -run 'TestSearchCmdStructure|TestCapabilityGapNotice' -v`
Expected: FAIL — flag usage text mismatch; `undefined: capabilityGapNotice`.

- [ ] **Step 3: Implement**

In `cmd/lmm/search.go`:

1. Flag help: `"mod source to search (default: all configured sources)"`. Update `searchCmd.Long`'s "If --source is not specified..." sentence to say all configured sources are searched concurrently, and refresh the examples.
2. Add the §7 helper:

```go
// capabilityGapNotice turns an ErrNotSupported search failure into a clean
// one-line notice (design §7) instead of a wrapped-error dump. ok is false
// for every other error.
func capabilityGapNotice(sourceID string, err error) (string, bool) {
	if !errors.Is(err, source.ErrNotSupported) {
		return "", false
	}
	return fmt.Sprintf("source %q does not support searching; install by ID instead: lmm install --source %s --id <mod-id>", sourceID, sourceID), true
}
```

(add the `source` package import).
3. Restructure `doSearch`'s fetch block:

```go
	var mods []domain.Mod
	var warnings []core.SourceWarning
	var totalResults int

	if searchSource == "" {
		agg, err := service.SearchAllSources(ctx, game.ID, query, searchCategory, searchTags, 0, 0)
		if err != nil {
			// No ErrAuthRequired special-case here: an all-sources failure's
			// joined error already names each source and its reason (including
			// auth), and a per-source auth hint lives in the warnings path.
			return fmt.Errorf("search failed: %w", err)
		}
		mods, warnings = agg.Mods, agg.Warnings
		totalResults = len(mods)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: source %s: %v\n", w.SourceID, w.Err)
		}
	} else {
		sourceToUse, err := resolveSource(game, searchSource, false)
		if err != nil {
			return err
		}
		if verbose {
			fmt.Printf("Searching for %q in %s (%s)...\n", query, game.Name, sourceToUse)
		}
		searchResult, err := service.SearchMods(ctx, sourceToUse, game.ID, query, searchCategory, searchTags, 0, 0)
		if err != nil {
			if notice, ok := capabilityGapNotice(sourceToUse, err); ok {
				return errors.New(notice)
			}
			if errors.Is(err, domain.ErrAuthRequired) {
				return authPromptError(sourceToUse)
			}
			return fmt.Errorf("search failed: %w", err)
		}
		mods = searchResult.Mods
		totalResults = len(mods)
	}
```

(The old single-source `resolveSource`-first shape goes away; keep `verbose` output sensible in aggregate mode, e.g. `Searching for %q in %s (all sources)...`.)
4. Installed marking becomes source-aware: build `installedKeys := map[string]bool{}` from `domain.ModKey(im.SourceID, im.ID)` over ALL installed mods (drop the `im.SourceID == sourceToUse` filter), and look up `installedKeys[domain.ModKey(mod.SourceID, mod.ID)]`.
5. Table: add a SOURCE column (both modes) — header `ID\tNAME\tAUTHOR\tVERSION\tSOURCE\t`, row value `mod.SourceID`, installed mark stays the last column. Adjust the separator row to match.
6. JSON: `searchModJSON` gains `Source string \`json:"source"\``; `searchJSONOutput` gains `Warnings []string \`json:"warnings,omitempty"\`` (formatted `source <id>: <err>`); populate both. In aggregate mode the empty-result JSON path must still include warnings.

- [ ] **Step 4: Run tests and verify manually**

Run: `go test ./cmd/lmm/ -v && go build -o /tmp/lmm-p5 ./cmd/lmm && go test ./...`
Expected: PASS. (Live aggregate behavior is exercised in Task 6's e2e and the final review.)

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add cmd/lmm/search.go cmd/lmm/search_test.go
git commit -m "feat(cli): search all configured sources by default with source column"
```

---

## Task 3: TUI provider — all-sources search and warnings

**Files:**
- Modify: `internal/tui/service.go` (SearchPage + prototypeProvider), `internal/tui/service_core.go` (coreProvider)
- Test: `internal/tui/service_core_test.go` (append), `internal/tui/service_test.go` (append)

**Interfaces:**
- Consumes: `Service.SearchAllSources` (Task 1).
- Produces: `DataProvider.Search(ctx, source, query, page)` treats `source == ""` as "all sources" (the interface signature is unchanged — `""` becomes a documented sentinel). `SearchPage` gains `Warnings []string` (already-formatted, display-ready). `coreProvider.Search("")` calls `svc.SearchAllSources` with `SearchPageSize`, maps warnings to `"<sourceID>: <err>"` strings, and marks installed via `domain.ModKey`. `prototypeProvider.Search("")` returns its full canned set (tagged with its one source) so `--prototype` mode exercises the UI.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/service_core_test.go` (match its existing service-construction pattern — read it first; it builds a real `core.Service` with temp dirs and registered stub sources):

```go
func TestCoreProviderSearchAllSources(t *testing.T) {
	// Arrange a service with two searchable sources the way this file's
	// existing tests do (real core.Service + registered stubs), a game
	// mapping both, then:
	p := NewCoreProvider(svc, game, "default")

	page, err := p.Search(context.Background(), "", "quer", 0)
	require.NoError(t, err)
	// Results from both sources present, each row's Source set:
	sources := map[string]bool{}
	for _, item := range page.Results {
		sources[item.Source] = true
	}
	assert.Len(t, sources, 2)
}

func TestCoreProviderSearchAllSourcesWarnings(t *testing.T) {
	// One good + one failing source registered; game maps both.
	page, err := p.Search(context.Background(), "", "quer", 0)
	require.NoError(t, err)
	require.Len(t, page.Warnings, 1)
	assert.Contains(t, page.Warnings[0], failingSourceID)
	assert.NotEmpty(t, page.Results, "good source's results survive the failure")
}
```

(These are intent sketches — flesh out the arrangement using the file's real helpers; the assertions are the contract. If no reusable stub-source helper exists in the tui test package, register `searchStubSource`-style stubs locally — the type from Task 1's core test can't be imported across test packages, so declare a local minimal copy.)

Append to `internal/tui/service_test.go`:

```go
func TestPrototypeProviderSearchAllSources(t *testing.T) {
	p := NewPrototypeProvider()
	page, err := p.Search(context.Background(), "", "", 0)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Results)
	for _, item := range page.Results {
		assert.NotEmpty(t, item.Source)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestCoreProviderSearchAll|TestPrototypeProviderSearchAll' -v`
Expected: FAIL — `page.Warnings` undefined / empty-source Search returns an error or empty set today.

- [ ] **Step 3: Implement**

`internal/tui/service.go`:
- `SearchPage` gains `Warnings []string` with a doc comment: per-source failures in all-sources mode, display-ready.
- Document the sentinel on the `DataProvider.Search` doc comment: `source "" means all of the game's sources`.
- `prototypeProvider.Search`: when `source == ""`, serve the same canned results it serves for its single source (tag `Source` on each item as it already does).

`internal/tui/service_core.go` — in `coreProvider.Search`, branch on `source == ""`:

```go
	if source == "" {
		agg, err := p.svc.SearchAllSources(ctx, p.game.ID, query, "", nil, page, SearchPageSize)
		if err != nil {
			return SearchPage{}, err
		}
		warnings := make([]string, 0, len(agg.Warnings))
		for _, w := range agg.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %v", w.SourceID, w.Err))
		}
		// Map agg.Mods -> []ModItem exactly as the single-source path does
		// (installed marking via domain.ModKey against GetInstalledMods),
		// then return SearchPage{..., TotalCount: agg.TotalCount, Warnings: warnings}.
	}
```

Factor the existing `source.SearchResult.Mods -> []ModItem` mapping into a shared helper if the two branches would otherwise duplicate it (they will — extract `func (p *coreProvider) modsToItems(mods []domain.Mod) []ModItem` or similar from the current body and use it in both).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/tui/service.go internal/tui/service_core.go internal/tui/service_core_test.go internal/tui/service_test.go
git commit -m "feat(tui): all-sources search mode in the data provider"
```

---

## Task 4: TUI search UI — "All sources" default, source column, warning line

**Files:**
- Modify: `internal/tui/search.go` (sources list + labels), `internal/tui/app.go` (results pane column, header label, warning line)
- Test: `internal/tui/search_test.go` (append), `internal/tui/app_test.go` (append if row rendering is asserted there — follow existing conventions)

**Interfaces:**
- Consumes: Task 3's `""` sentinel + `SearchPage.Warnings`.
- Produces: the search screen's source cycle starts on "All sources" (`""` prepended to `provider.Sources()`; `sourceIdx` 0 default); the header shows `All sources` for the sentinel; cycling `s` still rotates through every real source and back; the results pane gains a source column (value `item.Source`) when in all-sources mode (single-source mode keeps today's columns); a one-line warning notice renders under the header when `page.Warnings` is non-empty (e.g. `⚠ 1 source unavailable: remote: connection refused` — truncate to width).

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/search_test.go` (drive `Model.Update` with key messages per the file's existing style — read it first for the constructor/helpers):

```go
func TestSearchDefaultsToAllSources(t *testing.T) {
	// Build the model the way existing tests do (prototype provider).
	// Assert: m.search.sources[0] == "" and m.search.source() == "" at start,
	// and the search header/view contains "All sources".
}

func TestCycleSourceRotatesThroughAllThenReal(t *testing.T) {
	// With prototype provider (one real source): press 's' once -> source()
	// == "nexusmods"; press 's' again -> back to "" (All sources).
	// (Cycling must now be enabled when len(sources) > 1 INCLUDING the
	// sentinel — with one real source the list is ["", "nexusmods"].)
}

func TestSearchWarningLineRendered(t *testing.T) {
	// Inject a searchResultMsg carrying a SearchPage with Warnings and
	// assert the rendered searchView() contains the warning marker and the
	// source id. Follow how existing tests inject result messages.
}
```

(Intent sketches: flesh out against the real helper names — `newSearchModel`, `searchResultMsg{gen, page}`, `m.searchView()` etc. The assertions are the contract.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSearchDefaults|TestCycleSource|TestSearchWarning' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/tui/search.go`:
- `newSearchModel`: `s.sources = append([]string{""}, provider.Sources()...)` (the sentinel first ⇒ default). Add/adjust a `sourceLabel()` helper: `""` → `"All sources"`, else the ID — used everywhere the current code prints `s.source()`.
- Anywhere that guards "no source configured" (`source() == ""` → `searchFailed`): rework — the sentinel is now a VALID search target; the invalid case is `len(provider.Sources()) == 0` (no real sources at all). Find that guard in `startSearch` and base it on the underlying real-source count.

`internal/tui/app.go`:
- Source-cycle key handling: the `len(sources) > 1` enable-check now naturally includes the sentinel (1 real source ⇒ list length 2 ⇒ cycling enabled — desired: users toggle All↔single).
- `searchHeaderLines`: print the label via `sourceLabel()`.
- `searchResultsPane`: when `m.search.source() == ""`, add a source column (dynamic width like the existing columns; keep single-source rendering unchanged).
- `searchReadyView` (or wherever fits the layout): render `page.Warnings` as one truncated line above/below the results header when non-empty; style consistently with existing warning/status text.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -race -v && go build ./...`
Expected: PASS (all existing search/navigation tests still green — pay attention to tests that assumed sources[0] was a real source; update ONLY where the new sentinel legitimately changes the expectation, and say so in the report).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/tui/search.go internal/tui/app.go internal/tui/search_test.go
git commit -m "feat(tui): default search to all sources with source labels and warnings"
```

---

## Task 5: TUI Sources screen

**Files:**
- Modify: `internal/tui/keys.go` (ScreenSources + binding), `internal/tui/app.go` (routing, menu, view), `internal/tui/service.go` (SourceInfo + provider method + prototype impl), `internal/tui/service_core.go` (real impl)
- Test: `internal/tui/sources_view_test.go` (create)

**Interfaces:**
- Produces:
  - `SourceInfo{ID, Name, Type, Auth, Capabilities string}` in service.go (display-ready strings, mirroring `lmm source list` columns minus error rows — the TUI lists REGISTERED sources only; definition load errors remain CLI-only, note this in the doc comment)
  - `DataProvider` gains `SourceInfos() []SourceInfo`
  - `coreProvider.SourceInfos()`: iterate `svc.ListSources()` sorted by ID; Type via type-switch (`*custom.Directory` → "directory", `*custom.Manifest` → "manifest", `*custom.API` → "api", default "built-in"); Auth via `source.CapabilitiesOf` + the `interface{ IsAuthenticated() bool }` assertion (mirror cmd/lmm/source.go's `authState`); Capabilities via a summary like cmd's `capabilitySummary` (reimplement locally in the tui package — cmd/lmm helpers aren't importable)
  - `prototypeProvider.SourceInfos()`: 2–3 static rows exercising the layout
  - `ScreenSources` registered per the standard pattern: keys.go const + `screens` slice + `String()` case + KeyMap binding `"5"`; app.go `updateKey` case, `screenView()` case, `itemCount`, `selected` seed, `dashboardMenu` entry in BOTH layout branches; `sourcesView()` renderer following `profilesView`'s single-pane list style with columns ID / TYPE / AUTH / CAPABILITIES
- Consumes: `source.CapabilitiesOf`, the custom source types.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/sources_view_test.go` (follow app_test.go/navigation_test.go conventions):

```go
func TestSourcesScreenRegistered(t *testing.T) {
	// screens slice contains ScreenSources; String() returns a non-default
	// label; screenAt round-trips; pressing "5" from the dashboard navigates
	// (drive Model.Update with the key like navigation_test.go does).
}

func TestSourceInfosPrototype(t *testing.T) {
	p := NewPrototypeProvider()
	infos := p.SourceInfos()
	require.NotEmpty(t, infos)
	for _, si := range infos {
		assert.NotEmpty(t, si.ID)
		assert.NotEmpty(t, si.Type)
	}
}

func TestSourcesViewRenders(t *testing.T) {
	// Prototype model, navigate to ScreenSources, assert the rendered view
	// contains each prototype source's ID and TYPE and the column headers.
}

func TestCoreProviderSourceInfos(t *testing.T) {
	// Real core.Service with a registered directory source (custom.NewDirectory
	// over t.TempDir()) + a stub built-in: assert directory row has
	// Type "directory", Auth "n/a" (no auth capability), and rows sorted by ID.
}
```

(Intent sketches — flesh out with the package's real constructors; assertions are the contract.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSources|TestSourceInfos' -v`
Expected: FAIL — `undefined: ScreenSources`, `SourceInfos` not on the interface.

- [ ] **Step 3: Implement**

Follow the registration checklist from the Interfaces block exactly (keys.go: 3 touches; app.go: 5 touches incl. both dashboard-menu layout branches; service.go: type + interface method + prototype rows; service_core.go: real implementation). `sourcesView()` renders a header line, column headers, and one row per `SourceInfo` with the selected row highlighted like `profilesView` does; respect the layout/width conventions of the sibling views. Update the help overlay (`helpView`) with the new screen's key.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -race -v && go build ./...`
Expected: PASS, including all pre-existing navigation tests (the new screen changes `screens` length — check tests that pin the count or tab-cycle order and update them deliberately, noting each in the report).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/tui/keys.go internal/tui/app.go internal/tui/service.go internal/tui/service_core.go internal/tui/sources_view_test.go
git commit -m "feat(tui): read-only Sources screen"
```

---

## Task 6: End-to-end aggregate test (acceptance criterion 1)

**Files:**
- Test: `internal/core/service_aggregate_e2e_test.go` (create)

Pins #50 acceptance criterion 1 with REAL source implementations (not stubs): a directory source and a manifest source both return labeled results in one aggregate search; a failing remote source (dead-URL manifest) degrades to a warning without hiding the local results.

- [ ] **Step 1: Write the test**

Create `internal/core/service_aggregate_e2e_test.go` (`package core_test`):

```go
func TestAggregateSearchEndToEnd(t *testing.T) {
	// Directory source: t.TempDir() with subdir "CoolLocalMod".
	// Manifest source: local mods.yaml with mod "cool-remote" (reuse the
	// manifest fixture style from service_manifest_source_test.go).
	// Dead source: manifest def with URL https://127.0.0.1:1/mods.yaml
	//   (AllowHTTP false, https scheme — construction is pure, fetch fails).
	// Register all three via custom.New; game maps all three with "" values.

	res, err := svc.SearchAllSources(ctx, game.ID, "cool", "", nil, 0, 20)
	require.NoError(t, err, "dead source must not fail the aggregate")

	bySource := map[string][]string{}
	for _, m := range res.Mods {
		bySource[m.SourceID] = append(bySource[m.SourceID], m.ID)
	}
	assert.Contains(t, bySource, "local-mods")
	assert.Contains(t, bySource, "manifest-repo")
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "dead-repo", res.Warnings[0].SourceID)

	// Every merged mod carries the lmm game id (the Phase 2/3 lesson).
	for _, m := range res.Mods {
		assert.Equal(t, game.ID, m.GameID)
	}
}
```

(Intent sketch — complete the arrangement with the real fixture helpers from the neighboring e2e files; every assertion stays. Also add a second test: the same setup queried through `svc.SearchMods` with an explicit single source still works — guarding that aggregate didn't regress single-source.)

- [ ] **Step 2: Run and commit**

Run: `go test ./internal/core/ -run TestAggregateSearch -race -v && go test ./...`
Expected: PASS.

```bash
go fmt ./... && go vet ./...
git add internal/core/service_aggregate_e2e_test.go
git commit -m "test(core): aggregate search end-to-end with mixed real sources"
```

---

## Task 7: Documentation and version bump

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version)

ACCURACY RULE (twice-burned in this epic): verify every sentence against the code on this branch; capture real output with a scratch build.

- [ ] **Step 1: Update docs**

README.md:
- Search docs: no `--source` = all configured sources concurrently; SOURCE column; per-source failures warn on stderr; `--source` narrows; the §7 notice for non-searching sources (real captured output).
- TUI docs: "All sources" default + `s` cycling; the Sources screen (key `5`); note the TUI lists registered sources (definition load errors visible via `lmm source list`).
- Custom Sources section: cross-reference that aggregate search is how multi-source games surface everything in one query.

CHANGELOG.md — `[Unreleased]` → `### Added`:

```markdown
- Aggregate search: `lmm search` without `--source` now queries every source configured for the game concurrently, with per-source failures reported as warnings
- Search results (CLI and TUI) show each mod's source; the TUI search defaults to "All sources"
- TUI Sources screen (key `5`) mirroring `lmm source list`
```

Then `## [1.10.0] - <today>`, comparison links, `version = "1.10.0"`.

- [ ] **Step 2: Final verification and commits**

```bash
go fmt ./... && go vet ./... && go test ./... -race
git add README.md CHANGELOG.md
git commit -m "docs: document aggregate search and the TUI Sources screen"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.10.0"
```

---

## Out of Scope

- TUI mutations (#37/#42) — capability-gating of actions lands there (recorded requirement: mutation UI must consult `CapabilitiesOf`)
- Global re-ranking / cross-source dedup (explicitly excluded by design §5 v1)
- #52 directory-source polish; deferred Phase 4 polish (url.Error unwrap unification, path-prefix validation, auth-guard dedup)
