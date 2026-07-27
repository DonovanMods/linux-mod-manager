# Window-Sized Search Fetch Implementation Plan (#111 Tier 1 — EPIC #104)

**Goal:** The TUI's search fetch size derives from the visible results-pane budget at query time instead of the fixed 10-per-source, so a tall terminal fills its pane and short ones stop over-fetching. Tier 2 (fetch-to-fill looping) stays with #109 — NOT this plan.

**Architecture:** `DataProvider.Search` gains a `pageSize int` parameter (internal interface; all fakes updated — `stubProvider` absorbs most). The Model computes the fetch size once per query session (`m.searchFetchSize()`, derived from `availableContentHeight()` minus search chrome, clamped) at submit, stores it in search state, and every fetch of that session (submit + n/p requeries) passes it — resize takes effect on the NEXT query, keeping pagination arithmetic self-consistent within a session.

## Global Constraints

- Branch: `feat/window-sized-search-fetch` off `main`; PR closes NOTHING automatically — #111 gets a completion comment noting Tier 1 shipped and Tier 2 remains with #109 (issue stays open or closes per PR review; default: close #111 with the comment noting the Tier-2 handoff, since the issue's own text scopes Tier 2 to #109's machinery). Decision: CLOSE #111, Tier 2 tracked by #109.
- Version: MINOR → 1.19.0 (user-visible behavior change in fetch semantics; internal interface change).
- TDD RED-first; gofmt/vet/full suite per commit; add files BY NAME; goldens: the 120x36 search capture MAY change (bigger fetch → more canned rows rendered? NO — prototype canned set is fixed at 10 results and snapshot harness injects populatedSearchPage directly, bypassing Search; predict NO golden changes, audit in finalize).
- CLI unaffected (its own searchPageSize(limit) path). Single-source AND all-sources both use the computed size (SearchAllSources takes pageSize already; SearchMods too — service_core.go:289,317 just swap the constant for the param).
- Clamp: fetch size = max(pane row budget, 10) capped at 50 — never fetch fewer than today's behavior, never hammer sources for huge windows. Constants documented.

## Tasks

### Task 1: thread pageSize through and derive it

**Files:** `internal/tui/service.go` (interface + prototype), `internal/tui/service_core.go` (coreProvider), `internal/tui/search.go` (state + derivation), `internal/tui/mutations.go`/`app.go` (call sites), all test fakes (stubProvider et al.), tests.

1. RED tests first:
   a. Derivation: `m.searchFetchSize()` at 80x24 ≈ pane budget (compute expected from the real chrome numbers — header 2 + footer 1 + panel border 2 off availableContentHeight; assert exact value), floors at 10 (40x12), caps at 50 (very tall terminal e.g. 120x120).
   b. Stickiness: submit at one size, resize the model, press n — the requery passes the ORIGINAL session's size (capture via a recording fake provider); a fresh submit after resize uses the new size.
   c. Provider passthrough: coreProvider passes the param to SearchMods/SearchAllSources (recording/service-level test per existing service_core test patterns); prototypeProvider paginates its canned set by the given size (and its all-sources Exhausted/AttemptedCount behavior from v1.18.1 stays intact).
2. Implement: interface param; `m.search.fetchSize` set in startSearch when page==0 (or on submit path), passed on every fetch; providers use it with `if pageSize <= 0 { pageSize = SearchPageSize }` fallback documented (defensive for stray callers); keep the SearchPageSize const as the floor/fallback with an updated doc comment.
3. Existing search tests: many construct SearchPage directly (unaffected); fakes implementing DataProvider need the new signature — mechanical, stubProvider covers embedders.
4. Full suite + vet. Commit: `feat(tui): search fetch size derives from the visible pane, sticky per query (#111)`.

### Task 2: finalize

1. Verification (build/fmt/vet/test); goldens regen audit: predict ZERO changes (harness bypasses Search) — stop if any.
2. CHANGELOG `[1.19.0]`: Changed — the TUI sizes search fetches to the window (pane-budget-derived, min 10 / max 50 per source, fixed at query time; resize applies on the next search) instead of always 10 per source. Bump root.go → 1.19.0. Archive plan doc. Separate commits per repo convention.
3. PR (closes #111 with Tier-2-to-#109 note); smoke gate note (TUI search fetch behavior changed).

## Execution notes
Single implementation task (interface change ripples but is one coherent change), mid-tier implementer + reviewer; final whole-branch review most-capable (small).
