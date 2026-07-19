# TUI Phase 4: Search and Detail Browsing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the TUI useful for browsing mod-source search results — text input, real source search with cancellation, installed markers, detail panel, pagination — without installing anything (issue #40).

**Architecture:** The `DataProvider` boundary evolves: `Summary`+`InstalledMods` merge into a single-fetch `Overview`, the `SearchResults` placeholder becomes `Search(ctx, source, query, page)`, and `Sources()` exposes the game's configured sources. The Bubble Tea `Model` gains the program context (`Options.Ctx`) and a search sub-model (`internal/tui/search.go`) owning a Bubbles `textinput`, focus state, and a generation-tagged async search command (cancel context + discard stale results). Focus-aware key routing suspends the global KeyMap while the input is focused.

**Tech Stack:** Go 1.25, Bubble Tea + Bubbles (`textinput`) + Lip Gloss (all present), testify, existing `core.Service.SearchMods`.

## Global Constraints

- **`main` is branch-protected — all changes land via PR** (merge-commit style). Work on branch `feat/tui-phase4-search` off `main`. PRs get an automatic Copilot review within minutes; triage its comments before merging.
- Commits reference **`Refs #40`**; the PR body says `Closes #40` (main IS the default branch now, so it auto-closes).
- **Pre-made design decisions (from the blindspot pass — binding):**
  - **Enter-to-search only.** No debounced live search: `internal/source/httpclient` has no 429/backoff handling.
  - **Single source per search.** Default = first configured source, sorted alphabetically (mirrors `resolveSource`/`getConfiguredSources` in `cmd/lmm/helpers.go:86`); the active source is displayed and cycleable with `s`.
  - **Results mark already-installed mods** (Status "installed"), like `lmm search` does.
  - **Detail panel renders search-result fields only** — no follow-up `GetMod` call.
  - **No category/tag filters** this phase.
  - **`views/` split scoped to the search sub-model only** — one new file `internal/tui/search.go` in package `tui`; no new package (a package split would force exporting theme/model internals for zero Phase 4 benefit — revisit in Phase 5).
- Pagination mirrors the CLI picker (`cmd/lmm/install.go:198-286`): **page size 10, 0-based pages, re-query per page, `n`/`p` keys**, TotalCount-driven "more" hint with a full-page fallback when TotalCount is 0.
- Generation-tagged search results: a result/error message whose `gen` doesn't match the model's current generation is **discarded**. Each new search cancels the previous context AND bumps the generation.
- `domain.ErrAuthRequired` renders as a first-class search state with the exact remedy text `run 'lmm auth login <source>'`.
- Unknown values render `?` (existing `countLabel` convention); never fake data. Prototype mode stays side-effect-free.
- TDD per task (compile failure counts as RED); `go test ./...`, `go vet ./...` green; `gofmt -l cmd internal/tui` prints nothing (known pre-existing offender `internal/storage/cache/cache.go` is out of scope — never touch it).
- **Snapshot regeneration is required in any task that changes view output**: `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots`, commit the changed `.ansi` files, and state in the report which files changed and why.
- MINOR version bump to **1.5.0** at the end (new feature), CHANGELOG + README updates, separate `chore:` version commit.

**Baseline (verified):** `main` at the PR #39 merge; only `main` exists locally/remotely; suite green. Current TUI: `Model` in `internal/tui/app.go` with `provider DataProvider`, `keys KeyMap`, async `loadData` (`dataLoadedMsg`/`loadFailedMsg`), `m.selected map[Screen]int`, `screenView()` state gating, `searchView()` rendering a static empty-state line. `DataProvider` (internal/tui/service.go) currently has `Summary`, `InstalledMods`, `SearchResults` (placeholder), `Profiles`. `KeyMap` (internal/tui/keys.go) routes everything via `key.Matches` in `updateKey`; `Search` binding is `/`+`3`.

---

### Task 1: DataProvider v2 — Overview, Sources, Search; prototype provider

**Files:**
- Modify: `internal/tui/service.go`
- Modify: `internal/tui/prototype/data.go` (add Summary/Downloads demo fields)
- Test: `internal/tui/service_test.go`, `internal/tui/prototype/data_test.go`

