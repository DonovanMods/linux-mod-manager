# #97 Version Lock/Unlock Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give users a first-class version lock — `lmm mod lock/unlock` + a TUI lock action with a version picker — enforced as an update-apply refusal at the core level, visible in every listing, and traveling with profile export/import. Completes EPIC #98's Phase 2 (with #96 already shipped in v1.25.0).

**Architecture:** One PR (branch `feat/97-lock-surface`, → **v1.26.0**). The lock is a `locked: true` marker on the profile ref (YAML-only, per the design note's addendum — `Version` stays the always-populated record and the lock's target). Enforcement is a single core gate: `ApplyUpdate` loads the profile ref up front and refuses locked mods with a sentinel error, so CLI and TUI inherit parity for free. Deploy-side convergence needs **zero new code** — #96's machinery already converges to `ref.Version`. Displays extend the shipped #92 surfaces (CLI `POLICY` column/JSON, TUI flags column, update output) plus the one #92 remnant: `mod show` gains an Installed section.

**Tech Stack:** Go, cobra, bubbletea (Elm), existing test suites: `cmd/lmm/pin_visibility_test.go`, `internal/tui/pin_visibility_test.go`, `internal/tui/policy_test.go` (the templates for every new surface's tests).

## Design decisions locked for this PR

From `docs/plans/2026-07-29-lock-vs-pinned-design.md` (+ addendum) and this planning round:

1. **Lock = `Locked bool` on the profile ref.** `yaml:"locked,omitempty"`. `Version` is the target and the record; `unlock` clears ONLY the marker. Export/import carries it automatically (`ExportedProfile.Mods` is `[]domain.ModReference`).
2. **Locking is a metadata write, not a deploy.** `lmm mod lock <id> 1.2.3` validates the version resolves upstream, writes `Version`+`Locked` to the ref, and tells the user convergence happens on the next `profile apply`/`deploy` when the installed version differs (#96's convergence does the physical work). No auto-deploy — the message names the command.
3. **Lock requires the `Versions` capability** (static check) — version-less sources get: `source %q cannot resolve versions; to freeze this mod use 'lmm mod set-update <id> --pin'`. Locking with no version argument locks at the ref's current recorded version.
4. **Update refusal is core-level.** `ApplyUpdate` gains an up-front locked-ref gate returning `ErrModLocked` — CLI loops and the TUI's `applyUpdatesSequentially` surface it without TUI changes. CLI adds nicer pre-check UX in `applySingleUpdate` (JSON `status:"skipped", reason:"locked"`).
5. **Locked+notify mods ARE checked** ("locked but informed"): `CheckUpdates`/`UpdateCheckable`/`CountUpdateSkips` are untouched. Locked rows render in the update table with a `[locked@<version>]` marker on the POLICY cell.
6. **Locked+auto skip is reported at apply time, not as an `UpdateSkips` category** — DELIBERATE deviation from the design note's letter (note says "new `Locked` category in `UpdateSkips`"): `UpdateSkips` counts *check-time* filtering and its doc contract ("what CheckUpdates actually skipped") would be violated by an apply-time entry; locked mods are checked. The note's intent (skips visibly counted in both interfaces) is met by a distinct locked-skip line in the CLI auto/--all sections, a TUI warning line, and an additive JSON field.
7. **TUI flags column keeps `flagsWidth = 5`; `lck` outranks `pin`** in the 3-char slot ("lock wins and the UI names the lock"). A locked+pinned mod shows `lck`; its pin remains visible in the `P` picker, update output, and `mod show`. The three `pin_visibility_test.go` width/drift tests must stay green.
8. **`verify` gets lock-aware `--fix` gating + an informational drift note.** A locked mod's VERSION MISMATCH is still reported, but `--fix` refuses to rewrite its record (that would silently move the lock's meaning); DB-vs-ref version drift on a locked mod prints a non-issue "lock pending convergence" note.
9. **Docs reframe `pinned` as the check-mute** (`--pin` help currently says "pin to current version" — wrong framing), per the design note.
10. **Wire strings:** TUI keeps `"pin"`, CLI keeps `"pinned"` (documented deliberate split — do not unify). New TUI lock wire values follow the provider contract added here.

## Global Constraints

- TDD strictly: failing test observed first, then implement, then green. NEVER pipe `go test` in a `&&` chain. `git add` by name (untracked `IDEAS.md` stays untracked). gofmt (tabs), `go vet ./...` clean per task.
- CLI/TUI parity: behavior in core; both surfaces in the same PR.
- Existing pinned tests are contracts: `cmd/lmm/pin_visibility_test.go` (6 tests), `internal/tui/pin_visibility_test.go` (3 width/drift tests), `internal/tui/policy_test.go` (16 picker tests), `internal/tui/updated_marker_test.go`. Breaking any requires understanding + documenting why in the task report.
- TUI picker conventions: async fetch uses the three-step `checkForUpdates` pattern (running state → gen-guarded msg → resolve handler builds picker against the live Model); picker `choose` closures never mutate the Model (dispatch a `*ChosenMsg`); the pick IS the confirmation (no second confirm modal).
- `internal/core` tests are package `core_test` (unexported helpers not directly testable); `cmd/lmm` and `internal/tui` tests are in-package.
- Line numbers below are from v1.25.0 HEAD (92626b8) — locate by content if drifted.
- Version bump MINOR → **1.26.0** + CHANGELOG + `make man` in the final task (help text changes). Tag on the MERGE COMMIT after merge. PR closes #97; EPIC #98 then has all sub-issues done.
- Archive this plan doc IN the PR.

## File Structure

| File | Role |
|---|---|
| `internal/domain/mod.go` | `ModReference.Locked` + stale-comment fix (T1) |
| `internal/storage/config/profiles.go` | `ModReferenceConfig.Locked` + both conversion loops (T1) |
| `internal/core/profile.go` | `SetModLock`/`ClearModLock` (T2) |
| `internal/core/flows.go` | `ErrModLocked` + `ApplyUpdate` refusal gate (T2) |
| `internal/core/service.go` | `AvailableModVersions`, `SourceCapabilities` (T2) |
| `cmd/lmm/mod.go` | `lock`/`unlock` commands; `--pin` help reframe; `mod show` Installed section (T3, T5) |
| `cmd/lmm/update.go` | locked branches: single-mod, table marker, auto/--all skips + JSON (T4) |
| `cmd/lmm/list.go` | LOCKED column + JSON fields (T5) |
| `internal/tui/service.go`, `service_core.go`, `actions_provider.go` | `ModItem.Locked/LockedVersion`, provider contract + impls (T6) |
| `internal/tui/app.go`, `keys.go`, `mutations.go` | flags precedence, `L` key, async version picker (T6, T7) |
| `cmd/lmm/verify.go` | lock-aware `--fix` gate + drift note (T8) |
| `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go`, `docs/man/**`, design note | docs/version/man (T9) |

---

### Task 1: The `locked:` marker — domain + YAML plumbing

**Files:** Modify `internal/domain/mod.go:72-78`, `internal/storage/config/profiles.go:40-46, 146-153, 185-192`. Tests: `internal/storage/config/profiles_test.go`, `internal/core/profile_test.go` (or the file holding existing `UpsertMod`/`ReorderMods` tests — locate with grep).

**Interfaces — Produces:**
```go
// on domain.ModReference:
Locked bool `yaml:"locked,omitempty"` // #97 lock marker: lmm update refuses this mod; Version is the lock's target. Set/cleared only by lock/unlock; survives UpsertMod (in-place update) and export/import.
```
Same field on `ModReferenceConfig`, copied in BOTH inline loops (`LoadProfile`, `SaveProfile`). Also fix the stale `Version` comment (`// Empty string means "latest"` → `// The installed-version record (#94/#96): always stamped by installs, moved by updates, converged to by deploy. When Locked, also the lock's target.`).

- [ ] **Step 1: Failing tests.** (a) Round-trip: save a profile whose ref has `Locked: true`, `LoadProfile` returns it true and the YAML file contains `locked: true` (and an unlocked ref's YAML contains NO `locked:` key — omitempty pinned). (b) Export/import: `config.ExportProfile` → `ImportProfile` preserves `Locked` (works by construction — pin it anyway). (c) `UpsertMod` with a fresh ref (Locked zero-value) over an existing locked ref preserves `Locked: true` (the in-place contract — this is the load-bearing test). (d) `ReorderMods` with a reordered slice built from the loaded profile preserves `Locked` (find the TUI reorder call site — `grep -rn "ReorderMods" internal/ cmd/` — and confirm its slice source is the loaded refs; document in the report).
- [ ] **Step 2: Run — observe compile failures** (`unknown field Locked`).
- [ ] **Step 3: Implement** the two struct fields + both conversion loop lines + comment fix.
- [ ] **Step 4: Green**: `go test ./internal/storage/config/ ./internal/core/` bare; `go vet ./...`.
- [ ] **Step 5: Commit** `feat(domain): locked marker on profile refs (#97)` + Claude trailer.

---

### Task 2: Core — lock writes, refusal gate, version listing

**Files:** Modify `internal/core/profile.go` (near `UpsertMod`, :152), `internal/core/flows.go` (`ApplyUpdate`, :3405-3420), `internal/core/service.go` (near `ResolveModVersion`, :380). Tests: `internal/core/flows_update_test.go`, `internal/core/profile_test.go`, `internal/core/resolve_test.go`.

**Interfaces — Produces:**
```go
// profile.go — mirror UpsertMod's load→mutate-in-place→save shape:
// SetModLock marks the ref locked; version != "" also moves the target
// (ref.Version). Returns an error naming the mod when it is not in the profile.
func (pm *ProfileManager) SetModLock(gameID, profileName, sourceID, modID, version string) error
// ClearModLock clears ONLY the marker; Version stays (it is the record).
func (pm *ProfileManager) ClearModLock(gameID, profileName, sourceID, modID string) error

// flows.go:
// ErrModLocked reports an update apply refused because the profile ref is
// locked (#97). Callers branch with errors.Is.
var ErrModLocked = errors.New("mod is locked")

// service.go:
// AvailableModVersions lists the distinct per-file versions mod's source
// reports, first-seen order (the TUI version picker's data). Wraps
// source.ErrNotSupported when the list carries no version info.
func (s *Service) AvailableModVersions(ctx context.Context, sourceID string, mod *domain.Mod) ([]string, error)
// SourceCapabilities reports sourceID's declared capabilities (static lock
// gating). Mirror how the aggregate-search path reaches the registry
// (service.go:271's src access) — if an equivalent accessor already exists,
// reuse it and skip this (document in report).
func (s *Service) SourceCapabilities(sourceID string) (source.Capabilities, error)
```
`ApplyUpdate` gate (insert after the `result`/`emit` preamble, BEFORE `GetMod`/downloads/hooks):
```go
	// #97: a locked ref refuses update-apply entirely - the lock's whole
	// contract. Checked before any network or hook side effect.
	if prof, err := s.NewProfileManager().Get(game.ID, profileName); err == nil {
		for _, ref := range prof.Mods {
			if ref.SourceID == mod.SourceID && ref.ModID == mod.ID && ref.Locked {
				return result, fmt.Errorf("%w: %s is locked at v%s - move the lock with 'lmm mod lock %s <version>' or unlock with 'lmm mod unlock %s'", ErrModLocked, mod.Name, ref.Version, mod.ID, mod.ID)
			}
		}
	}
```
(A missing/unreadable profile falls through — matches `PlanProfileSwitch`'s ignore-errors precedent for profile loads; a lock cannot exist in an unloadable profile.)

- [ ] **Step 1: Failing tests.** (a) `SetModLock` on an existing ref: `Locked` true, version moved when given, untouched when `""`; error when the mod isn't in the profile (pin the message: `mod %s:%s not found in profile %q`). (b) `ClearModLock`: marker false, `Version` unchanged. (c) `ApplyUpdate` on a locked mod: `errors.Is(err, core.ErrModLocked)`, message contains `locked at v` and both commands, and NOTHING was downloaded/deployed (use the update-test scaffolding from `TestApplyUpdate_RecordsEffectiveFileVersion`; assert the mock's download count is zero and the DB row is unchanged). (d) Unlocked mod: update applies as before (existing tests cover; add one explicit control). (e) `AvailableModVersions`: multi-version fixture → `["1.5","1.0"]` first-seen; version-less fixture → `errors.Is(err, source.ErrNotSupported)`.
- [ ] **Step 2: Observe failures.**
- [ ] **Step 3: Implement.** `AvailableModVersions` = `GetModFiles` + the unexported `availableVersions` (same package) + the `anyFileHasVersion` guard producing the wrapped `ErrNotSupported` (reuse `ResolveVersionFiles`' exact wrap format `"source %q: version resolution: %w"`).
- [ ] **Step 4: Green** (`go test ./internal/core/` bare), vet.
- [ ] **Step 5: Commit** `feat(core): lock marker writes, ApplyUpdate refusal gate, version listing (#97)`.

---

### Task 3: CLI `lmm mod lock` / `unlock` + `--pin` reframe

**Files:** Modify `cmd/lmm/mod.go` (commands + init wiring :107-121; `--pin` flag help :113 and `modSetUpdateCmd.Long` :30-46). Tests: `cmd/lmm/mod_test.go` (or a new `mod_lock_test.go`, following `pin_visibility_test.go` conventions).

**Interfaces — Produces:** `lmm mod lock <mod-id> [version]` (`cobra.RangeArgs(1,2)`), `lmm mod unlock <mod-id>` (`cobra.ExactArgs(1)`), both inheriting `-s`/`-p` persistent flags and the `withGameService` → `resolveSource` → `resolveProfile` → `GetInstalledMod` idiom verbatim from `doModSetUpdate` (mod.go:153-198).

`doModLock` contract:
1. Resolve source/profile/mod (existing idiom; `"mod not found: %s"` on lookup failure).
2. Static capability gate: `SourceCapabilities(modSource).Versions` false → error `source %q cannot resolve versions; to freeze this mod use 'lmm mod set-update %s --pin'`.
3. Version argument given → validate via `service.ResolveModVersion(ctx, modSource, &mod.Mod, version)`; surface its errors verbatim (`ErrVersionNotFound` already lists available versions; `ErrNotSupported` covers dynamic version-less).
4. `pm.SetModLock(game.ID, profileName, modSource, modID, version)` (empty version = lock at current record).
5. Output: `✓ %s locked at v%s` (target = version arg, else the ref's version); when the target differs from the installed `mod.Version`, append the convergence hint on its own line: `Installed version is v%s — run 'lmm profile apply' (or 'lmm deploy') to converge.`
6. Policy is NOT touched (decision: policy-neutral).

`doModUnlock`: resolve idiom → `pm.ClearModLock` → `✓ %s unlocked (update policy: %s)` using the CLI's `policyToString`.

`--pin` reframe: flag help → `"mute update checks for this mod (to hold an exact version, use 'lmm mod lock')"`; update `modSetUpdateCmd.Long`'s `--pin` line to match.

- [ ] **Step 1: Failing tests** (in-package, reparent-onto-throwaway-root + flag-global resets, exactly like `TestInstallCmd_VersionFlag_NoLongerRejected`): (a) lock with explicit version writes `Locked+Version` to the profile YAML (assert via `config.LoadProfile`); (b) lock with no version keeps the ref's version, sets marker; (c) unknown version errors listing available; (d) version-less source errors naming `--pin`; (e) unlock clears marker, version intact; (f) lock on a mod not installed → `mod not found`; (g) the convergence hint appears iff target ≠ installed. Use `setupDoInstallTest`-style service fixtures or the file's existing seeded-service helpers.
- [ ] **Step 2: Observe failures.** **Step 3: Implement.** **Step 4: Green** (`go test ./cmd/lmm/` bare) + vet. Note: `make man` will be needed (new commands + changed help) — deferred to T9, but the genman drift test runs in the full suite; if it fails here, regenerate in THIS task and say so in the report.
- [ ] **Step 5: Commit** `feat(cli): lmm mod lock/unlock; --pin help reframed as check-mute (#97)`.

---

### Task 4: CLI update integration — refusal UX, table marker, auto/--all skips

**Files:** Modify `cmd/lmm/update.go` (`applySingleUpdate` :471-520, table loop :366-399, auto loop :436-445, `--all` loop :448-466, `updateSkippedJSON` :41-44, single-mod JSON doc :62-72). Tests: `cmd/lmm/pin_visibility_test.go` siblings (same file or `lock_visibility_test.go`).

**Behavior contract** (per the interaction matrix):
- `applySingleUpdate` loads the profile once up front (`config.LoadProfile` + ref lookup by `sourceID:modID`). When the mod's ref is locked:
  - updates found → print `Update available: %s → %s — but %s is locked at v%s.` + `Move the lock: lmm mod lock %s %s   |   Unlock: lmm mod unlock %s` and DO NOT apply; JSON: `{status:"skipped", reason:"locked", to_version:<new>}` (extend the `Reason` doc comment: `"pinned" | "local" | "locked"`).
  - zero updates + pinned → the existing pinned message gains a ` (also locked)` suffix when locked; zero updates + unlocked-pinned/plain paths unchanged.
- Table loop: locked rows' POLICY cell renders `policyToString(...) + " [locked@" + ref.Version + "]"`; a locked+auto row is NOT appended to `autoUpdates` (count it in a local `lockedAuto int` + collect names).
- After the auto-apply section (and in the no-auto case too, whenever `lockedAuto > 0`): `%d locked mod(s) skipped by auto-update: %s — move the lock or unlock to update.`
- `--all` loop: filter locked from `notifyUpdates` the same way, with the same reporting line covering them.
- The core `ErrModLocked` gate (T2) remains the backstop — CLI tests should exercise BOTH layers (one test bypasses the CLI pre-check by calling `applyUpdate` directly if reachable, else rely on T2's core test).

- [ ] **Step 1: Failing tests**: (a) single-mod update of a locked mod with an available update → skipped output naming both commands, JSON `reason:"locked"`; (b) locked+auto mod appears in the table with `[locked@…]`, is not auto-applied, and the locked-skip line prints with its name; (c) `--all` skips it with the same line; (d) locked+pinned single-mod → pinned message with ` (also locked)`; (e) existing pinned tests in `pin_visibility_test.go` all still green (run them by name).
- [ ] **Step 2-4:** observe, implement, green (`go test ./cmd/lmm/` bare), vet.
- [ ] **Step 5: Commit** `feat(cli): update respects locks - refusal UX, table marker, auto/--all skips (#97)`.

---

### Task 5: CLI listings — `lmm list` LOCKED column + `mod show` Installed section

**Files:** Modify `cmd/lmm/list.go` (:25-37 JSON struct, :119-131 header, :133-159 rows, plus a profile-YAML load in `doList`), `cmd/lmm/mod.go` `doModShow` (:373-452). Tests: `cmd/lmm/list_test.go` / `pin_visibility_test.go` siblings, `mod_test.go`.

**Contract:**
- `list.go`: `doList` loads the profile YAML (`config.LoadProfile(configDir, game.ID, profileName)` — mirror `coreProvider.Overview`'s tolerant `_ =` shape, service_core.go:132-140) and builds `lockedByKey map[string]domain.ModReference` keyed `sourceID:modID`. Verbose table gains a tenth column `LOCKED` (`-` or the ref's version); JSON gains `Locked bool \`json:"locked"\`` + `LockedVersion string \`json:"locked_version,omitempty"\`` (additive — JSON-contract-additions-are-MINOR precedent). Non-verbose text table unchanged.
- `mod show`: after the existing source-fetch output, add an **Installed** section sourced from the DB + profile ref — this closes #92's one unshipped row. Resolve profile (`resolveProfile(service, game.ID, modProfile)` — the persistent `-p` flag finally gets used here), `GetInstalledMod`; when not installed, print nothing extra (and JSON omits the block). When installed:
```
Installed: v1.5 (profile: default)
  Update policy: notify
  Lock: locked at v1.2.3 — run 'lmm profile apply' to converge   (or "Lock: none")
```
  (the converge suffix only when ref.Version ≠ installed version). JSON: additive `installed` object `{version, profile, update_policy, locked, locked_version}`.

- [ ] **Step 1: Failing tests**: (a) `list -v` shows the locked version in the LOCKED column for a locked mod, `-` otherwise; (b) `--json` carries `locked`/`locked_version` (and omits `locked_version` when unlocked); (c) `mod show` prints the Installed section with policy + lock and omits it when the mod isn't installed; (d) `mod show --json` carries the `installed` object. Follow `TestList_VerboseShowsUpdatePolicy` / `TestList_JSONIncludesUpdatePolicy` fixtures.
- [ ] **Step 2-4:** observe, implement, green, vet.
- [ ] **Step 5: Commit** `feat(cli): lock state in list and mod show; mod show gains Installed section (#97, completes #92's mod-show remnant)`.

---

### Task 6: TUI plumbing — ModItem, flags precedence, provider contract

**Files:** Modify `internal/tui/service.go` (ModItem :55-86), `internal/tui/service_core.go` (Overview mapping :132-155; new provider impls near `SetUpdatePolicy` :1522), `internal/tui/actions_provider.go` (interface :74-80 area; prototype impls + validator :719-745), `internal/tui/app.go` (`modFlags` :2283-2295). Tests: `internal/tui/pin_visibility_test.go` siblings, `service_core_test.go`, `actions_provider_test.go`.

**Interfaces — Produces:**
```go
// ModItem additions:
Locked        bool   // #97: profile ref carries locked: true
LockedVersion string // the ref's Version when Locked (the lock's target)

// ActionProvider additions (doc comments per SetUpdatePolicy's style):
// SetLock locks item at version (""= the ref's current recorded version).
// Metadata write on the profile ref - never touches the network or deploys;
// convergence happens on the next profile apply/switch.
SetLock(ctx context.Context, item ModItem, version string) (ActionOutcome, error)
// Unlock clears the lock marker; the version record stays.
Unlock(ctx context.Context, item ModItem) (ActionOutcome, error)
// AvailableVersions lists the distinct versions item's source reports
// (network). The lock picker's data source.
AvailableVersions(ctx context.Context, item ModItem) ([]string, error)
```
- `Overview` (profile YAML already loaded): build `map[string]domain.ModReference` and populate the two new ModItem fields.
- `modFlags` precedence: `lck` outranks `pin` in the 3-char slot; update its doc comment's layout table; the `*` session-marker slot is untouched:
```go
func (m Model) modFlags(mod ModItem) string {
	flag := ""
	switch {
	case mod.Locked:
		flag = "lck" // lock wins the slot ("the UI names the lock"); pin state stays visible in the P picker and mod actions
	case mod.UpdatePolicy == "pin":
		flag = "pin"
	}
	marker := " "
	if m.wasUpdatedThisSession(mod) {
		marker = "*"
	}
	return fmt.Sprintf("%-3s %s", flag, marker)
}
```
- `coreProvider` impls: `SetLock`/`Unlock` via `p.svc.NewProfileManager().SetModLock/ClearModLock` (map errors with the mod name, per `SetUpdatePolicy`'s wrap style); `AvailableVersions` via `p.svc.AvailableModVersions` with `mapNetworkError("listing versions", item.Source, "version resolution", "pin it instead (P)", err)`-style ErrNotSupported mapping (match `mapNetworkError`'s actual signature at service_core.go:1172). `prototypeProvider`: canned versions list + in-memory lock flags, mirroring its `SetUpdatePolicy`.

- [ ] **Step 1: Failing tests**: (a) `modFlags` renders `lck` for locked, `lck` for locked+pinned (precedence pinned by test), `pin` for pinned-only — and the three existing width/drift tests in `pin_visibility_test.go` still pass unchanged (run by name); (b) Overview populates `Locked`/`LockedVersion` from a profile fixture; (c) provider round-trip: `SetLock` then `Overview` shows locked; `Unlock` clears; (d) `AvailableVersions` maps ErrNotSupported to the friendly message. Use `recordingActions`/`failingActions` doubles (actions_provider_test.go:761, :902) extended with the three methods — every ActionProvider implementer must compile.
- [ ] **Step 2-4:** observe, implement, green (`go test ./internal/tui/` bare), vet.
- [ ] **Step 5: Commit** `feat(tui): lock state on rows and the ActionProvider lock contract (#97)`.

---

### Task 7: TUI `L` — async version picker

**Files:** Modify `internal/tui/keys.go` (:248 area, binding + help), `internal/tui/mutations.go` (new flow next to `editSelectedModPolicy` :404-454), `internal/tui/app.go` (helpGroups installedMods entry). Tests: new `internal/tui/lock_picker_test.go` mirroring `policy_test.go`'s 16-test shape.

**Contract** — the three-step async pattern (`checkForUpdates`, mutations.go:1004-1031, is the template; `openGameSwitcher`'s synchronous shape is FORBIDDEN here — this is network I/O):
1. `L` on Installed Mods (guards identical to `editSelectedModPolicy`: wrong screen/empty list/nil actions → no-op; action already running → no-op). Sets the running action state with status `Fetching versions for <name>…`, returns a Cmd calling `m.actions.AvailableVersions`, tagged with a generation counter.
2. `versionsFetchedMsg{gen, item, versions}` / `versionsFetchFailedMsg{gen, err}`: stale-gen dropped; failure surfaces as a status-line message (ErrNotSupported already mapped by T6 to name pinning as the alternative).
3. `resolveVersionsFetched` builds the picker: one option per version — `Note: "installed"` on the item's current `Version`, `Note: "locked"` on `LockedVersion` when locked (both notes may land on the same row: `"installed, locked"`); pre-select the locked target when locked, else the installed version. When `item.Locked`, append a final option `Label: "unlock"`. `choose` dispatches `lockChosenMsg{item, version}` / `unlockChosenMsg{item}` (closures never mutate the Model — same rule as `policyChosenMsg`).
4. Resolvers dispatch `buildAction` with `m.actions.SetLock` / `m.actions.Unlock` — the pick IS the confirmation. Outcome message: `<name> locked at v<version>` (+ ` — apply the profile to converge` when version ≠ installed) / `<name> unlocked`.
5. Key binding `L` / help `"lock"`, registered in the installedMods help group beside `P`. No title hint (matches `P`'s treatment — item-scoped keys live in help, screen-scoped toggles get hints).

- [ ] **Step 1: Failing tests** (mirror `policy_test.go`'s coverage): wrong-screen no-op; empty-list no-op; nil-actions no-op; fetch-failure → status line, no picker; ErrNotSupported message names pinning; stale-gen message dropped; picker rows carry installed/locked notes and pre-selection; unlock option present iff locked; choosing a version dispatches SetLock with it; choosing unlock dispatches Unlock; quit-while-fetching cancels (ctx); running-action collision no-op.
- [ ] **Step 2-4:** observe, implement, green, vet. Run the full TUI suite — the new key must not collide with existing bindings (grep keys.go for "L" first; if taken, use the next mnemonic and document).
- [ ] **Step 5: Commit** `feat(tui): L opens the lock version picker on Installed Mods (#97)`.

---

### Task 8: `verify` lock-awareness

**Files:** Modify `cmd/lmm/verify.go` (version-record check :249-378, `repairModVersion` call :328). Tests: `cmd/lmm/verify_test.go`.

**Contract:**
- Hoist a `config.LoadProfile` above the version-record loop; build the ref map.
- Locked mod with VERSION MISMATCH: still reported and counted as an issue, but `--fix` refuses for that row: `--fix skipped: %s is locked at v%s — the record is the lock's target; move the lock ('lmm mod lock %s <version>') instead of rewriting it.` JSON keeps `status:"version_mismatch"` (additive `note:"locked"` field only if the JSON already has a note field — check; else the text-only refusal suffices, document choice).
- Locked mod whose DB `mod.Version` ≠ ref `Version` (convergence pending — not corruption): informational line, NOT counted in `issues`: `~ %s — lock pending convergence (installed v%s, locked v%s) — run 'lmm profile apply'`.

- [ ] **Step 1: Failing tests**: (a) locked + mismatch + `--fix` → issue reported, repair NOT invoked (assert the record unchanged), refusal text present; (b) unlocked + mismatch + `--fix` → repaired as today (existing tests); (c) locked + DB/ref drift → informational line, exit code/issue count unaffected.
- [ ] **Step 2-4:** observe, implement, green (`go test ./cmd/lmm/` bare), vet.
- [ ] **Step 5: Commit** `feat(cli): verify is lock-aware - --fix refuses locked records, drift prints a convergence note (#97)`.

---

### Task 9: Docs, version 1.26.0, PR

**Files:** `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go`, `docs/man/**`, `docs/plans/2026-07-29-lock-vs-pinned-design.md`, this plan → archive.

- [ ] **Step 1: README.** New "Locking mods to a version" subsection (lock/unlock commands, TUI `L`, per-profile scope + export/import travel, the lock-vs-pin distinction table's shorthand: pinned mutes checks, a lock holds a version); update the pinned wording everywhere to the check-mute framing; document the interaction matrix rows users will hit (`lock+auto` skip, update refusal).
- [ ] **Step 2: Design note status.** Update its `**Status**:` line to `Implemented — #96 (v1.25.0) + #97 (v1.26.0).`
- [ ] **Step 3: CHANGELOG** `[1.26.0]`: Added — `lmm mod lock/unlock`, TUI lock picker, lock display in list/mod show/update output, update refusal for locked mods, lock-aware verify; Changed — `--pin` help reframed. Comparison links from v1.25.0.
- [ ] **Step 4:** Bump `version` to `1.26.0`; `make man`; full suite bare (`go test ./...`), `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5:** `git mv` this plan to `docs/plans/archive/`; commit `chore: bump version to 1.26.0`.
- [ ] **Step 6:** Push, PR (`Closes #97` — and note EPIC #98's sub-issues are then all complete), Copilot triage rounds incl. suppressed findings.
