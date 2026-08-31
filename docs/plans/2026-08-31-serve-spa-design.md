# `lmm serve` SPA — UX/UI Design

**Date:** 2026-08-31
**Status:** Approved direction, pre-implementation (issue [#326](https://github.com/DonovanMods/linux-mod-manager/issues/326); implementation plan follows owner sign-off)
**Supersedes:** the page-per-command web UI shipped by EPIC #276's units 2–6 (the server-rendered *page layer* only — the `/api/v1` + SSE + jobs backend is this design's foundation and stays)
**Owner decisions incorporated:** 2026-08-31 brainstorming session (mockups preserved under `.superpowers/brainstorm/`)

## Why (one paragraph)

The first web UI converted the TUI too literally: six pages mapped to CLI verbs, context
re-established per page, actions far from the data they act on (50-finding UX audit, 14 blockers,
in the #326 thread). This design starts fresh as a **single-page application**.

## Settled decisions (owner, 2026-08-31 — do not re-litigate)

- **WEBUI.md is relaxed for this project.** It targets public-facing web apps; `lmm serve` is a
  single-user localhost tool. Remaining hard constraints: no major security risks, no poor
  performance, no external dependencies a user could struggle to install (users install nothing —
  everything embeds in the binary). Modern browsers assumed; **the CLI is the fallback**, so the
  no-JS requirement is dropped. **Desktop only, ≥ 1080p** — no small-screen design (gaming tool).
- **SPA**: most elements on one page, modals for popups, separate routed pages only where a
  whole-page design earns it.
- **Stack**: vendored **Preact + htm** — two pinned files in-repo, native ES modules, **no
  npm/Node/bundler**; `go build` remains the entire build; assets via `go:embed`.
- **Home = "Mission Control"**: per-game overview — attention cards that act in place, the library
  beneath, common actions in the frame.
- **Drill-in = slide-over by default**, with *More info →* routing to a full mod page.
- **Search = omnibar** (live-filters the library; source results append in place), with a
  dedicated **search page** as the escape hatch for heavy browsing.
- **Jobs = inline state + activity tray**: the control you clicked morphs into its progress; a
  top-bar activity bell collects running/queued/recent/failed jobs; toasts when appropriate.
- **Style = "Launcher"**: dark-energy palette (charcoal, amber/green accents, confident headers),
  **follow-the-system by default with a user-selectable light/dark override** — so Launcher gets a
  first-class light variant, both defined as CSS custom-property token sets.
- **Design principle (named):** *quick path inline, full path one click away* — every heavy
  surface (full mod page, search page, tray→event stream) is the one-click deepening of an inline
  affordance, never the default.

## Information architecture

URL scheme (History API; the Go server serves the SPA shell for all app routes):

```text
/                                → game chooser (or redirect to the single/default game)
/g/{game}/{profile}              → Mission Control (home)
/g/{game}/{profile}/mod/{source}/{id}   → full mod page
/g/{game}/{profile}/search?q=…          → dedicated search page
```

Context (game + profile) lives **in the path** — the audit's B1 wrong-game bug class becomes
structurally impossible. The slide-over annotates the URL (`…?mod=nexus/123`) so deep links and
Back behave.

### Mission Control (home)

- **Top bar**: game picker ▾ · profile picker ▾ (with *Manage profiles…* opening the profiles
  modal) · undeployed-changes indicator + **Deploy** button · omnibar · activity bell 🔔 · theme
  toggle (system/light/dark) .
- **Attention cards** (render only when non-empty; each acts in place):
  - *Updates (n)* — per-row checkboxes, "Update selected" → one batch job.
  - *Health (n)* — findings with per-finding *Repair* and *Repair all*; last-verify timestamp + re-run.
  - *Conflicts (n)* — each conflict names the contenders and the winning rule, with *Resolve…*
    opening the reorder modal.
- **Library** — the spine. Rich table: enabled toggle, name, version (+update target), badges
  (⬆ update, ⚠ health, ⇄ conflict, 🔒 lock, policy), load-order position, row actions (primary
  action + ⋯ menu). Filter/sort controls (all/enabled/updatable/unhealthy; name/load-order/
  recently-updated). Multi-select rows → a batch bar (enable/disable/uninstall/update selected).
  Row click → slide-over.
- **Empty states**: no games → setup guidance; empty library → inline search prompt.

### Slide-over (default drill-in)

Right-side panel over a still-visible home: name, author, installed → available version, lock +
policy controls (editable), summary prose, key actions (update/enable/disable/uninstall), its own
findings/conflicts, *Changelog* preview, **More info →** (routes to the full page). Esc / outside
click closes; ←/→ steps through the current list.

### Full mod page

Everything, unlimited room: full description, complete changelog, files table, versions table
(install/rollback per version honoring lock rules), dependency info, per-mod job history. Back
returns to home exactly as left.

### Search

- **Omnibar**: typing live-filters the library ("In your library (n)"); Enter fans out to the
  game's sources and appends "From sources (n)" rows in place — install inline (version picker ▾
  where applicable), slide-over on click. Source failures surface as a warning row, never
  swallowed.
- **Search page** (escape hatch, and the model for "pages that earn it"): source badges, star/
  download counts, summaries, category/source filters, sort, pagination.

### Jobs

- The initiating control morphs: button → progress bar with phase text (from the SSE
  `DeployPhase`/event vocabulary, humanized); cards show live counts.
- **Activity tray** (bell dropdown): running jobs with progress, queued, recent results, failures
  with their next step inline (a conflict shows *Overwrite?* right in the tray). Entries expand to
  the phase-by-phase event stream. Backed by the registry's retained jobs (adds `GET /api/v1/jobs`).
- Toasts: job completion/failure when its origin isn't on-screen; never for things in view.

### Modals (inventory)

Confirm-plan (install/uninstall/update batch/deploy/repair — renders the Plan, the SPA sibling of
the CLI's confirm), conflict-overwrite, **reorder** (drag-and-drop load order with live conflict
preview; reachable from Conflicts card and library), **profiles** (list/create/rename/delete/
export/import/set-default), keyboard-shortcuts help. Modals stack at most one deep; everything
else is slide-over or page.

## Visual design

Launcher tokens as CSS custom properties, two full sets (dark + light), applied via
`prefers-color-scheme` with a persisted `data-theme` override (localStorage). Dark: deep charcoal
surfaces (#0b0f14/#111823), amber primary action, green=good/amber=warn/red=conflict semantics,
confident uppercase-tracked headers. Light: same hues re-weighted on paper-white. Density tuned
for ≥1080p desktop: comfortable rows, three-across cards, slide-over ≈ 40% width. System font
stack; `ui-monospace` for versions/paths/phases.

## Architecture

```text
internal/serve/
  spa/                 index.html shell, app/ (ES modules: components, store, router, api, sse, theme)
  vendor/              preact.module.js, htm.module.js   (pinned versions, vendored, doc-commented)
  … existing api.go, jobs.go, sse.go, plans.go, middleware.go stay (the backend)
  templates/, pages_*  DELETED with the old page layer (README/CLAUDE.md updated)
```

- **State**: one small store (plain Preact signals-style implementation or context; no external
  state lib) hydrated from `/api/v1`, updated by actions + the activity SSE stream.
- **Router**: History API; server-side catch-all serves the shell for `/`, `/g/…` routes.
- **CSRF**: token delivered in the shell (meta tag), sent as the existing header on every mutation.
- **API additions** (all additive): `GET /api/v1/jobs` (tray index); a global **activity SSE
  stream** (`GET /api/v1/events`: job lifecycle events multiplexed; per-job streams stay for
  detail); endpoints for the audit's five CLI-only actions via small core wrappers — set update
  policy, lock/unlock, **reorder** (`ResolveReorder` exists; needs the apply), profile CRUD +
  set-default; plus search pagination params. The wire stays "core documents only" (Phase 3 rule).
- **Old URLs**: the six old page routes 301 to their SPA equivalents.

## Scope

**In (feature-complete bar — owner decision at doc review, 2026-08-31): FULL BIDIRECTIONAL
PARITY.** The web UI can do everything the CLI can — including **all admin tools**: game
add/detect, `auth login`, custom-source management, archive import/adopt (the primary user IS the
admin; this is a single-user tool). That pulls in #307 (non-interactive game add + credential
store in core) and a real **Setup surface**: first-run flow in the game chooser, game add/detect
UI, source auth UI, custom-source editor, archive import/adopt UI (over `ImportArchivePlan`).
And vice-versa: any capability the web UI adds must be CLI-reachable — concretely, per-item
update batches land in core (#324) with the CLI's `-i` selection picker (#254) closing that gap.
**Out:** remote access/auth, mobile layouts, no-JS operation, i18n.

## Testing

- Backend: existing Go httptest suites (API/jobs/SSE) extend to the new endpoints/wrappers.
- SPA: `chromedp`-driven E2E smoke in Go (test-only dependency; skips when no Chrome/Chromium on
  PATH, like the 7z pattern) covering: home render, drill-in, install w/ conflict round-trip,
  batch update, deploy progress, reorder, theme override. No Node test runner (no Node anywhere).
- The security middleware tests (Host/Origin/CSRF) continue unchanged — same origin story.

## Process

Long-lived feature branch **`webui`** off `v2`; story branches off `webui`, `--no-ff` merges back;
issues close at the `webui` merge; `webui` merges to `v2` only when the owner declares the UI
feature-complete and approves UI+UX. **No releases until further notice** (owner directive). The
old page layer's deletion is the feature branch's first commit, so every subsequent screen is
judged as the SPA, never against the old look.