**Interfaces:**
- Produces (consumed by Tasks 2–5):

```go
const SearchPageSize = 10 // mirrors the CLI picker's displayPageSize

type ModItem struct {
	Name            string
	Author          string
	Version         string
	Source          string
	Status          string
	Summary         string
	Downloads       int64
	Endorsements    int64
	HasEndorsements bool
}

// SearchPage is one page of search results for one source/query.
type SearchPage struct {
	Results    []ModItem
	Query      string
	Source     string
	Page       int // 0-based
	PageSize   int
	TotalCount int // 0 if the source doesn't report totals
}

type DataProvider interface {
	// Overview returns the dashboard summary and installed-mod rows from a
	// single underlying fetch.
	Overview(ctx context.Context) (Summary, []ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
	// Sources lists the game's configured source IDs, sorted; index 0 is the
	// default (mirrors the CLI's resolveSource).
	Sources() []string
	Search(ctx context.Context, source, query string, page int) (SearchPage, error)
}
```

- `Summary`, `ProfileItem` unchanged. The old `Summary(ctx)`, `InstalledMods(ctx)`, `SearchResults(ctx)` methods are **removed** from the interface and both implementations.

- [ ] **Step 1: Write the failing tests**

Replace `TestPrototypeProviderMirrorsFakeData`'s method calls in `internal/tui/service_test.go` with the v2 interface and add search behavior tests:

```go
func TestPrototypeProviderOverviewMirrorsFakeData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()
	data := prototype.Load()

	summary, mods, err := provider.Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, data.Game.Name, summary.GameName)
	require.Equal(t, data.Stats.Installed, summary.Installed)
	require.Len(t, mods, len(data.InstalledMods))
	require.Equal(t, data.InstalledMods[0].Name, mods[0].Name)
}

func TestPrototypeProviderSources(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"nexusmods"}, NewPrototypeProvider().Sources())
}

func TestPrototypeProviderSearchFiltersCannedResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()

	page, err := provider.Search(ctx, "nexusmods", "frost", 0)
	require.NoError(t, err)
	require.Equal(t, "frost", page.Query)
	require.Equal(t, "nexusmods", page.Source)
	require.Len(t, page.Results, 1)
	require.Equal(t, "Frostfall", page.Results[0].Name)
	require.Equal(t, 1, page.TotalCount)

	all, err := provider.Search(ctx, "nexusmods", "", 0)
	require.NoError(t, err)
	require.Len(t, all.Results, len(prototype.Load().SearchResults), "empty query returns everything")
}
```

Keep the `Profiles` assertions from the old test (unchanged method).

- [ ] **Step 2: Run to verify RED**

`go test ./internal/tui -run TestPrototypeProvider -v` — compile FAIL (`provider.Overview` undefined).

- [ ] **Step 3: Implement**

In `internal/tui/service.go`: add the constants/types/interface above; rewrite the prototype provider:

```go
func (p prototypeProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	return Summary{
		GameName:    p.data.Game.Name,
		ProfileName: p.data.Profile.Name,
		Installed:   p.data.Stats.Installed,
		Enabled:     p.data.Stats.Enabled,
		Updates:     p.data.Stats.Updates,
		Conflicts:   p.data.Stats.Conflicts,
	}, modItems(p.data.InstalledMods), nil
}

func (p prototypeProvider) Sources() []string {
	return []string{"nexusmods"}
}

func (p prototypeProvider) Search(_ context.Context, source, query string, _ int) (SearchPage, error) {
	all := modItems(p.data.SearchResults)
	matched := make([]ModItem, 0, len(all))
	for _, item := range all {
		if strings.Contains(strings.ToLower(item.Name), strings.ToLower(query)) {
			matched = append(matched, item)
		}
	}
	return SearchPage{
		Results:    matched,
		Query:      query,
		Source:     source,
		Page:       0,
		PageSize:   SearchPageSize,
		TotalCount: len(matched),
	}, nil
}
```

