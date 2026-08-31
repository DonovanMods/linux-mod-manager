# `lmm serve` (v2.1.0) — Design

**Date:** 2026-08-30
**Status:** Approved design (owner, 2026-08-30), pre-implementation — the implementation plan is written from this document
**Issues:** EPIC [#276](https://github.com/DonovanMods/linux-mod-manager/issues/276); carried TUI intents #74 #225 #226 #232 #257 #87; deferred: #307 (non-interactive game add/auth), #317 (cross-process mutation lock)
**Builds on:** [2026-08-27-v2-core-refactor-design.md](2026-08-27-v2-core-refactor-design.md) (the v2 core contract this UI consumes) and the Phase 3 phase-end review's serve-readiness inventory

## Summary

`lmm serve` starts a local HTTP server over the long-lived `core.Service` and opens a
browser tab: server-rendered HTML pages for the day-to-day mod workflow the removed TUI
covered, a small `/api/v1` JSON layer whose responses are exactly the frozen v2.0.0
core types, and SSE streams of the typed core events for live progress. Ships as
**v2.1.0 — the first published, announced v2 release** (the v2.0.0 tag stays a quiet
contract freeze; its draft release is never published).

## Settled constraints (not re-litigated)

- Local HTTP/JSON + SSE over `internal/core`; web UI in a browser tab; single static
  no-CGO binary with embedded assets (2026-08-27 owner decision — chosen over
  Wails/Electron/Tauri/Fyne).
- The job registry lives in `serve`, not in core (EPIC #276).
- The v2.0.0 JSON contract is frozen: serve documents ARE the goldened core types;
  wire changes are additive (`omitzero`/new fields per the existing conventions).
- WEBUI.md governs the frontend: server-rendered HTML, JavaScript as progressive
  enhancement only, TailwindCSS, mobile-first, semantic/accessible markup, no JS
  framework, no client-side state management.

## Scope (owner-approved 2026-08-30): "TUI parity plus"

The v2.1.0 UI covers the daily workflow: status dashboard; installed-mod list with
enable/disable/uninstall (with `keep-cache`/deploy options, #226); mod details with
full prose (#232) and changelog (#87); search and install with file/version selection
(#225); update check with per-item selection (#74); profile switch/apply; deploy and
verify/health with live progress (#257).

**Stays CLI-only in v2.1.0** (the UI says so explicitly where relevant, rather than
hiding it): game add/detect, `auth login`, custom-source management, archive
import/adopt, hooks editing, `profile export/import`, settings mutation. These are
later v2.x iterations; game add and auth also depend on #307.

## Architecture

```text
cmd/lmm/serve.go        the `lmm serve` command: flags, app.Open, browser open
internal/serve/
  server.go             http.Server, mux, middleware (host/origin check, CSRF, logging)
  pages_*.go            page handlers (status, mods, search, updates, profiles, health, jobs)
  api.go                /api/v1 JSON handlers (writeJSON over core types, error envelope)
  plans.go              server-side plan store (in-memory, TTL, single-use)
  jobs.go               job registry: goroutine per Apply, EventSink ring buffer
  sse.go                SSE framing + fanout + heartbeats
  templates/*.gohtml    go:embed html/template pages (one base layout + per-page)
  static/app.css        committed Tailwind build output (go:embed)
  static/app.js         one small vanilla-JS file: SSE progress + confirm enhancement
```

- One `app.Open(ctx, opts)` at startup; the `*core.Service` lives for the process.
- Boundary rules extend, not relax: `internal/serve` imports only
  `app`/`core`/`domain`; `cmd/lmm`'s allowed set gains `internal/serve`. The
  boundary, doc-comment, and `Details()` ratchets all cover `internal/serve`.
- `lmm serve` flags: `--addr` (default `127.0.0.1:7420`), `--no-open`. On start it
  prints the URL and opens the browser (xdg-open, best-effort).

## Rendering model

Server-rendered `html/template` pages, embedded with `go:embed`. CSS is Tailwind
built at **dev time** with the standalone `tailwindcss` CLI (`make css`; the binary is
a documented dev tool, never a build or runtime dependency) and the output committed at
`internal/serve/static/app.css`. JavaScript is one small hand-written file used only to
enhance: subscribe to a job's SSE stream and render progress in place, and upgrade
confirm forms. **Every page and every mutation works with JavaScript disabled** — plain
links, plain form POSTs, full-page renders. Mobile-first layout; semantic elements;
native controls.

## HTTP surface

### Pages (GET unless noted)

| Route | Renders | Core calls |
|---|---|---|
| `/` | status dashboard | `Status`, `GameStatus` |
| `/mods` | installed list + enable/disable/uninstall forms | `ListMods` |
| `/mods/{source}/{id}` | details: prose, files, versions, changelog | `ModDetail`, `ModFiles`, `AvailableModVersions` |
| `/search` | search form + results + install forms | `Search` |
| `/updates` | update table with per-item checkboxes | `CheckUpdates` |
| `/profiles` | profile list + switch/apply forms | `ListProfiles` |
| `/health` | verify findings + conflicts | `Verify`, `Conflicts` |
| `/jobs/{id}` | live job page (progress; result when done) | job registry |

Mutations are POSTs from those pages and follow the flow in "Mutations" below.

### `/api/v1` (JSON; used by the enhancement JS and available to scripts)

- `GET /api/v1/{status,mods,updates,profiles,health}`, `GET /api/v1/mods/{source}/{id}`,
  `GET /api/v1/search?q=` — the same core documents the CLI's `--json` emits, via a
  `writeJSON(w io.Writer, v any)` split out of `cmd/lmm`'s `emitJSON`.
- `POST /api/v1/plans/{kind}` — computes a Plan, stores it server-side, returns the
  plan document plus a `plan_id`. (Plans carry unexported `json:"-"` freshness
  snapshots, so the stored server-side object — not the wire copy — is what Apply
  receives. The store is in-memory, single-use, ~10-minute TTL.)
- `POST /api/v1/jobs` with `{plan_id, options}` — starts the Apply as a job, returns
  `{job_id}`. `GET /api/v1/jobs/{id}` — job status document.
  `GET /api/v1/jobs/{id}/events` — SSE.
- Failures use the CLI's `{"error","details"}` envelope, same typed details
  (`details.conflicts`, `details.warnings`, …).

## Jobs and SSE

- Registry in `internal/serve`: each job runs one Apply in a goroutine with an
  `EventSink` writing to a bounded ring buffer (~1024 events) with SSE fanout;
  subscribers joining late replay the buffer first. States: `running`, `succeeded`,
  `failed` (with the envelope). The registry keeps the last ~50 jobs, in memory only —
  a restart forgets history (the DB remains the truth about state).
- SSE frames: one JSON object per typed core event (they are already json-goldened),
  `event:` set to the event type name; comment heartbeats every ~15s.
- `beginOp` already serializes mutations in-process, so concurrent Apply jobs queue.

## Mutations: Plan → confirm → Apply

Every mutation renders its Plan on a confirm page first (no-JS: the POST returns the
confirm page; JS: same flow, enhanced). Confirm POSTs the stored `plan_id` → job →
redirect to `/jobs/{id}` (no-JS fallback: run synchronously and render the result).

Conflict handling mirrors the CLI contract exactly: where the plan itself carries
conflict/artifact information (uninstall, purge, import-archive) it is shown before
confirm; where conflicts surface at Apply (install — the files are known only after
the cache fill), a failed job carrying `*core.ConflictError` renders the conflict list
with an "overwrite" action that re-plans and re-applies with `AcceptConflicts` (the
re-run is download-free per the Phase 3 Ruling 7 carve-outs). `ErrStalePlan` renders
a "things changed underneath this plan" page with a fresh plan to re-confirm. The
Ruling-10 convenience wrappers (`DeployProfile`, `UninstallMod`, `PurgeProfile` — no
freshness check) are never serve entry points.

## Security

- Default bind `127.0.0.1:7420`; `--addr` can change it, and a non-loopback bind
  prints a loud warning (no authentication exists in v2.1.0 — local single-user).
- `Host` and `Origin` validation against DNS rebinding; CSRF token on every form and
  state-changing API call; no cookies beyond the CSRF session; assets served with
  conservative headers. A real auth token for LAN use is a later, separate feature.

## Cross-process concurrency (documented caveat)

In-process mutations serialize via `beginOp`. A CLI mutation racing a serve mutation
in another process is guarded only by SQLite's locking; the deploy-tree file
operations could interleave. v2.1.0 documents this ("avoid running CLI mutations
while a serve operation is in flight"); the advisory file lock in `app` is #317.

## Small core/cmd additions this epic makes (each a plan task)

1. `writeJSON(w io.Writer, v any)` extracted; `emitJSON` becomes its stdout wrapper.
2. The two CLI-side `Details()` envelope types (`profileWarningsError`,
   `gameDetectPartialError`) lift into `core` so both frontends share one wire shape
   (the `Details()` coverage ratchet follows them).
3. `ModDetail` gains an optional changelog (#87): a `Changelog` field populated where
   the source can supply it (NexusMods), omitted otherwise — additive, golden re-recorded
   once as an addition.
4. Everything else serve needs already exists post-#309/#314 (queries, Plan/Apply
   pairs, events, `ImportArchivePlan` for later import UI).

## Scope mapping (carried TUI intents)

| Intent | Mechanism |
|---|---|
| #257 live deploy progress | job SSE stream on every Apply |
| #74 per-item update selection | `/updates` checkboxes → one batch plan → confirm |
| #225 install with file/version pick | details/search install form: `AvailableModVersions` + install options |
| #226 uninstall/deploy options | confirm-page form options mapping to the existing option structs |
| #232 full prose in details | details page renders complete text |
| #87 changelog in details | core addition 3 above + details section |

## Testing

- `httptest` against a `Service` seeded with the existing fake-source fixtures;
  sandboxed HOME/XDG as everywhere else.
- API handlers asserted against the existing core goldens (the contract IS the test);
  the envelope paths against the envelope goldens.
- SSE: framing + replay + heartbeat tests with a fake clock where needed.
- Pages asserted semantically (status codes, form targets, key content, CSRF token
  presence) — never byte-golden HTML.
- Ratchets (boundary, doc-comment, `Details()` coverage) extended to `internal/serve`.
- A cancellation test per job path: killing a job's request does not kill the Apply
  (jobs own their context; Ruling 16 semantics hold).

## Release (v2.1.0)

MINOR bump; CHANGELOG section; README gains the serve section; `make man` for the new
command; flip `.goreleaser.yaml` back to `draft: false` in the release prep so
v2.1.0 publishes directly as the announced v2 debut, with release notes that carry the
v2 migration story (users arrive from v1.30.x). `develop`/`main` promotion remains a
separate decision.

## Out of scope for v2.1.0

Admin operations (game add/detect, auth login, custom sources, archive import/adopt,
hooks, profile export/import, settings mutation); remote access and authentication;
the cross-process advisory lock (#317); #307.
