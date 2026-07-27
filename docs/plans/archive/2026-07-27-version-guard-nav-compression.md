# Version Guard + Nav Compression Implementation Plan (#93 interim, #108 — EPIC #104)

**Goal:** Two small fixes: the dead `lmm install --version` flag rejects honestly instead of silently installing latest (#93 interim guard), and the nav bar compresses at narrow widths instead of truncating its last entry to an ellipsis (#108).

**Architecture:** No new subsystems. #93 is a guard clause in install's RunE. #108 is tiered nav rendering selected by measured width: full labels → current-label-only → numbers-only, with the existing hard truncate kept as a safety net.

**Tech Stack:** Go, Bubble Tea/lipgloss, testify require.

## Global Constraints

- Branch: `fix/version-guard-nav-compression` off `main`; one PR. **Closes #108; does NOT close #93** (interim only — the real `--version` implementation belongs to EPIC #98 via #96/#97; the PR comments on #93 and #98 instead).
- TDD RED-first for both behavior changes; gofmt tabs, vet clean, full `go test ./...` per commit; add files BY NAME (IDEAS.md stays untracked); dense why-comments.
- Version: PATCH → 1.18.2.
- Goldens: 80x24 captures' nav line WILL change (compression replaces truncation); 120x36 must NOT change (full nav fits at 116 cols). Regenerate in finalize with exactly that audit.
- The `•N•` same-width current-marker convention (PR #107) carries into every tier — marking current must never change a tier's width.

---

### Task 1: #93 — `--version` guard

**Files:** `cmd/lmm/install.go` (+`install_test.go`).

Facts: `installVersion` is registered (`install.go:297`) and never read anywhere (verified by exploration and by #98's own body — "declared, never read... currently lies to users"). The #98-decided interim fix is option 2: reject clearly.

Steps:
1. RED test: invoking install with `--version 1.2.3` (any target) returns an error containing "not yet supported" and mentioning the flag, BEFORE any plan/network work happens (use the repo's existing install-test fixture patterns — no real source calls; asserting the error shape from the RunE/doInstall entry is enough). A no-flag invocation keeps its existing behavior (pick any existing green install test as the guard).
2. Fix: at the top of install's RunE (or doInstall, wherever flag state first flows), `if installVersion != "" { return fmt.Errorf("--version is not yet supported: version-specific installs need the version→file resolver tracked by #96/#97 (EPIC #98); omit --version to install the latest") }`. Doc comment on the guard: interim honesty fix per #93/#104 — the flag previously parsed and silently ignored; remove the guard when #96 wires real resolution.
3. Full suite + vet. Commit: `fix(cli): reject the unimplemented install --version flag instead of ignoring it (#93)`.

### Task 2: #108 — tiered nav compression

**Files:** `internal/tui/app.go` (`nav()`), tests in `app_test.go`; goldens NOT regenerated here (finalize).

Facts: `nav()` (app.go:993-1016) renders `[N] Label` / `•N• Label` for all six screens, joined by two spaces (~87 cells); `View()` hard-truncates to `availableWidth()` (floor 76 at 80 cols), so the last entry currently degrades to `…`/`•…` (documented in #108 with the arithmetic; `TestNavMarkerAddsNoWidth` pins the marker's zero-width property).

Design (decided): measure with `lipgloss.Width` and pick the first tier that fits `availableWidth()`:
- **Tier 1 (full)**: current rendering, unchanged.
- **Tier 2 (current-label-only)**: non-current screens render just their number cell `[N]`; the current screen keeps `•N• Label`. (~43 cells worst case — fits 80 comfortably.)
- **Tier 3 (numbers-only)**: all screens render just number cells (`[N]`/`•N•`, ~29 cells) — for pathological widths; panel titles still identify the current screen.
Keep `View()`'s hard truncate as the final safety net. Marker stays inside the number cell in every tier (same-width invariant preserved per tier).

Steps:
1. RED tests:
   a. At 80x24, for EVERY screen selected (loop 1-6): `lipgloss.Width(nav-line-as-rendered)` ≤ `availableWidth()` AND the line contains the current screen's `•N•` marker AND (for tier 2) the current screen's label text — the last-screen case (Conflicts) is the one that fails today (currently truncated away).
   b. At 120x36: nav renders tier 1 (all six full labels present) — pins no regression at wide sizes.
   c. At 40x12: nav fits and every number cell present (numbers-only tier), current marked `•N•`.
   d. Existing `TestNavMarksCurrentScreenWithoutColor` and `TestNavMarkerAddsNoWidth` stay green (marker semantics unchanged); adapt only if their exact-string assumptions collide with tier selection at their test widths — justify any change in the report.
2. Implement the tier selection in `nav()` with a doc comment: why measured tiers (the 6-screen full nav outgrew 80 cols — #108's arithmetic), why the safety-net truncate stays, why the marker never changes a tier's width.
3. Full suite + vet. Commit: `fix(tui): nav compresses at narrow widths instead of truncating the last screen (#108)`.

### Task 3: Finalize

1. Regenerate goldens; audit: ONLY 80x24 files change, and only their nav line (tier 2 replaces the truncated full nav); 120x36 byte-identical. Anything else → stop and investigate. Commit with that justification.
2. CHANGELOG `[1.18.2]`: Fixed — `lmm install --version` now fails with a clear "not yet supported" error instead of silently installing the latest version; the TUI nav bar compresses at narrow widths (number cells, current screen keeps its label) instead of truncating the last screen entry into an ellipsis at 80 columns. Bump root.go → 1.18.2 (separate `chore:` commit). Archive plan doc in-PR.
3. PR: closes #108, references #93 WITHOUT closing (body explains interim-vs-real split); after merge, comment on #93 (guard shipped, closure deferred to EPIC #98's resolver work) and #98 (foundational item #93's interim fix shipped; silent-lie removed).
4. Smoke gate: TUI nav changed → user smoke test before merge.

## Execution notes

Tasks 1 and 2 are independent but share the branch — sequential per the skill. Models: both mid-tier; reviews mid-tier; final whole-branch review most-capable (small diff — quick).