Extend `modItems` to copy the new fields, and add `Summary string` + `Downloads int64` to `prototype.Mod` with short demo values in `Load()` (e.g. Campfire: "Camping and survival skill system.", 4_200_000). Update `prototype/data_test.go` only if its invariants break (they shouldn't — it checks counts and the active profile).

Delete the now-unimplemented old methods from the prototype provider. **`app.go` will not compile after this task alone** — that is expected; Task 3 rewires the Model. To keep the branch bisectable, apply the minimal mechanical shim in this task: change `loadData` to call `Overview`/`Profiles` and delete its `SearchResults` call plus the `searchResults` payload from `dataLoadedMsg` (the field `m.searchResults` and `searchView`'s use of it become an empty-slice render until Task 4 replaces the view). Adjust `failingProvider`/`emptyProvider` in `app_test.go` to the v2 interface.

- [ ] **Step 4: Run the full suite** — `go test ./... && go vet ./...`; `gofmt -l internal/tui` silent. Snapshot check: regenerate; the search view now renders its empty state unconditionally — if captures change, include them.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/service.go internal/tui/prototype/data.go internal/tui/service_test.go internal/tui/prototype/data_test.go internal/tui/app.go internal/tui/app_test.go docs/assets/tui
git commit -m "feat(tui): evolve DataProvider — Overview single-fetch, Sources, Search

Refs #40"
```

---

### Task 2: CoreProvider v2 — single fetch, Sources, Search with installed markers

**Files:**
- Modify: `internal/tui/service_core.go`
- Test: `internal/tui/service_core_test.go`

**Interfaces:**
- Consumes: `core.Service.SearchMods(ctx, sourceID, gameID, query, category string, tags []string, page, pageSize int) (source.SearchResult, error)` (service.go:107 — already maps `game.SourceIDs`), `source.SearchResult{Mods []domain.Mod; TotalCount, Page, PageSize int}`, `core.Service.RegisterSource(source.ModSource)`, `domain.ModKey(sourceID, modID) string`, `source.ModSource` interface (internal/source/source.go:36).
- Produces: `NewCoreProvider` unchanged signature; v2 methods.

- [ ] **Step 1: Write the failing tests**

In `internal/tui/service_core_test.go` (external `tui_test` package): update the fixture's assertions to `Overview`, and add a stub source so `Search` can be tested against the real service path:

```go
// stubSource implements source.ModSource with canned search results.
// Only Search and identity methods matter; the rest are unreachable in these tests.
type stubSource struct {
	result source.SearchResult
	err    error
}

func (s *stubSource) ID() string      { return "stub" }
func (s *stubSource) Name() string    { return "Stub Source" }
func (s *stubSource) AuthURL() string { return "" }
func (s *stubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return s.result, s.err
}
func (s *stubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
```

If `source.ModSource` has methods beyond these (check `internal/source/source.go` — e.g. `GetDownloadURL`), stub them the same way: the compiler is the checklist.

```go
func TestCoreProviderOverviewSingleFetch(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	summary, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, summary.Installed)
	require.Equal(t, 1, summary.Enabled)
	require.Equal(t, -1, summary.Updates)
	require.Len(t, mods, 2)
}

func TestCoreProviderSources(t *testing.T) {
	provider, _, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"nexusmods": "testgame", "stub": "testgame"}

	require.Equal(t, []string{"nexusmods", "stub"}, provider.Sources())
}

func TestCoreProviderSearchMarksInstalled(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{result: source.SearchResult{
		Mods: []domain.Mod{
			{ID: "101", SourceID: "stub", Name: "SkyUI-Stub", Author: "a", Version: "5.2"},
			{ID: "999", SourceID: "stub", Name: "NewMod", Author: "b", Version: "1.0"},
		},
		TotalCount: 2, Page: 0, PageSize: 10,
	}})
	// Fixture installed mod 101 under sourceID "nexusmods"; install one under "stub" too:
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "101", SourceID: "stub", GameID: game.ID, Name: "SkyUI-Stub", Version: "5.2"},
		ProfileName: "default", Enabled: true,
	}))

	page, err := provider.Search(context.Background(), "stub", "sky", 0)
	require.NoError(t, err)
	require.Equal(t, "sky", page.Query)
	require.Equal(t, "stub", page.Source)
	require.Equal(t, 2, page.TotalCount)
	require.Len(t, page.Results, 2)

	byName := map[string]tui.ModItem{}
	for _, r := range page.Results {
		byName[r.Name] = r
	}
	require.Equal(t, "installed", byName["SkyUI-Stub"].Status)
	require.Equal(t, "available", byName["NewMod"].Status)
}

