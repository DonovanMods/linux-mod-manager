# TUI mod details view — design (#86)

**Issue:** [#86](https://github.com/DonovanMods/linux-mod-manager/issues/86) — TUI: mod details view (parity with `lmm mod show`)
**Branch:** `feat/86-mod-details` → PR `--base develop`
**Version:** no bump (bump-at-release); ships MINOR in the next batch alongside #224
**Depends on:** #224's context-view host (`internal/tui/contextview.go`), merged at `aeff971`
**Feeds:** #87 (mod-level changelog) — this view is its landing slot

---

## Problem

`lmm mod show` (`cmd/lmm/mod.go:606-753`) prints a mod's full metadata; the TUI
has no equivalent. The search detail pane (`internal/tui/app.go:1841-1897`)
renders 8 fixed fields plus a clipped Summary, and the Installed Mods screen
offers nothing at all. This violates the standing CLI/TUI parity directive.

It is a plumbing gap, not only a rendering gap: `ModItem`
(`internal/tui/service.go:66-162`) carries no `Description`, `PictureURL`, or
`SourceURL`, and nothing under `internal/tui/` calls `core.Service.GetMod`
except `AvailableVersions`.

---

## Ratified decisions

Recorded so they are not re-litigated during implementation.

1. **Fetch policy: local-first, enrich in background.** The view opens
   instantly from the `ModItem` already in hand and fills in from the network
   when the fetch lands. It never blocks on a spinner and never fails closed.
   Rejected: fetch-on-open (an offline user loses data they already had) and a
   Health-style explicit `f`-for-full tier (makes the common case two
   keystrokes).
2. **Description is HTML-cleaned in BOTH interfaces.** `core.CleanChangelog`
   (`internal/core/changelog.go:28-40`) is already the shared CLI+TUI
   HTML→terminal cleaner — built for "readable terminal/TUI display", full text
   with truncation left to callers. It is misnamed for its job, not
   mis-scoped. Point it at `Description` too. `lmm mod show` prints raw `<p>`
   tags today; that is a wart, and fixing it in one interface only would create
   exactly the divergence the parity directive forbids. The CLI keeps its
   2000-char cap (`mod.go:709`) because it is a one-shot dump; the TUI scrolls
   instead. **This changes `mod show` output — CHANGELOG entry required.**
3. **The host is generalized so details renders on the screen it was opened
   from.** Rejected: pushing onto Health as shipped (the nav bar would read
   "Health" while showing mod details), and `infoOverlay` (it holds a static
   `[]string` snapshot and cannot re-render as the background fetch lands —
   it structurally fights decision 1).

---

## Accepted deviations (recorded during implementation)

1. **Profile resolution order in `doModShow`.** The pre-refactor code called
   `svc.GetMod` before `resolveProfile`; because `Service.ModDetail` receives an
   already-resolved profile, the CLI now resolves the profile first. When
   **both** the profile and the mod ID are invalid, the surfaced error changes
   from "mod not found" to the profile error. Both are correct errors; only the
   ordering differs, and each single-failure case still produces its own. This
   is the only intended behavioral difference in the extraction.

2. **`--json` keeps the raw description.** Decision 2's HTML cleaning applies to
   human-readable rendering only — the CLI's terminal output and the TUI view.
   `mod show --json` is a machine contract and a consumer may want the original
   markup, so it emits the source's description unchanged. Pinned by
   `TestDoModShow_JSONDescriptionStaysRaw`.

3. **`m.selectedMod()` was not reusable for the Search screen.** It hardcodes
   `m.selected[ScreenInstalledMods]` and indexes the installed list, so opening
   details from Search would have shown an unrelated mod. Implementation added
   `selectedModForDetails`, which branches by screen, bounds-checks, and gates
   on `searchReady`.

4. **The scroll indicator is budgeted _within_ the content height.**
   `len(Lines(w, h)) <= h` must hold for every input: the host applies its own
   `clampLines` on top, so a view that returns `h+1` lines gets double-clamped
   and its truthful `↓ N more` replaced by a misleading `+N more` from clamp
   math.

---

## Architecture

### 1. Generalize the context-view host

The host interface (`contextview.go:10-19`) is already generic. The _host_ is
nailed to `ScreenHealth` in four places. All four edits are subtractive.

| Location                              | Today                                                             | Change                                                                                                                                                                                                                                                                                                                                                      |
| ------------------------------------- | ----------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `contextview.go:28-32`                | `pushContext(c, from)` sets `m.screen = ScreenHealth`             | Delete that line. Signature becomes `pushContext(c contextContent)`.                                                                                                                                                                                                                                                                                        |
| `contextview.go:38-47` + `app.go:153` | `popContext` restores `m.contextReturn`                           | **Delete `contextReturn`.** With the screen no longer changing on push, `from` is provably always `m.screen`, so the field can only ever disagree with reality. `popContext` just clears `contextContent`.                                                                                                                                                  |
| `app.go:1393` `screenView()`          | `case ScreenHealth: return m.healthScreenView()` consults content | Add `if m.contextContent != nil { return m.contextView() }` **after** the `picker`/`inputModal` early-returns, so modals still outrank pushed content (matches today's key precedence at `app.go:941-955`). `healthScreenView` collapses to `healthHomeView`; the chrome-rendering body of `app.go:2131-2147` moves to `contextView()` in `contextview.go`. |
| `app.go:966`, `app.go:429`            | Both gated on `m.screen == ScreenHealth &&`                       | Drop the gate. Nav-away becomes "clear whenever `gotoScreen` runs with content pushed" — which also covers pressing the digit for the screen you are already on.                                                                                                                                                                                            |

Doc comments asserting Health-specificity must be updated in three places:
`contextview.go:5-9`, `app.go:146-152`, `internal/tui/navigation.go:15-23`.

Health's own action guards (`m.contextContent == nil` at `mutations.go:130`,
`:1554`, `:1862`) stay unchanged and still pass — they become
defense-in-depth behind the swallow rule below.

### 2. Swallow declined keys while content is pushed

**This is a required part of the generalization, not a nicety.** Today, keys
the pushed content declines fall through to the outer switch. On Health that is
harmless (its three actions are explicitly guarded). On Installed Mods it is
not: `↑/↓` would move `m.selected` invisibly underneath the details view, and
`e`/`x`/`u` would enable/uninstall the row behind it.

Mirror `updateOverlayKey`'s documented semantics (`overlay.go:46-60`: "every
other key is swallowed so nothing behind the overlay can react to it"). While
`m.contextContent != nil`, only these do anything:

- keys the content itself handles (`HandleKey` returns `handled=true`)
- `esc` — pop
- nav digits / `tab` / `shift+tab` — navigate away (implicitly pops)
- quit keys (`isQuitKey`)
- help (`?`)

Everything else is swallowed. This retires the whole class of
acting-on-the-row-underneath bugs in one rule.

### 3. Extract the mod-detail composition into core

`doModShow` composes its Installed block from three sources plus derived
display logic:

- `svc.GetInstalledMod(...)` — `mod.go:628` (DB; failure means "not installed",
  not an error — see `mod.go:618-621`)
- `config.LoadProfile(...)` + `prof.FindRef(...)` — `mod.go:639-644` (lock
  state, from profile YAML)
- `game.DeployMode == domain.DeployCompile && svc.ModHasPakMergeSource(...)` —
  `mod.go:635` (pak conversion, merge-compile games only)
- the "run `lmm profile apply` to converge" hint, emitted only when the lock
  target differs from the installed version (`mod.go:734-736`)

That composition is CLI-only today. Rebuilding it inside `coreProvider` is
precisely the duplication the parity directive forbids, so extract it:

```go
// internal/core/moddetail.go
type ModDetail struct {
    Mod       *domain.Mod  // source-side metadata (network)
    Installed *InstalledDetail // nil when not installed in this profile
}

type InstalledDetail struct {
    Version       string
    Profile       string
    UpdatePolicy  domain.UpdatePolicy
    Locked        bool
    LockedVersion string
    ConvertPaks   *bool // nil unless merge-compile game with a pak merge source
}

func (s *Service) ModDetail(ctx context.Context, game *domain.Game,
    profile, sourceID, modID string) (*ModDetail, error)
```

`doModShow` becomes a renderer over it (colour, ordering, the 2000-char cap,
the converge hint's wording); `coreProvider.GetModDetails` maps it into the TUI
view model. Same shape as #224's verify-engine extraction — engine in core,
both surfaces render.

**Drift guard:** capture `mod show`'s current human and `--json` output
_first_, pin with tests, then extract. This is #224's byte-identity lesson at
1/20th the scale — one ~70-line function, not 1513. The HTML-cleaning change
of decision 2 lands **after** the capture tests are green, as its own commit,
so the two changes are never entangled.

### 4. TUI view model and provider

A new struct, **not** a widened `ModItem` — `ModItem` is constructed in three
places (`service_core.go:157` Overview, `service_core.go:407` modsToItems,
`service.go:735` prototype) and two of them cannot supply the new fields
without a network call per row.

```go
// internal/tui/service.go
type ModDetails struct {
    // Source-side; populated from the ModItem at open, enriched by the fetch.
    ID, Name, Version, Author string
    Summary, Description      string
    Category                  string
    SourceURL, PictureURL     string
    Endorsements              int64
    HasEndorsements           bool

    // Installed block — local, present from the first frame. Nil when the mod
    // is not installed in the active profile (parity with mod show).
    Installed *InstalledDetails

    // Fetch state. Set by the model's handler and resolvers (never by the
    // provider); read by Lines() to pick the render state below.
    Fetching bool
    FetchErr string
}

// InstalledDetails is the TUI-side mirror of core.InstalledDetail, with the
// policy already rendered to a display string (the TUI has no reason to carry
// domain.UpdatePolicy). Deliberately a separate type rather than reusing the
// core struct: the view model is a rendering contract, and every other TUI
// row type follows the same rule.
type InstalledDetails struct {
    Version       string
    Profile       string
    UpdatePolicy  string
    Locked        bool
    LockedVersion string
    ConvertPaks   *bool // nil = not applicable, not "off"
}
```

New `DataProvider` method (`service.go:288` — read-only, so it belongs on
`DataProvider`, not `ActionProvider`):

```go
GetModDetails(ctx context.Context, item ModItem) (ModDetails, error)
```

`coreProvider.GetModDetails` is modelled on `AvailableVersions`
(`service_core.go:1674-1685`), which is already `svc.GetMod` keyed on
`(item.Source, currentGame().ID, item.ID)` — the new method is that call plus
the `ModDetail` join, with errors through a details-flavoured
`mapNetworkError` (`service_core.go:1207-1216`): capability `"mod details"`,
fallback `"run 'lmm mod show <id>' in a shell"`.

`prototypeProvider.GetModDetails` returns canned data; `prototype.Mod`
(`internal/tui/prototype/data.go:79`) gains `Description`, `SourceURL`,
`PictureURL`. The compiler enforces both implementations.

**No caching.** `core.Service.GetMod` (`internal/core/service.go:422-439`) is a
pass-through with no cache at any layer, so each open is one round trip.
Memoizing is deliberately out of scope (YAGNI): the fetch is user-initiated,
one-per-open, and cancelled on the next action.

---

## Interaction

**Binding: `enter`.** No new key. `Select` is already documented as
context-dependent (`app.go:1079-1087`: dashboard opens a menu entry, Profiles
switches profile), and `openSelectedMenuEntry` early-returns unless
`ScreenDashboard` (`app.go:389-392`) — so `enter` is currently a **no-op** on
both Installed Mods and Search. Extend the existing switch with those two
screens.

Layout (80 cols, enriched + installed):

```
┌─ Skyrim Script Extender ──────────────────────────────── fetching… ─┐
│ ID: 12345   Version: 2.2.6   Author: SKSE Team                      │
│ Category: Utilities                                                 │
│ Endorsements: 84213                                                 │
│ URL: https://www.nexusmods.com/skyrimspecialedition/mods/30379      │
│ Image: https://staticdelivery.nexusmods.com/mods/1704/images/...    │
│                                                                     │
│ Summary:                                                            │
│ A modder's resource that expands scripting capabilities.            │
│                                                                     │
│ Description:                                                        │
│ <cleaned, wrapped, scrollable>                                ↓ 24  │
│                                                                     │
│ Installed: v2.2.3 (profile: default)                                │
│   Update policy: notify                                             │
│   Lock: locked at v2.2.3 — run 'lmm profile apply' to converge      │
│   Pak conversion: on                                                │
└─────────────────────────────────────────────────────────────────────┘
```

Field order mirrors `mod show` exactly. Optional rows (Category,
Endorsements, URL, Image, Summary, Description) are omitted when empty, as in
the CLI. `Pak conversion` appears only for merge-compile games with a pak merge
source.

**Render states:**

| State           | Header      | Description slot                 |
| --------------- | ----------- | -------------------------------- |
| Fetch in flight | `fetching…` | `(loading…)`                     |
| Enriched        | —           | cleaned text, scrollable         |
| Fetch failed    | —           | `(unavailable — <reason>)`       |
| Not installed   | —           | Installed block omitted entirely |

Name / ID / Version / Author / Endorsements come from the row instantly. On the
Search path `Summary` is also already local (`service_core.go:415`); on the
Installed path it is not, and arrives with the fetch.

**Scrolling** uses `infoOverlay`'s offset model (`overlay.go:21-29`), not
`windowedRows` — there is no selection in a document body. The view **must**
consume `↑/↓`/`j`/`k` so the list selection behind it cannot drift; the swallow
rule above is the backstop, not the primary defence.

**Async idiom** — `editSelectedModLock` (`mutations.go:628-658`) verbatim:

1. Guards: screen ∈ {Installed Mods, Search}, `m.actions != nil`,
   `!m.action.running && m.action.pending == nil`, a valid `m.selectedMod()`.
2. Cancel any prior ctx, derive `context.WithCancel(m.ctx)`, `m.action.gen++`,
   capture `gen`, set `running` + status line.
3. Return a plain `tea.Cmd` (no goroutine) emitting one of two gen-tagged
   messages.
4. Dispatch in `Model.Update`'s switch with `if msg.gen != m.action.gen {
return m, nil }` (stale-gen drop).
5. Both resolvers clear `running`, call and nil `m.action.cancel`, and check
   `m.action.draining` → `resolveDrainedQuit()`. The draining check is a
   Copilot-PR-#63 finding repeated in every resolver; do not omit it.

**No progress channel.** `mutations.go:1549-1552` is explicit that a plain
fetch needs no `ch`/`waitForActionProgress`/`tea.Batch` plumbing — that
machinery is for mutating flows.

The view is pushed **immediately** (step 1, before the fetch returns), and the
resolver mutates the already-pushed `ModDetails`. Pushing on success instead
would defeat local-first entirely.

---

## Error handling

A failed fetch never closes or blocks the view. Local data stays, the
Description slot renders `(unavailable — <reason>)`, and the mapped error goes
to the status line. `mapNetworkError` already special-cases
`source.ErrNotSupported` and `domain.ErrAuthRequired`, so a directory source
that cannot serve details degrades to the local view with an explanatory line
rather than erroring.

`ModDetail`'s installed lookup follows `doModShow`'s convention: a
`GetInstalledMod` failure means "not installed" and omits the block; a
`resolveProfile` failure is a real error. A `config.LoadProfile` failure
degrades to `Locked: false` (matching `mod.go:639`, which ignores `perr`).

---

## Testing

**Host generalization** — extend #224's existing tests in
`internal/tui/health_screen_test.go:548-658`, which currently assert push jumps
to `ScreenHealth`; they become "screen is unchanged". New cases:

- push from a non-Health screen renders there, `esc` returns to it
- `gotoScreen` from any host screen clears pushed content (extends
  `TestGotoScreenAwayFromHealthClearsPushedContext:614`)
- a declined mutation key (`e`, `x`) is swallowed — the row underneath is
  untouched, and `m.selected` does not move on `↑/↓`
- the five existing Health guard tests (`:856`, `:883`, `:1397`,
  `health_conflicts_test.go:304`) stay green unmodified

**Provider** — `GetModDetails` on both implementations; a call counter on the
shared recording fake (`actions_provider_test.go:761+`); new `prototype.Mod`
fields.

**Async** — following `lock_picker_test.go`'s coverage shape: wrong-screen
no-op, nil-provider no-op, single-flight refusal, stale-gen drop, quit-drain
cancels the ctx, success enriches the pushed view, failure keeps local data and
sets the status line.

**Layout** — the overflow assertion shape at `health_screen_test.go:503-512`
(`lipgloss.Width(view) <= availableWidth()`), at 80x24 and a narrow terminal.

**CLI** — capture tests on `mod show` human + `--json` output recorded
**before** the core extraction, then HTML-cleaning assertions layered on top.
`snapshot_test.go:44-51`'s `slugs` map needs no change (no new screen).

---

## Success criteria

1. `enter` on a selected mod in Installed Mods or Search opens a full-screen
   details view **without changing the highlighted nav entry**; `esc` returns.
2. The view renders local data on the first frame and enriches in place; an
   offline user still sees name/version/author/installed state.
3. Every field `lmm mod show` prints is present, in the same order, with the
   same omit-when-empty rules.
4. `mod show` and the TUI render `Description` identically cleaned. Apart from
   that one intended change, `mod show`'s human and `--json` output is
   byte-identical to the captures recorded before the extraction.
5. A declined key cannot mutate or move the list underneath the view.
6. `go test ./...` green; the 24 frozen verify goldens untouched.

---

## File map

| File                             | Change                                                                                                                                                                           |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/core/moddetail.go`     | **new** — `ModDetail`, `InstalledDetail`, `Service.ModDetail`                                                                                                                    |
| `internal/core/changelog.go`     | doc comment: the cleaner is not changelog-specific                                                                                                                               |
| `cmd/lmm/mod.go`                 | `doModShow` becomes a renderer over `Service.ModDetail`; `CleanChangelog` applied to `Description`                                                                               |
| `internal/tui/contextview.go`    | drop the `ScreenHealth` jump and `contextReturn`; absorb the chrome renderer as `contextView()`                                                                                  |
| `internal/tui/moddetails.go`     | **new** — the `contextContent` implementation (`Title`/`Lines`/`HandleKey`/`HelpGroup`), scroll offset, render states                                                            |
| `internal/tui/app.go`            | `screenView` early check; ungate key routing (`:966`) and nav-away (`:429`); swallow rule; `Select` extended to two screens; `healthScreenView` → `healthHomeView`; doc comments |
| `internal/tui/service.go`        | `ModDetails`/`InstalledDetails`; `GetModDetails` on `DataProvider`; prototype impl                                                                                               |
| `internal/tui/service_core.go`   | `coreProvider.GetModDetails`                                                                                                                                                     |
| `internal/tui/mutations.go`      | handler + two gen-tagged msgs + two resolvers                                                                                                                                    |
| `internal/tui/prototype/data.go` | `Description`/`SourceURL`/`PictureURL` on `prototype.Mod`                                                                                                                        |
| `internal/tui/navigation.go`     | doc comment (host is no longer Health-specific)                                                                                                                                  |
| `CHANGELOG.md`                   | Added: TUI details view. Changed: `mod show` cleans HTML.                                                                                                                        |

## Task ordering

The sequence is load-bearing — the CLI capture tests must exist before
anything moves, and the two CLI changes must not entangle:

1. Capture tests pinning `mod show` human + `--json` output (no production
   change).
2. Extract `Service.ModDetail`; `doModShow` becomes its renderer. Captures
   stay green **unchanged** — this step is behaviour-preserving.
3. Apply `CleanChangelog` to `Description` in the CLI. Captures updated in
   this commit alone, so the diff shows exactly the intended change.
4. Host generalization + swallow rule, with #224's tests updated.
5. TUI view model, provider methods (both implementations), async handler.
6. The `moddetails.go` content view and its `enter` binding.

---

## Out of scope

- **#87 (mod-level changelog)** — separately scoped. This view is its landing
  slot; the Description block's renderer is where it will sit.
- **CurseForge `Screenshots []ModAsset`** (`curseforge/types.go:43`, dropped at
  `curseforge.go:294-327`) — a terminal cannot render images, so the single
  `PictureURL` carries the whole value.
- **Details caching / memoization** — YAGNI; revisit only if re-opening the
  same mod proves annoying in real use.
- **Opening URLs in a browser** — no precedent in this TUI; would need a
  platform-shell decision of its own.
