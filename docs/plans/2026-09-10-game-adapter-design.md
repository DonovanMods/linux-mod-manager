# The game-adapter seam — design (#353)

**Date:** 2026-09-10 · **Issue:** [#353](https://github.com/DonovanMods/linux-mod-manager/issues/353) · **Milestone:** v2.0.0
**Status:** design, for approval. No code in this unit.
**Inputs:** #353's settled decision (in-tree, compile-time, **no dynamic plugins**), the external review's §3, the BepInEx spike ([docs/plans/2026-09-09-bepinex-spike.md](2026-09-09-bepinex-spike.md)) and the Tier-1 code already on `dyoung522/v2-bepinex`, today's seams (`domain.DeployMode`, `source.MergeCompiler`, `archive_listing.go`, `overrides.go`, `verify.go`, per-game hooks, `internal/source/steam/data`), and [docs/plans/2026-08-27-v2-core-refactor-design.md](2026-08-27-v2-core-refactor-design.md) (Plan/Apply, typed errors, thin frontends, additive JSON).

---

## TL;DR

The seam already exists; it is just spelled three different ways and hung off
the wrong noun. `source.MergeCompiler` makes game-specific compilation a
property of a **source**, so an Icarus `.pak` downloaded from NexusMods
cannot compile. `bepinexNormalise` makes archive layout a property of a
**bool parameter** threaded through core. `.EXMODZ`'s wrapper strip lives
inside a format parser. Each is really a property of the **game**.

This design names that noun `adapter.GameAdapter`, puts it under
`internal/adapter/<name>`, and selects it with one new `adapter:` key in
`games.yaml` defaulting to `generic-files`. Adapters supply **pure rule
tables and reports**; core keeps every side effect it has today — it applies
the rewrites, writes the files, runs the repairs, emits the events. The
default adapter is the identity function, which is what makes
"byte-for-byte identical behaviour for every existing game" a provable claim
rather than an aspiration.

Two real adapters come out the other side (Icarus's compile path, BepInEx's
layout + loader), which is the whole reason this doc waits on #358/#359: an
interface extracted from one implementation is a rename.

---

## 1. The interface

Package `internal/adapter`. One required core, plus optional capabilities
that core type-asserts for — the idiom `source` already proves with
`CapabilityReporter`, `MergeCompiler` and `WorkshopScanner`, so no adapter
pays for a capability it does not have and core never imports a concrete
adapter.

```go
// The whole required surface.
type GameAdapter interface {
    ID() string    // "generic-files", "icarus", "bepinex" — the games.yaml value
    Label() string // "BepInEx (Unity plugin loader)" — display only

    // NormalizeArchive is the adapter's rule TABLE, not its executor: it maps
    // an archive's member list to the cache-entry-relative paths those
    // members take, and says nothing about disk.
    NormalizeArchive(req NormalizeRequest) (Layout, error)
}

type NormalizeRequest struct {
    Game    *domain.Game // the game and its loader block; never mutated
    ModName string       // for adapters that name a directory after the mod
    Members []string     // slash-separated, archive-relative, files only
}

// Layout is one answer. A zero Layout is the identity — Applies() false,
// Rewrite() returns its argument — so core holds one unconditionally
// instead of branching at every member.
type Layout struct {
    Kind     string   // adapter-defined diagnostic ("game-root-relative"); never a wire value
    Warnings []string // surfaced verbatim; an adapter warns, it never guesses
    // rewrites/applies unexported; Applies() and Rewrite(member) are the API.
}
```

**Optional capabilities**, each a one-purpose interface:

| Interface | Method | Who implements it | What core does with it |
| --- | --- | --- | --- |
| `MergeCompiler` | today's `source.MergeCompiler`, moved verbatim (11 methods) | `icarus` | the whole `merged_pak.go` compile path, unchanged |
| `FileRouter` | `RouteFile(g *domain.Game, rel string) FileRoute` | `bepinex` | decides link / copy-once / skip per deployable file |
| `Preconditioner` | `CheckPreconditions(g *domain.Game, mods []domain.InstalledMod) error` | `bepinex` | one call in `plan.go`; the error becomes a typed core error |
| `Verifier` | `Verify(ctx, VerifyRequest) ([]Finding, error)` | `bepinex` | appends adapter findings to `VerifyResult.Findings` |
| `Guide` | `Guidance(g *domain.Game) []GuidanceNote` | `bepinex` | launch/bootstrap text on `game list`, `verify`, the web game card |

### Decisions inside the interface

**Adapters supply rules; core owns every side effect.** `NormalizeArchive`
is pure. The tree rewriter that applies a `Layout` to an extracted directory
— rename, drop, `CleanupEmptyDirs` — is **core's**, generalised from
`normalizeBepInExTree`, and it is written once. So an adapter is a table
test, and a bug in the rewriting is a bug in one place. This also keeps
`internal/adapter` free of `internal/linker`.

**The deploy mapping is path rewriting against a single root, and that is
enough for both real adapters.** BepInEx is the proof: a game whose
`mod_path` *is* its `install_path`, plus members rewritten under `BepInEx/`,
is a multi-target deploy (plugins, patchers, monomod, config) expressed with
one root and no new deploy-rule type. Icarus deploys one merged artifact into
`mod_path`. **No `roots:` map, no second deploy target in 2.0.** The case
that genuinely needs one — Bethesda's `Data/` beside a game-root SKSE/ENB —
has no adapter here to design against, and `FileRoute` is where a `Root`
field lands additively on the day it does.

**`FileRoute` is how "this file is not ordinary mod content" is said.**

```go
type FileRoute int
const (
    RouteLink     FileRoute = iota // default: the linker deploys it (every generic file)
    RouteCopyOnce                  // a real file, copied on first deploy, never overwritten,
                                   // never entered into deployed_files — applyProfileOverrides'
                                   // exact semantics, reused (BepInEx/config/**)
    RouteSkip                      // ingested and cached, never deployed
)
```

`RouteCopyOnce` generalises #358(b) without inventing a mechanism:
`overrides.go` already writes real files with copy-on-first-deploy,
never-overwrite semantics, and the reason is identical (a user hand-edits
the file after first run; a symlink would push the edit back into the shared
cache entry). An adapter with no `FileRouter` gets `RouteLink` for every
file, which is today's behaviour exactly.

**Compilation is a capability of the adapter, not of a source.** Today
`Service.mergeCompilerForGame` walks the game's `sources:` map looking for
the sole source implementing `source.MergeCompiler`, and fails loud on zero
or many. That is the wrong question — it makes "can this game compile" depend
on where its bytes came from. After the move, `game.Adapter` answers it: one
game, one adapter, zero ambiguity, and an Icarus `.pak` fetched from
NexusMods or imported from a local archive compiles like any other.
`soleMergeCompiler` and both resolvers are **deleted**, not adapted.

**Verify: adapters report, core repairs.** An adapter's `Verify` is
read-only and returns `Finding` values that map field-for-field onto
`core.VerifyFinding` (`Status`, `Note`, `Fixable`, `FixableReason`). No
adapter writes to the game directory, and **no adapter finding is
`--fix`-able in 2.0**. That is not a limitation, it is what the evidence
says: of the spike's six BepInEx checks, the only fixable one ("every
enabled plugin is linked") is not an adapter check at all — core's existing
per-file deployment pass already produces `stale_deployment`/`missing` for
it and `verify_repair.go` already fixes it. What the adapter adds is exactly
the set core cannot see: preloader present, declared-version drift,
bootstrap files matching the declared mode, `BepInEx/LogOutput.log` newer
than the last deploy. All four are honestly non-fixable — they are
instructions to the user, and they carry a `FixableReason` saying so.

**Adapters raise no events and own no `Op`.** An adapter runs inside a
flow that already has one (`OpInstall`, `OpVerify`, `OpMergeRegen`); a
second vocabulary would mean two sources of truth for the SSE stream and a
new `EventType` per adapter. Adapter diagnostics travel as `Layout.Warnings`
and `Finding` rows, which every frontend already renders.

**Typed errors are wrapped by core.** `internal/adapter` declares two
sentinels — `ErrNotAMod` (the framework-pack refusal, generalised: "this
archive is the loader/base game, not a mod for it") and
`ErrPreconditionUnmet` — and adapters wrap them with a message naming the
remedy. Core translates them into `core.AdapterPreconditionError`, which
implements the `--json` envelope's `Details() any` extension point. Doing
the `Details()` half in core, not in the adapter, keeps
`cmd/lmm/details_coverage_test.go`'s ledger and the wire contract in the
packages that already own them, and keeps `internal/adapter` importing
nothing but `internal/domain`.

**`generic-files` is the identity and that is enforced, not asserted.** It
implements only the required three methods; its `NormalizeArchive` returns
the zero `Layout` for every input. §6 is how that becomes a proof.

---

## 2. Selection

### The key

```yaml
games:
  lethal-company:
    install_path: /home/you/.steam/.../Lethal Company
    mod_path: /home/you/.steam/.../Lethal Company   # the install path, twice
    adapter: bepinex
    loader:
      kind: bepinex
      version: 5.4.23.5
      runtime: mono
      bootstrap: native
```

`GameConfig.Adapter` (``yaml:"adapter,omitempty"``) and
`domain.Game.Adapter` (``json:"adapter,omitempty"``). Empty means
`generic-files`, and the writer emits the key only when it is not the
default — `deploy_mode`'s existing rule, so an untouched `games.yaml`
round-trips byte-identically.

**Validation splits by layer.** `storage/config` checks syntax only (a
non-empty slug); it must not learn the registry. Core validates *existence*
when it resolves a game, failing loud with the registered names — the same
treatment `soleMergeCompiler` gives an unconfigured compile game today, at a
better moment.

### Composition with `deploy_mode`

`deploy_mode` keeps its meaning — *how a downloaded file is handled* — and
stays orthogonal for its two ordinary values: a BepInEx game is `extract`, a
Hytale-style game is `copy`, both with `generic-files`. Only `compile` is
really an adapter fact.

**Decision: `deploy_mode: compile` and `adapter:` coexist for 2.0, with a
read-only migration.**

- On load, `adapter` empty + `deploy_mode: compile` ⇒ `adapter = "icarus"`.
  Every existing `games.yaml` keeps working with no user action.
- An explicit `adapter:` wins.
- `deploy_mode: compile` with an adapter that does **not** implement
  `MergeCompiler` is a load-time error naming both keys and the fix. One
  truth after normalisation.
- Nothing is rewritten on save: a file the user did not edit is written back
  as it was read.
- Retiring `deploy_mode: compile` is a config-format break, therefore a
  MAJOR change, therefore **out of scope for 2.0** (see OQ1).

Core derives compile-ness from exactly one expression after load —
`adapter implements MergeCompiler` — and `domain.DeployCompile` survives as
the config spelling and the `importArchiveKind` branch it already drives.

### Composition with `loader:`

`loader:` (#359) stays on `domain.Game`, not on the adapter. It describes
the **installation** — which loader build is present, Mono or IL2CPP, native
or Proton — and it is populated by detection and checked by verify;
those facts outlive any adapter decision. The adapter is what makes the
block *meaningful*: only `bepinex` reads it. A game carrying `loader:` with
`adapter: generic-files` warns once at load ("the loader block is ignored
until the game's adapter is `bepinex`") rather than erroring, because a
half-configured game mid-setup is a real state.

This composition also deletes a hedge. #358's `bepinexNormalise` takes a
`loaderDeclared bool` to gate its two ambiguous shapes (a bare `plugins/`
root, a loose root `.dll`), because a 7 Days to Die archive rooted at
`plugins/` must not be silently moved under a `BepInEx/` directory. After
the extraction the bepinex adapter is only ever *asked* about a bepinex
game, so the gate is structural: the parameter goes away and the failure
mode it guarded becomes unrepresentable.

### Composition with `mod_path`

Unchanged, and no adapter gets a say. The curated known-games entry already
owns it, including the game-root case a BepInEx game needs
(`mod_path == install_path`, never `mod_path: ""` — the spike's correction).
Adding an adapter opinion here would give two answers to one question.

### Prefill from detection

`internal/source/steam/data/steam-games.yaml`'s `GameInfo` gains an optional
`adapter:` passthrough beside its existing `deploy_mode`/`sources`, and
`domain.DetectedGame` gains an `Adapter` field (``json:"adapter,omitempty"``)
beside `DeployMode`. Curated entries are edited in the same commit as U3:
Icarus (app `1149460`) already says `deploy_mode: compile`, so it gains
`adapter: icarus` explicitly rather than relying on the migration; the
loader games the spike named (Lethal Company, Valheim, …) gain
`adapter: bepinex` and the game-root `mod_path`. An **unknown** detected
game carries no adapter, which reads as `generic-files` — the same
conservative default #206 already gives its empty `ModPath`.

---

## 3. The extraction plan

The rule for every unit: **move, do not rewrite.** A diff that changes the
body of an existing Icarus test is the review's stop signal — those tests
are the regression net, and a net you adjusted to fit is not a net.

### U1 — the seam and the generic adapter

`internal/adapter` (interface, capabilities, `Layout`, `FileRoute`,
`Finding`, `GuidanceNote`, sentinels, `Registry`) and
`internal/adapter/generic`. `domain.Game.Adapter`, the `games.yaml`
round-trip, the load-time resolution and migration, `Service`'s resolver,
`app` registering the two adapters that exist at this point (generic, and
icarus once U2 lands — U1 registers generic only).

Core starts calling the adapter at exactly the points it calls the seams
today — no new call sites, no moved logic:

| Call site today | Adapter call after |
| --- | --- |
| `archive_listing.go:classifyImportArchive(game, mc, filename)` | unchanged shape; `mc` comes from the adapter, not the source walk |
| `importWithIdentity` / `extractIntoStaging`, after extraction | core's tree rewriter applies `NormalizeArchive`'s `Layout` |
| `importDeployablePaths` (the plan half) | the same `Layout`, so plan and ingest cannot disagree |
| `deployableFiles` → linker | `RouteFile` classifies each file (all `RouteLink` for generic) |
| `applyProfileOverrides` | additionally receives the `RouteCopyOnce` files |
| `plan.go`'s precondition block | `CheckPreconditions`, when implemented |
| `verify.go`'s pass list | `Verify`, appended after the existing passes |
| `merged_pak.go`, `install.go:2123`, `service.go:1105/1585/1608` | the adapter's `MergeCompiler` instead of the source type-assertion |

Because the only adapter registered in U1 is the identity, **the entire
golden set must pass with no re-recording** (§6). That is deliberate: the
riskiest change (new call sites through every flow) is proved safe before
any behaviour moves through them.

### U2 — Icarus moves

`internal/source/icarus` splits along a line that is already there. The
Firestore `ModSource` (`Search`/`GetMod`/`GetModFiles`/`GetDownloadURL`/
`CheckUpdates`, `firestore_client.go`, `firestore_value.go`) stays in
`internal/source/icarus`. The 11 `MergeCompiler` methods and the format
code they own (`compile.go`, `merge.go`, `exmod.go`, `exmodz.go`,
`format.go`, `pakconvert.go`) move to `internal/adapter/icarus`, with their
tests — `merge_test.go`, `golden_test.go`, `pakconvert_test.go`,
`exmodz_test.go` and the rest — moved unmodified but for the package clause
and import paths.

`.EXMODZ`'s wrapper strip (`modWrapperDir`/`stripModWrapper`, #237) stays
inside the compile implementation and does **not** become
`NormalizeArchive`. It is a rule about the *bundle format's payload*, read
at merge time; `NormalizeArchive` is a rule about an *archive's layout on
disk*, applied at ingest. Conflating them would move a correctness fix that
took a released bug to find.

Resolution flips: `mergeCompilerSourceForGame`, `mergeCompilerForGame` and
`soleMergeCompiler` are deleted and replaced by one `adapterCompiler(game)`.
The user-visible improvement lands here: a `.pak` or `.exmodz` for Icarus
compiles regardless of which source served it.

### U3 — BepInEx moves

`bepinex_layout.go`'s rule table becomes
`internal/adapter/bepinex.NormalizeArchive`, dropping the `loaderDeclared`
parameter; `normalizeBepInExTree` becomes core's generic tree rewriter;
`isBepInExConfigMember` becomes `RouteFile` returning `RouteCopyOnce` for
`BepInEx/config/**`; `ErrBepInExFrameworkPack` becomes
`adapter.ErrNotAMod` wrapped with the same message. #359's loader
precondition, verify tier and launch guidance land behind
`Preconditioner`/`Verifier`/`Guide` rather than as `if game.Loader != nil`
branches in core. Its tests move with it, including the shape table over the
spike's five real Thunderstore packages.

### U4 — docs

README architecture section (the adapter row in the layout tree, and the
review's `generic-files / icarus / bepinex / …` list as *shipped and
possible*, not as a promise), `docs/configuration.md`'s `adapter:` key with
the migration note, and `docs/adapters.md` — how to write one, in the
"here is the interface and here are two worked examples" form, because the
whole point of #353 is that a contributor can add one without touching core.

### Order and parallelism

U1 is a hard gate. **U2 ∥ U3** afterwards: they touch different adapter
packages and mostly different core call sites, with one real conflict
surface (`service.go`'s resolver and `app/sources.go`'s registration), which
is a few lines each and a merge, not a redesign. U4 runs alongside both and
lands last. If the coordinator would rather not pay the merge, U2 → U3
serially costs one unit of wall clock and nothing else; U2 first either way,
because it is the one carrying real regression risk.

| Unit | Size | Depends on | Parallel with |
| --- | --- | --- | --- |
| U1 seam + generic adapter | **M** | this design approved | — |
| U2 Icarus extraction | **M** | U1 | U3, U4 |
| U3 BepInEx extraction | **M** | U1, #358 + #359 merged | U2, U4 |
| U4 docs | **S** | U1 | U2, U3 |

---

## 4. Boundary rules

```text
internal/adapter/          interface + registry     imports: internal/domain, internal/source
                           + the built-in identity  (source only for the merge primitives U1
                                                     aliases; they MOVE here in U2)
internal/adapter/icarus/   the compile adapter      imports: internal/domain, internal/adapter
internal/adapter/bepinex/  the loader adapter       imports: internal/domain, internal/adapter
internal/core/             imports internal/adapter — NEVER internal/adapter/<name>
internal/app/              imports the concrete NAMED adapters and registers them
cmd/lmm, internal/serve/   unchanged allow-lists
```

**The identity adapter has no package of its own.** `generic-files` is
`adapter.Generic`, declared in `internal/adapter` and pre-registered by
`NewRegistry`, so `Resolve` can never hand core a nil adapter. See the
2026-09-10 amendment below for why.

**`internal/source` is deliberately not on that list.** A source is where
bytes come from; an adapter is what a game does with them, and the Icarus
split in U2 is the proof they are different questions. So
`source.MergeCompiler`, `source.MergeSource` and `source.MergeFailure` move
into `internal/adapter` rather than being imported from it, and nothing
outside core referenced them.

**`internal/adapter/boundary_test.go`** enforces this the way
`internal/serve/boundary_test.go` does — a live `go list` per subpackage
against an allow-list, no escape hatch — and additionally asserts that
`internal/core` appears in no adapter's imports, which is the violation the
whole design exists to prevent.

**`cmd/lmm`'s allow-list does not change.** The CLI never imports
`internal/adapter`: `Service.ListAdapters()` gives `--adapter`'s validation
and its prompt the registered names, so `--adapter` is a plain string flag
and the boundary ratchet stays at five entries.

**The doc-comment ratchet** (`internal/testutil.UndocumentedExports`) gains
`internal/adapter` and each subpackage, in each package's own
`doc_comment_test.go`.

---

## 5. Frontends

Additive everywhere; nothing is removed or renamed. The parity ledger:

| Surface | Today | After |
| --- | --- | --- |
| `lmm game list` | columns incl. deploy mode, convert-paks | + an **Adapter** column (one word, always shown) |
| `lmm game list --json` | game document | + `adapter`; + `guidance[]` when the adapter offers notes |
| `lmm game add` | no deploy/adapter flags at all | + `--adapter <name>`; prefilled by `--from-detected`'s curated entry; the interactive path prompts with the registered list |
| `lmm game edit` | `--source`, `--remove-source` | + `--adapter <name>`, refusing the `deploy_mode: compile` + non-compiling-adapter combination by name |
| `lmm game detect` / `--json` | `DetectedGame` | + `adapter` (omitempty), beside the existing `deploy_mode` |
| `lmm verify` / `--json` | findings + summary | adapter findings render as ordinary `VerifyFinding` rows; guidance notes print after the summary |
| `lmm install` / `lmm import` | conflict & confirmation errors | + the loader precondition as a typed error with `Details()` |
| `GET /api/v1/games`, `POST /api/v1/games`, `PUT /api/v1/games/{id}` | game document / sources edit | + `adapter` read and written |
| SPA `gameadd.js` | mod path, sources, detection prefill | + an adapter `<select>` prefilled from detection |
| SPA game card / Health | game facts, verify findings | + the adapter name; guidance notes as a note row |
| README | architecture tree, non-goals | + the adapter layer and `docs/adapters.md` |

There is **no `lmm game show`** — `lmm game list [--json]` is the read
surface, and this design invents no command to fill the gap.

**JSON is additive only.** Three new keys total (`adapter` on the game
document and on `DetectedGame`, `guidance` on the game list entry), each
`omitempty`/`omitzero`, so every document for a generic game is byte-identical
to today's and no existing golden is re-recorded (§6). New goldens cover the
added keys; `internal/serve`'s `TestJSONWireContractCoverage` ratchet gets a
golden for any serve type that gains a tag. Per the tracker's own precedent,
additive JSON is MINOR — and nothing here is a CLI or config break, since
`deploy_mode: compile` keeps working.

---

## 6. Testing

**Per adapter — pure table tests, no mocks.** `NormalizeArchive` takes a
member list and returns a `Layout`, so an adapter's whole rule surface is
testable without a filesystem: BepInEx over the spike's five real
Thunderstore packages plus the framework-pack refusal and the
unrecognised-root warning; Icarus over its `.exmodz`/`.pak` classification.
Icarus's merge, golden and pakconvert tests move unmodified and remain the
regression net for the compile path — U2's review rejects any diff that
edits one.

**The generic byte-identity proof**, in four parts, all of which must hold
at the end of U1:

1. **No golden is re-recorded.** `make test` and `make test-race` pass with
   none of `-update-json-goldens`, `-update-app-json-goldens`,
   `-update-json-cli`, `-update-serve-json-goldens` or any per-command
   `-update-*` flag ever passed. The whole existing set —
   `internal/{core,domain,app}/testdata/json/`,
   `cmd/lmm/testdata/json_golden/`, `internal/serve/testdata/json/` — is the
   proof, and re-recording is the failure.
2. **`internal/adapter/generic/identity_test.go`**: over a corpus of member
   lists taken from the existing import fixtures (every archive shape in
   `internal/core`'s import tests), `Layout.Applies()` is false and
   `Rewrite(m) == (m, true)` for every member; `RouteFile` is absent, so
   every file routes `RouteLink`.
3. **`internal/core/adapter_generic_test.go`**: a game with no `adapter:`
   resolves to `generic-files`, and each new call site takes its identity
   branch — asserted as *no rewrite and no route change*, not by restating
   the outcomes the golden set already pins.
4. **Config round-trip**: a pre-#353 `games.yaml` (including one with
   `deploy_mode: compile`) loads and saves byte-identically, and the loaded
   game resolves to `icarus`.

**New coverage** that is genuinely new behaviour: the migration and its
error case; the `loader:`-without-`bepinex` warning; the precondition typed
error's `Details()` shape (with its entry in
`cmd/lmm/details_coverage_test.go`'s ledger); adapter verify findings
flowing into `VerifyResult` with `Fixable` false and a real
`FixableReason`; `RouteCopyOnce` reaching `applyProfileOverrides` and never
entering `deployed_files`; the boundary and doc-comment ratchets.

---

## 7. Open questions

Three, and only three — everywhere else one option was clearly better and
the decision is above.

**OQ1 — Does 2.0 keep `deploy_mode: compile`, or retire it?**
*Recommendation: keep it, exactly as §2 describes.* Retiring it is a config
break in the release that is meant to be the first stable public v2, for a
key one shipped game uses and a migration that already works silently.
Retirement belongs to a MAJOR, with a deprecation note in `docs/configuration.md`
now. The cost of keeping it is one derivation rule and one load-time error —
cheap, and the alternative asks every existing Icarus user to edit a file to
get their working install back.

**OQ2 — `icarus` names both a source and an adapter. Live with the
collision, or rename the source?**
*Recommendation: live with it, documented.* They are different namespaces
(`sources:` vs `adapter:`) and the collision is honest — the Project
Daedalus source and the Icarus adapter really are about the same game.
Renaming the source (say to `project-daedalus`) breaks every existing
`games.yaml` `sources:` map and every stored auth row keyed by source id, to
fix a confusion that one sentence of documentation also fixes. Worth asking
because the collision will look like a bug to a reader of `docs/adapters.md`.

**OQ3 — Is the loader adapter named `bepinex`, or `unity-loader` with
`loader.kind` selecting the flavour?**
*Recommendation: `bepinex`.* It follows the spike's own ruling ("resist
inventing a general framework abstraction before there is a second
framework") and costs nothing later: MelonLoader arrives as
`internal/adapter/melonloader`, sharing whatever the two turn out to
actually share, chosen with real code in front of us. `unity-loader` is a
guess about MelonLoader's shape made before anyone has read it. Worth asking
because the name is in `games.yaml` — user-visible config — and renaming an
adapter later is the same kind of break as OQ2.

---

## Decisions log

1. `internal/adapter.GameAdapter`; three required methods, five optional capability interfaces, type-asserted the way `source` already does it.
2. Adapters are pure rule tables and read-only reports; core keeps every side effect, including the tree rewriter, the file writes and the repairs.
3. One deploy root plus path rewriting. No `roots:` map in 2.0; `FileRoute` is where a second root lands additively.
4. `FileRoute` = link / copy-once / skip; `RouteCopyOnce` reuses `applyProfileOverrides`' semantics for `BepInEx/config/**`.
5. Compilation becomes a property of the game's adapter, not of a mapped source; `soleMergeCompiler` and both resolvers are deleted.
6. Verify: adapters report, core repairs. No adapter finding is `--fix`-able in 2.0, and that is what the evidence says, not a shortcut.
7. Adapters raise no events and own no `Op`; diagnostics travel as `Layout.Warnings` and `Finding` rows.
8. `internal/adapter` declares sentinels; core wraps them into the typed error carrying `Details()`, keeping the wire contract and its ratchet in core.
9. `adapter:` in `games.yaml`, empty ⇒ `generic-files`, written only when non-default; syntax checked in `storage/config`, existence checked in core.
10. `deploy_mode: compile` and `adapter:` coexist; `compile` with no adapter migrates to `icarus` on load, read-only, and nothing is rewritten on save (OQ1).
11. `loader:` stays on `domain.Game`; the adapter makes it meaningful; `loader:` without `adapter: bepinex` warns rather than errors.
12. The adapter selection replaces #358's `loaderDeclared` gate, making the shape it guarded against unrepresentable.
13. `mod_path` gets no adapter opinion; curated detection entries keep owning it.
14. Detection prefills `adapter:` through `GameInfo` and `DetectedGame`; unknown games get none, which reads as generic.
15. Move, do not rewrite: an edited Icarus test in U2 is a review stop signal.
16. `.EXMODZ`'s wrapper strip stays in the compile implementation — a format rule, not a layout rule.
17. Adapters import `internal/domain` and `internal/adapter` only; not `internal/source`; a boundary test enforces it and asserts core is absent.
18. `cmd/lmm`'s allow-list does not change; `Service.ListAdapters()` serves the CLI.
19. Three new JSON keys, all additive; no existing golden is re-recorded, and re-recording one is the failure signal.
20. Units: U1 seam+generic (M, gate) → U2 Icarus (M) ∥ U3 BepInEx (M, needs #358+#359) ∥ U4 docs (S).

---

## Approved with notes (coordinator, 2026-09-10)

Approved as written. The three open questions are answered as recommended, and each is
binding on U1–U4:

- **OQ1 — keep `deploy_mode: compile`** in 2.0, derived from the adapter as §2 describes; a
  deprecation note in `docs/configuration.md` now; retirement is a MAJOR.
- **OQ2 — live with the `icarus` source/adapter name collision**, documented in one sentence
  wherever `docs/adapters.md` first names both.
- **OQ3 — the loader adapter is named `bepinex`**; MelonLoader, if it ever comes, is its own
  adapter and the two share what they actually share.
- **"Move, do not rewrite" is the review's stop signal**: a diff that changes the body of an
  existing Icarus test is a NOT READY on its own. U1 must land with the whole golden set
  byte-identical and no re-recording.
- **Order**: U1 is a hard gate; U2 (Icarus) first after it; U3 (BepInEx) after U1 AND after
  #358/#359 merge; U4 alongside; U2 ∥ U3 is allowed if the coordinator has the slots.
- **Boundary**: the new ratchet (internal/adapter/* imports only domain and source
  primitives, never core; core imports the registry only) lands in U1, before any adapter
  moves.

### Amendment — U1 implementation (coordinator ask, 2026-09-10)

Three points of §2 and §4 were settled against U1's own acceptance criterion (the entire
golden set byte-identical, no re-recording) while implementing #411, and they bind U2–U4.
First, **`domain.Game.Adapter` carries the CONFIGURED value only** (`omitempty`), and the
`deploy_mode: compile` ⇒ `icarus` derivation lives in core's resolver (`Service.AdapterName`
/ `AdapterFor`) rather than at config load: a derived value on the domain type would have
added `"adapter": "icarus"` to an existing CLI golden and would have been written back into
a `games.yaml` the user never edited. Core is also where the registry is, which §2 already
made the home of existence checking, so the compile-composition rule ("`compile` with an
adapter that cannot compile is an error naming both keys") lands there too — in U1 it fires
for an EXPLICIT adapter only, because a compile game with no key must keep working. Second,
**the derivation yields `icarus` only once that adapter is registered**, so U1 (which
registers none) leaves compile games on today's source-based `MergeCompiler` resolution and
U2 activates the derivation with no code change; `mergeCompilerForGame` /
`mergeCompilerSourceForGame` / `soleMergeCompiler` accordingly survive U1 as a documented
fallback behind `Service.adapterCompiler`, and U2 deletes them as §3 says. Third, **there is
no nil adapter**: the identity lives in `internal/adapter` itself as the registry's built-in
default (`internal/adapter/generic` folds away), because "nil means identity" is an implicit
contract every future seam call would have to remember — exactly the class of bug this
design exists to prevent. §6's identity proof is unchanged; it moved to
`internal/adapter/identity_test.go`. Finally, the known-games catalog gaining
`adapter: icarus` (§2, "Prefill from detection") is a **U2** change, not U1, because it
would move the detect golden.