func TestCoreProviderSearchPropagatesAuthRequired(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{err: fmt.Errorf("%w: key required", domain.ErrAuthRequired)})

	_, err := provider.Search(context.Background(), "stub", "x", 0)
	require.ErrorIs(t, err, domain.ErrAuthRequired)
}
```

(Add `"errors"`, `"fmt"`, and the `source` package to the test imports. Update the two old `TestCoreProviderSummary`/`TestCoreProviderInstalledMods` tests to go through `Overview`; delete `TestCoreProviderSearchResultsAreEmptyUntilPhase4`.)

- [ ] **Step 2: Verify RED** — compile FAIL (`provider.Overview`, `Sources`, new `Search` undefined).

- [ ] **Step 3: Implement** in `internal/tui/service_core.go`:

```go
func (p *coreProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	mods, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}

	enabled := 0
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		if mod.Enabled {
			enabled++
		}
		items = append(items, ModItem{
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.SourceID,
			Status:  installedModStatus(mod),
		})
	}

	summary := Summary{
		GameName:    p.game.Name,
		ProfileName: p.profile,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1,
		Conflicts:   -1,
	}
	return summary, items, nil
}

func (p *coreProvider) Sources() []string {
	sources := make([]string, 0, len(p.game.SourceIDs))
	for id := range p.game.SourceIDs {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	return sources
}

func (p *coreProvider) Search(ctx context.Context, sourceID, query string, page int) (SearchPage, error) {
	result, err := p.svc.SearchMods(ctx, sourceID, p.game.ID, query, "", nil, page, SearchPageSize)
	if err != nil {
		return SearchPage{}, fmt.Errorf("searching %s for %q: %w", sourceID, query, err)
	}

	installed, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return SearchPage{}, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}
	installedKeys := make(map[string]bool, len(installed))
	for _, mod := range installed {
		installedKeys[domain.ModKey(mod.SourceID, mod.ID)] = true
	}

	items := make([]ModItem, 0, len(result.Mods))
	for _, mod := range result.Mods {
		status := "available"
		if installedKeys[domain.ModKey(mod.SourceID, mod.ID)] {
			status = "installed"
		}
		item := ModItem{
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.SourceID,
			Status:  status,
			Summary: mod.Summary,
			Downloads: mod.Downloads,
		}
		if mod.Endorsements != nil {
			item.Endorsements = *mod.Endorsements
			item.HasEndorsements = true
		}
		items = append(items, item)
	}

	return SearchPage{
		Results:    items,
		Query:      query,
		Source:     sourceID,
		Page:       page,
		PageSize:   SearchPageSize,
		TotalCount: result.TotalCount,
	}, nil
}
```

Delete the old `Summary`/`InstalledMods`/`SearchResults` methods (fold the status-mapping loop into `Overview` as above; keep `installedModStatus`). Add `"sort"` import. Note the `%w` wrap preserves `errors.Is(err, domain.ErrAuthRequired)`.

- [ ] **Step 4: Full suite** — `go test ./... && go vet ./...`, gofmt silent.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/service_core.go internal/tui/service_core_test.go
git commit -m "feat(tui): CoreProvider v2 — Overview, Sources, real Search with installed markers

Refs #40"
```

---

### Task 3: Program-context threading (Options.Ctx)

**Files:**
- Modify: `internal/tui/app.go`, `cmd/lmm/tui.go`
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Produces: `Options{Theme string; Provider DataProvider; Ctx context.Context}`; `Model.ctx context.Context` (Background fallback when nil). Task 4's search commands derive per-search contexts from `m.ctx`.

- [ ] **Step 1: Write the failing test**

```go
func TestModelUsesProvidedContext(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	var seen context.Context
	provider := recordingProvider{onOverview: func(c context.Context) { seen = c }}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Ctx: ctx})
	require.NoError(t, err)

	model.Init()()
	require.Equal(t, "marker", seen.Value(ctxKey{}))
}
```

