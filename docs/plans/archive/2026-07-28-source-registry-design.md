# Design: Built-ins as First-Class Registry Sources (#76, settles #75)

**Status**: Approved design, pre-implementation. Implementation plan to follow.
**Issues**: #76 (registry conversion), #75 (game-scoped source listing — settled here, closed by PR 2).
**Decisions made with the user (2026-07-28)**: primary goal is eliminating special-casing (uniform mechanism), not user control over built-ins; all three behavioral special-cases are normalized (deploy default, import matching, game add menu); #75 resolves as filter-by-game-with---all; the underlying motivation is adding complex sources without recompiling — this design is the enabler, with declarative-schema power extensions filed as follow-ups.

## Problem

NexusMods (`internal/source/nexusmods/`) and CurseForge (`internal/source/curseforge/`) are wired in as compile-time special cases while custom sources (EPIC #45) flow through the registry/definition framework. Every fact the app knows about the built-ins lives in switch statements keyed on the strings `"nexusmods"`/`"curseforge"`:

- `cmd/lmm/auth.go`: `supportedSources` slice (interactive picker offers only built-ins), `getSourceDisplayName`, `printAuthInstructions`, `validateAPIKey`, `getEnvKeyForSource` — five parallel switches.
- `cmd/lmm/game_add.go`: a literal two-item menu; CurseForge catalog search and NexusMods slug entry are bespoke code paths bypassing the `ModSource` interface entirely.
- `cmd/lmm/deploy.go:54`: `-s` defaults to the literal `"nexusmods"` while search/update resolve dynamically.
- `cmd/lmm/import.go`: scan-matching consults CurseForge only; `--id` prefers CurseForge.
- `cmd/lmm/source.go` + `internal/tui/service_core.go`: the `TYPE` column is a negative type-switch over the custom types ("built-in" = whatever doesn't match), duplicated by hand across CLI and TUI.
- `source.CapabilitiesOf` defaults to all-true for sources that don't implement `CapabilityReporter` — the built-ins are "fully capable" by omission rather than declaration.

Consequence: framework improvements special-case the built-ins (#75 is soft-blocked on exactly this), and the "exactly two built-ins" assumption is baked into the UX layer.

### What "registry source" means here (and what it doesn't)

The exploration's gap analysis (see Appendix) established that expressing NexusMods/CurseForge as **YAML custom definitions** is infeasible without a large declarative-schema expansion (GraphQL/POST transport, auth-validation endpoints, slug→ID resolution and caching, batch endpoints, per-status error taxonomies, file-supersession update tracking). This design therefore takes the other meaning: **built-ins stay typed Go code, but register, declare capabilities, and expose metadata through the exact same uniform path custom sources use.** No call site anywhere branches on source identity.

## Design

### 1. Self-describing sources: optional metadata interfaces

New optional interfaces in `internal/source`, consumed via type-assertion exactly like the existing `CapabilityReporter`, `DownloadHeaderProvider`, and the ad-hoc `IsAuthenticated()`/`SetAPIKey()` seams:

```go
// EnvKeyProvider names the environment variable consulted for this source's
// API key. Absent: the derived LMM_<ID>_API_KEY convention applies.
type EnvKeyProvider interface{ EnvKey() string }

// KeyValidator performs a live API-key check at auth-login time.
// Absent: keys are stored and validated on first use.
type KeyValidator interface{ ValidateKey(ctx context.Context, key string) error }

// AuthInstructionsProvider supplies human setup steps for obtaining a key.
// Absent: generic instructions naming the env var.
type AuthInstructionsProvider interface{ AuthInstructions() string }

// GameCatalog lists the games a source knows about, for interactive
// game-creation flows. Absent: manual identifier entry.
type GameCatalog interface{ ListGames(ctx context.Context) ([]GameEntry, error) }
type GameEntry struct{ ID, Name, Slug string }

// TypeLabeler names the source's kind for listings (directory/manifest/api/
// built-in). Absent: "unknown".
type TypeLabeler interface{ TypeLabel() string }
```

Implementations:

- **NexusMods**: `EnvKey() = "NEXUSMODS_API_KEY"` (legacy name preserved exactly), `ValidateKey` wraps the existing `/v1/users/validate.json` probe, `AuthInstructions` carries the current switch text, `TypeLabel() = "built-in"`, explicit `Capabilities()` (all true). No `GameCatalog` (no usable catalog API) — game-creation degrades to manual identifier entry generically.
- **CurseForge**: `EnvKey() = "CURSEFORGE_API_KEY"`, `ValidateKey` wraps the `GetGames` live probe, `AuthInstructions` as current, `TypeLabel() = "built-in"`, explicit `Capabilities()` (all true), **`GameCatalog` wraps `GetGames`** (powering game add's interactive search).
- **Custom types**: `TypeLabel()` returns `directory`/`manifest`/`api`; nothing else changes (they already declare capabilities; the derived env key and no-validator defaults are exactly their current behavior).

`CapabilitiesOf`'s all-true-by-omission default is retained (test doubles rely on it) with an updated doc comment: production sources must implement `CapabilityReporter` explicitly; the default exists for test convenience.

### 2. One registration pipeline

`cmd/lmm/root.go`'s `registerSources` becomes a single ordered pipeline over source factories:

1. Built-in factories: `func() source.ModSource { return nexusmods.New(nil, "") }`, likewise CurseForge — constructed **without** keys.
2. Custom factories from `LoadSourceDefinitions` (unchanged loading, warnings, and skip-on-broken behavior).

Each factory's product goes through identical steps: construct → collision check (first registration wins, stderr warning — unchanged rule; defaults run first so existing configs behave identically; a user's `sources/nexusmods.yaml` still loses, shadowing stays out of scope) → resolve API key via the **one** helper (env var from `EnvKeyProvider` or the `LMM_<ID>_API_KEY` derivation, then DB token fallback) → `SetAPIKey` if the source accepts one (built-ins move from constructor-injected keys to the same post-construction seam customs use; both already expose `SetAPIKey`) → register.

`getEnvKeyForSource`'s switch dies; `envKeyForSourceID` stays as the derivation fallback.

### 3. Auth flows de-switched

- `supportedSources` is deleted. The interactive `auth login`/`logout` picker offers **every registered source with `Capabilities().Auth`**, sorted by ID. Behavior delta (intended, small): auth-capable custom sources now appear in the picker, fixing the limitation documented in the v1.20.0 docs pass.
- Display names come from `Name()` (already correct on all four concrete types); `getSourceDisplayName` dies.
- `validateAPIKey`'s switch becomes: source implements `KeyValidator` → run it live; otherwise → store with the existing "validated on first use" messaging. `printLoginResult`'s built-in-vs-custom branch keys off has-a-validator.
- `printAuthInstructions`'s switch becomes an `AuthInstructionsProvider` query with the existing generic fallback.
- `auth status` iterates the registry uniformly (auth-capable sources, sorted) plus the existing orphaned-token pass; the three-tier built-in-first logic dies. Built-ins still appear (they're always registered), so status output stays materially the same.

### 4. Behavioral normalizations (user decision: all three)

1. **`deploy -s`**: default `"nexusmods"` → `""` with `resolveSource` semantics (sole configured source → automatic; several → interactive prompt), matching search/update exactly.
2. **`import`**: scan-matching (`tryMatchCurseForge`) generalizes to iterate the game's configured sources that declare `Search`, sorted by ID; the first source whose result satisfies the **existing, unchanged match-acceptance rules** wins. (`curseforge` sorts before `nexusmods`, so typical two-source setups keep today's outcome.) `import --id`'s CurseForge-preferred default becomes `resolveSource`. `--skip-match` now skips all match lookups, not just CurseForge's.
3. **`game add`**: the menu is built from registered sources. `GameCatalog` implementers get the interactive catalog-search flow (CurseForge today; any future source for free); everything else gets the manual-identifier flow (the NexusMods slug path, generalized to "enter this source's game identifier for this game"). Custom sources become usable at game-creation time.

### 5. Game-scoped source listing (settles #75)

- **Core**: `Service.SourcesForGame(gameID string) ([]source.ModSource, error)` — intersects `game.SourceIDs` keys with the registry, sorted by ID. `SearchAllSources` refactors onto it (it hand-rolls this intersection today at `service.go:206-210`; one implementation, three consumers).
- **CLI**: `lmm source list` scopes to the active game when resolvable (`-g` or default game). New `--all` flag shows the full registry with an `IN USE` column marking the active game's sources. No game resolvable → full list, unchanged (the command must keep working with zero games configured).
- **TUI**: Sources screen shows the game-scoped list by default; `a` toggles the full-registry view with the same in-use marker. CLI/TUI parity per the standing directive.
- **Broken-definition error rows remain visible in both views** — they never registered, so they have no game association, and hiding them behind scoping would bury exactly the diagnostics a user debugging their YAML needs.

### 6. Back-compat and error handling

- **Zero config migration**: `games.yaml`, `config.yaml`, source-definition YAML, and the DB token store are untouched. Legacy env vars work exactly as before via `EnvKey()`.
- Collision rule, custom-definition warn-and-skip isolation, and the `file://` trust check in `DownloadModToCache` (asserts `*custom.Directory`) are all unchanged. The trust check is orthogonal: built-ins never serve `file://` URLs.
- Prototype/demo fixtures (`internal/tui/prototype/`, `prototypeProvider`) keep their canned `nexusmods`/`curseforge` IDs — fixture data, not wiring.
- Registration of built-ins remains unconditional and infallible (constructors cannot fail); the existing `TestInitService_RegistersSources` pins survive with the same assertions.

### 7. Testing

TDD RED-first throughout, per house rules:

- Interface-conformance tests per built-in: explicit capabilities, `EnvKey`, `ValidateKey` (mocked HTTP), `AuthInstructions` non-empty, CurseForge `GameCatalog` (mocked), `TypeLabel`.
- Registration-pipeline tests: ordering, first-wins collision with warning, key resolution precedence (env over DB token, legacy names honored, derived fallback), `SetAPIKey` seam reached for all source kinds.
- `SourcesForGame`: known game, unknown game, sources in `SourceIDs` not registered (skipped), sorted output; `SearchAllSources` behavior unchanged after refactoring onto it.
- CLI behavior tests: normalized `deploy`/`import`/`game add` flows (prompt vs auto paths), scoped `source list` + `--all` (incl. no-game fallback and broken-definition rows in both views).
- TUI: Sources screen scope toggle + goldens; in-use marker rendering.
- Auth tests: picker content now derives from the registry (existing assertions on the literal two-item prompt text updated).

### 8. Phasing and sizing

Two PRs, each MINOR:

1. **PR 1 — mechanism**: metadata interfaces, explicit capabilities, registration pipeline, auth/source-list/TUI de-switching (`TypeLabeler` kills both hand-synced switches). Behavior deltas: auth picker gains auth-capable custom sources; all else identical.
2. **PR 2 — behavior + #75**: the three normalizations plus game-scoped listings. Closes #75. #76 closes when both have landed.

### 9. Follow-ups filed after landing (the "more power without recompiling" path)

The user's underlying goal is adding complex sources (ESOUI-class) without recompiling. ESOUI-class REST sources already work via `api` definitions today; these declarative-schema extensions — deliberately out of scope here — widen what YAML can express, and the uniform mechanism this design lands means each benefits every source equally:

1. `{category}`/`{tags}` placeholders for `api` search endpoints (closes the silent drop of `SearchQuery.Category`/`.Tags`).
2. A declarative `validate` endpoint in `AuthConfig` — custom sources gain live key-checks at `auth login` via the same `KeyValidator` seam.
3. A declarative dependency endpoint for `api` sources (currently hard-disabled).
4. POST-body/GraphQL transport — largest, only worth designing against a concrete source that demands it.

## Appendix: exploration inventory (2026-07-28)

Full architecture map and the built-in-vs-declarative gap table live in the session record; the load-bearing facts:

- The registry (`internal/source/registry.go`) is fully N-source generic; "exactly two built-ins" exists only in the CLI/UX layer.
- No test outside `internal/source/{nexusmods,curseforge}` imports those packages; test ripple concentrates in `cmd/lmm/helpers_test.go`, `root_test.go` (registration pins — assertions survive) and `auth_test.go`/`auth_status_test.go` (literal prompt-text assertions — updated in PR 1).
- Custom sources associate with games identically to built-ins (`games.yaml` `sources:` map → `Game.SourceIDs`); `SearchAllSources` and the TUI's `Sources()` are already game-scoped; only the *listing* paths (`ListSources` consumers) are registry-wide — hence #75.
- Gap table (why not YAML): NexusMods needs GraphQL POST, validate endpoint, FileUpdates supersession, dependency GraphQL; CurseForge needs slug→ID caching, batch endpoints, per-status error mapping, filename version heuristics; `custom.API` is GET-only, flat-JSON, no category/tags, no dependencies, no validation.