with a small `recordingProvider` in `app_test.go` that wraps `NewPrototypeProvider()` and calls `onOverview(ctx)` before delegating `Overview`; other methods delegate directly.

- [ ] **Step 2: Verify RED** — compile FAIL (`Options.Ctx` undefined).

- [ ] **Step 3: Implement** — add `Ctx` to `Options`; in `NewModel`, `if options.Ctx == nil { options.Ctx = context.Background() }`, store `ctx: options.Ctx` on `Model`; `loadData` uses `m.ctx` instead of `context.Background()`. In `cmd/lmm/tui.go`, pass `Ctx: cmd.Context()` (prototype path) and `Ctx: ctx` (real path inside `withGameService`).

- [ ] **Step 4: Full suite**; no view changes → assert snapshots unchanged after regeneration.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go cmd/lmm/tui.go
git commit -m "feat(tui): thread program context into the model and data loads

Refs #40"
```

---

### Task 4: Search sub-model — focus routing, generation-tagged async search

**Files:**
- Create: `internal/tui/search.go`
- Modify: `internal/tui/app.go` (Model wiring, updateKey routing, itemCount), `internal/tui/keys.go`
- Test: `internal/tui/search_test.go` (new), `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `DataProvider.Search`/`Sources` (Tasks 1–2), `m.ctx` (Task 3), `github.com/charmbracelet/bubbles/textinput`, `domain.ErrAuthRequired`.
- Produces (consumed by Task 5's rendering):

```go
type searchState int

const (
	searchIdle searchState = iota
	searchLoading
	searchReady
	searchFailed
	searchAuthRequired
)

type searchModel struct {
	input      textinput.Model
	sources    []string
	sourceIdx  int
	state      searchState
	page       SearchPage
	err        error
	authSource string
	gen        int
	cancel     context.CancelFunc
}

type searchResultMsg struct {
	gen  int
	page SearchPage
}

type searchFailedMsg struct {
	gen    int
	err    error
	source string
}
```

KeyMap additions: `Submit` (enter), `Blur` (esc), `NextPage` (n), `PrevPage` (p), `CycleSource` (s).

- [ ] **Step 1: Write the failing tests** (`internal/tui/search_test.go`, package `tui`)

```go
func searchScreenModel(t *testing.T) Model {
	t.Helper()
	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	return updateWithRunes(t, model, "3") // jump to search screen (blurred)
}

func TestSlashFocusesSearchInputOnSearchScreen(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	require.True(t, model.search.input.Focused())
}

func TestTypingWhileFocusedDoesNotTriggerGlobalKeys(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	for _, r := range "quest124" { // q would quit; 1/2/4 would jump screens
		model = updateWithRunes(t, model, string(r))
	}
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.Equal(t, "quest124", model.search.input.Value())
}

func TestEscBlursSearchInput(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, updated.(Model).search.input.Focused())
}

func TestEnterSubmitsSearchAndRendersResults(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	for _, r := range "frost" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Equal(t, searchLoading, model.search.state)
	require.NotNil(t, cmd)

	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, model.search.page.Results, 1)
	require.Equal(t, "Frostfall", model.search.page.Results[0].Name)
}

func TestStaleSearchResultsAreDiscarded(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 5
	model.search.state = searchLoading

	updated, _ := model.Update(searchResultMsg{gen: 4, page: SearchPage{Query: "stale"}})
	require.Equal(t, searchLoading, updated.(Model).search.state, "stale gen must be ignored")
}

func TestAuthRequiredBecomesFirstClassState(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 1
	updated, _ := model.Update(searchFailedMsg{
		gen:    1,
		err:    fmt.Errorf("%w: key required", domain.ErrAuthRequired),
		source: "nexusmods",
	})
	m := updated.(Model)
	require.Equal(t, searchAuthRequired, m.search.state)
	require.Equal(t, "nexusmods", m.search.authSource)
}

func TestCycleSourceKey(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.sources = []string{"curseforge", "nexusmods"}
	model = updateWithRunes(t, model, "s")
	require.Equal(t, 1, model.search.sourceIdx)
	model = updateWithRunes(t, model, "s")
	require.Equal(t, 0, model.search.sourceIdx, "cycling wraps")
}

func TestPaginationKeysRequeryWithinBounds(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 25}

	updated, cmd := model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "next page issues a search command")
	_ = updated

	model.search.page.Page = 0
	_, cmd = model.Update(keyRunes("p"))
	require.Nil(t, cmd, "prev on page 0 is a no-op")
}
```

Add tiny helper `keyRunes(s string) tea.KeyMsg` (or reuse `updateWithRunes`'s construction) and imports (`fmt`, `domain`, `tea`). Note `moveSelection`'s `itemCount(ScreenSearch)` must become `len(m.search.page.Results)` — update `TestSelectionMovementIsClamped` if it exercises the search screen.

- [ ] **Step 2: Verify RED** — compile FAIL (`model.search` undefined).

- [ ] **Step 3: Implement**

`internal/tui/search.go` — the sub-model plus command constructor:

```go
func newSearchModel(provider DataProvider) searchModel {
	input := textinput.New()
	input.Placeholder = "search the archives"
	input.CharLimit = 120
	return searchModel{input: input, sources: provider.Sources()}
}

func (s searchModel) source() string {
	if len(s.sources) == 0 {
		return ""
	}
	return s.sources[s.sourceIdx]
}
```

On `Model`: add `search searchModel` (init in `NewModel` via `newSearchModel(options.Provider)`). Search command (method on `Model` so it reaches provider/ctx):

```go
// startSearch cancels any in-flight search, bumps the generation, and returns
// the model plus a command executing the new query.
func (m Model) startSearch(query string, page int) (Model, tea.Cmd) {
	if m.search.cancel != nil {
		m.search.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.search.cancel = cancel
	m.search.gen++
	m.search.state = searchLoading

	gen := m.search.gen
	provider := m.provider
	source := m.search.source()
	return m, func() tea.Msg {
		result, err := provider.Search(ctx, source, query, page)
		if err != nil {
			return searchFailedMsg{gen: gen, err: err, source: source}
		}
		return searchResultMsg{gen: gen, page: result}
	}
}
```

Message handling in `Update` (new cases; both discard stale generations):

```go
	case searchResultMsg:
		if msg.gen != m.search.gen {
			return m, nil
		}
		m.search.state = searchReady
		m.search.page = msg.page
		m.selected[ScreenSearch] = 0
		return m, nil
	case searchFailedMsg:
		if msg.gen != m.search.gen {
			return m, nil
		}
		if errors.Is(msg.err, domain.ErrAuthRequired) {
			m.search.state = searchAuthRequired
			m.search.authSource = msg.source
			return m, nil
		}
		m.search.state = searchFailed
		m.search.err = msg.err
		return m, nil
```

Focus routing at the TOP of `updateKey` (before the global switch):

```go
	if m.screen == ScreenSearch && m.search.input.Focused() {
		switch {
		case key.Matches(msg, m.keys.Quit) && msg.String() == "ctrl+c":
			return m, tea.Quit
		case key.Matches(msg, m.keys.Blur):
			m.search.input.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Submit):
			m.search.input.Blur()
			return m.startSearch(m.search.input.Value(), 0)
		default:
			var cmd tea.Cmd
			m.search.input, cmd = m.search.input.Update(msg)
			return m, cmd
		}
	}
```

(`q` must type a letter while focused — hence the explicit `ctrl+c`-only quit check.) Blurred-on-search-screen keys, added to the global switch **before** the `Search` jump case:

```go
	case key.Matches(msg, m.keys.Search):
		if m.screen == ScreenSearch {
			m.search.input.Focus()
			return m, textinput.Blink
		}
		m.screen = ScreenSearch
		return m, nil
	case key.Matches(msg, m.keys.NextPage):
		if m.screen == ScreenSearch && m.search.state == searchReady && m.search.hasNextPage() {
			return m.startSearch(m.search.page.Query, m.search.page.Page+1)
		}
		return m, nil
	case key.Matches(msg, m.keys.PrevPage):
		if m.screen == ScreenSearch && m.search.state == searchReady && m.search.page.Page > 0 {
			return m.startSearch(m.search.page.Query, m.search.page.Page-1)
		}
		return m, nil
	case key.Matches(msg, m.keys.CycleSource):
		if m.screen == ScreenSearch && len(m.search.sources) > 1 {
			m.search.sourceIdx = (m.search.sourceIdx + 1) % len(m.search.sources)
		}
		return m, nil
```

with `hasNextPage` on `searchModel` mirroring the CLI picker's logic (install.go:244-251):

```go
func (s searchModel) hasNextPage() bool {
	if s.page.TotalCount > 0 {
		return (s.page.Page+1)*s.page.PageSize < s.page.TotalCount
	}
	return len(s.page.Results) == s.page.PageSize // full page ⇒ maybe more
}
```

`keys.go`: add `Submit` ("enter", help "search"), `Blur` ("esc", help "cancel input"), `NextPage` ("n"), `PrevPage` ("p"), `CycleSource` ("s") to `KeyMap`+`DefaultKeyMap`. **Conflict check:** `Select` also binds "enter" — the focused branch consumes enter before the global switch, and the global `Select` case only acts on `ScreenDashboard`, so no shadowing; note this in the commit body. `itemCount(ScreenSearch)` returns `len(m.search.page.Results)`; delete the `m.searchResults` Model field and its remaining uses (Task 1's shim).

- [ ] **Step 4: Full suite + snapshot regeneration** (search view content changes: idle state now shows the input — Task 5 rewrites rendering, but the placeholder line changes now if `searchView` renders the input; keep `searchView` minimally rendering `m.search.input.View()` above the existing empty-state so tests/captures stay honest).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/search.go internal/tui/search_test.go internal/tui/app.go internal/tui/app_test.go internal/tui/keys.go docs/assets/tui
git commit -m "feat(tui): search sub-model with focus routing and cancellable queries

Focused input suspends the global keymap (ctrl+c still quits, esc blurs,
enter submits). Searches carry a generation tag: new queries cancel the
in-flight context and stale results/errors are discarded. n/p paginate
per the CLI picker contract; s cycles the game's configured sources.

Refs #40"
```

---

### Task 5: Search view rendering — results, installed markers, detail panel, states

**Files:**
- Modify: `internal/tui/app.go` (`searchView`, `helpView`)
- Test: `internal/tui/app_test.go` or `internal/tui/search_test.go`

**Interfaces:** Consumes Task 4's `searchModel` states and Task 1's `SearchPage`/`ModItem`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSearchViewRendersStates(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)

	require.Contains(t, model.View(), "search the archives", "idle shows the input placeholder")

	model.search.state = searchAuthRequired
	model.search.authSource = "nexusmods"
	view := model.View()
	require.Contains(t, view, "lmm auth login nexusmods")

	model.search.state = searchFailed
	model.search.err = errors.New("the aether is down")
	require.Contains(t, model.View(), "the aether is down")

	model.search.state = searchReady
	model.search.page = SearchPage{
		Query: "sky", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 12,
		Results: []ModItem{
			{Name: "SkyUI", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "UI overhaul.", Downloads: 1000, Endorsements: 50, HasEndorsements: true},
			{Name: "SkyFresh", Author: "someone", Version: "1.0", Status: "available"},
		},
	}
	view = model.View()
	require.Contains(t, view, "SkyUI")
	require.Contains(t, view, "installed")
	require.Contains(t, view, "Page 1/2")
	require.Contains(t, view, "UI overhaul.", "detail panel shows the selected result's summary")
}

func TestSearchViewStaysWithinBounds(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // 100x30
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", PageSize: 10, TotalCount: 10,
		Results: []ModItem{{Name: "A", Status: "available"}}}
	require.Equal(t, model.availableWidth(), lipgloss.Width(model.screenView()))
	require.Equal(t, model.availableContentHeight(), lipgloss.Height(model.screenView()))
}
```

- [ ] **Step 2: Verify RED** — the idle assertion may pass (input renders from Task 4); the ready/auth/detail assertions FAIL against the current placeholder view.

- [ ] **Step 3: Implement** — rewrite `searchView()`:

- Header line: `ARCHIVE SEARCH  [source: <s.source()>  (s cycles)]` (theme `PanelTitle` + `MutedText`), then `m.search.input.View()`.
- `searchIdle`: muted hint `"/ focus · enter search · s source"` — no fake data.
- `searchLoading`: `"Consulting the archive index..."`.
- `searchAuthRequired`: `DangerText` line `Authentication required for <authSource>.` + plain line `Run 'lmm auth login <authSource>' in a shell, then search again.`
- `searchFailed`: `DangerText` with `m.search.err.Error()`.
- `searchReady`: two-pane layout like `commanderDashboardView` (reuse `panelWithHeight` + `lipgloss.JoinHorizontal`): left pane = result rows via `m.row(i, ...)` with columns `name / version / status` (status styled `WarningText` when "installed" so it pops); right pane = detail of `m.selected[ScreenSearch]`: Name, Author, Version, Source, Status, Downloads (`fmt` with thousands via plain %d — no new deps), Endorsements (`?` unless `HasEndorsements`), and Summary wrapped with `lipgloss.NewStyle().Width(...)` — width-constrained rendering only; get the style from the theme (`MutedText.Width(w)`), never a bare `NewStyle` (theme principle).
- Footer: `Page X/Y (N results) · n next · p prev` — Y from TotalCount when known, else `Page X` + `n next` only when `hasNextPage()`.
- Empty results (`searchReady`, zero rows): `No archives matched "<query>" on <source>.`
- `helpView()` gains the search keys (`/ focus · enter search · esc cancel · n/p pages · s source`).

The whole view must fill `availableWidth()`/`availableContentHeight()` exactly (the existing screen-size tests iterate all screens — they will enforce this).

- [ ] **Step 4: Full suite + regenerate snapshots** (search captures change: input + hint replace the old placeholder).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go internal/tui/search_test.go docs/assets/tui
git commit -m "feat(tui): render search results, detail panel, and search states

Refs #40"
```

---

### Task 6: Docs, changelog, version 1.5.0, final review, PR

**Files:**
- Modify: `README.md` (Terminal UI section: search usage + new keys), `CHANGELOG.md`, `cmd/lmm/root.go` (version → "1.5.0"), `cmd/lmm/tui.go` (Long help text mentions search)

**Steps:**

- [ ] README Terminal UI section: add search usage (`/` to focus, enter to search, `n`/`p` pages, `s` source cycle, `esc` cancel; results mark installed mods; auth-required shows the login command). Update the key list.
- [ ] `cmd/lmm/tui.go` Long description: replace "Search shows an honest placeholder" phrasing (if present) with the real capability; keep the read-only caveat (browsing/searching never installs).
- [ ] CHANGELOG: new `## [1.5.0] - <date>` section under the emptied `[Unreleased]`: Added — **TUI search** (real source search with cancellation, installed markers, detail panel, pagination, source cycling, auth-required guidance); Changed — DataProvider boundary v2 (single-fetch Overview; program-context threading). Update comparison links (`v1.4.0...v1.5.0`, Unreleased from v1.5.0) matching the existing v-prefixed pattern.
- [ ] Version bump: `cmd/lmm/root.go` → `"1.5.0"` as its own `chore: bump version to 1.5.0` commit.
- [ ] Verification: `go fmt ./... && go vet ./... && go test ./...`; build; `--version` prints 1.5.0; `tui --help` reflects search. (If `go fmt` touches `internal/storage/cache/cache.go`, check it out — out of scope.)
- [ ] Final whole-branch review (controller dispatches per SDD skill), fix wave if needed, then push and open PR to `main`: title `feat(tui): search and detail browsing (Phase 4)`, body summarizing the above + `Closes #40`, note the interactive smoke test (`lmm tui`, `/`, query, pagination, `s`, auth-required path with a logged-out source) is deferred to the user. Triage the auto-Copilot review before merging.

---

## Deliberately NOT in this plan

- Category/tag filters, live/debounced search, `GetMod` detail fetch, image thumbnails (pre-made decisions / roadmap deferrals).
- Install-from-search (Phase 5), update/conflict workflows (Phase 6) — issue #37.
- 429/backoff handling in httpclient — prerequisite if live search is ever wanted; separate issue.
- The `views/` package split beyond the search sub-model file; cmd test-globals helper chore (#37).
