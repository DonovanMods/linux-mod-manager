# Game adapters

A **game adapter** is the one place lmm says *what a particular game does
with mod content*. It is the counterpart to a **source**, which says where
a mod's bytes came from — and keeping the two apart is the whole point:
before adapters existed, compiling was a property of the source a file was
downloaded from, so an Icarus `.pak` from NexusMods could not compile while
the identical file from Project Daedalus could.

Adapters are **in-tree and compile-time**. There is no plugin system and
there will not be one: Go's `-buildmode=plugin` forces CGO, which would end
lmm's static, CGO-free binary. Adding an adapter means adding a package
under `internal/adapter/` and one registration line — and *nothing* in
`internal/core`.

- **Choosing one for your game:** [configuration.md's Adapter
  section](configuration.md#adapter-gamesyaml).
- **Why it is shaped this way:**
  [docs/plans/2026-09-10-game-adapter-design.md](plans/2026-09-10-game-adapter-design.md).

> `icarus` names both a mod **source** (`sources:`) and an **adapter**
> (`adapter:`). They are different keys and different jobs — the source is
> where a mod's bytes come from (Project Daedalus), the adapter is what the
> game does with them once they arrive — and a game may use either without
> the other. The collision is honest rather than accidental: they really
> are about the same game.

## What ships today

| Adapter | Selected by | What it does |
| --- | --- | --- |
| `generic-files` | the default — an absent `adapter:` key | The identity. An archive lands exactly where it was extracted, every file is deployed by the linker, and nothing extra is checked. This is what every game lmm managed before adapters existed. |
| `icarus` | `adapter: icarus`, or derived from `deploy_mode: compile` | Icarus's compile path: every enabled mod's `.EXMODZ` table diffs (and, with `convert_paks`, its prebuilt `.pak` artifacts) merge against the installed game's own `data.pak` into one artifact. |
| `bepinex` | `adapter: bepinex`, or derived from a game whose `mod_path` is its install path and that declares `loader: kind: bepinex` **or** has BepInEx installed in its directory | Plugin archive layout, `BepInEx/config/**` seeding, the "this game has no BepInEx" refusal, and verify's loader-installation tier. |

`lmm game list` always names what *this* build actually ships, and
`lmm game add --adapter` / `lmm game edit --adapter` validate against the
same list — so the table above is documentation, not the source of truth.

### Which adapter a game uses

`lmm game list`, `lmm game show` and `GET /api/v1/games[/{id}]` name the
adapter a game **resolves** to, derived or not. In the JSON document,
`adapter` is exactly what `games.yaml` says and `effective_adapter` is the
one in use. `effective_adapter` is omitted for `generic-files`: **an
absent `effective_adapter` means `generic-files`**, the same "absent means
the identity" rule `adapter` follows, unless `adapter_error` is set.

`adapter_error` is present only for a game no flow can run on: an
`adapter:` this build does not ship, or one a composition rule below
refuses. It carries the refusal every flow on that game makes, and such a
game has no `effective_adapter`, because it uses no adapter at all. The CLI
renders it as `<adapter> (refused)`, and `lmm game show` prints the reason.

## The division of labour

This is the design's load-bearing decision, and everything below follows
from it:

> **An adapter supplies pure rule TABLES and read-only REPORTS.
> `internal/core` keeps every side effect** — it applies the rewrites,
> writes the files, runs the repairs, emits the events.

So an adapter is a table test. The rename/drop/cleanup executor is written
once, in core, which means a bug in *rewriting* is a bug in exactly one
place rather than one per adapter. It is also why `internal/adapter` imports
neither `internal/core` nor `internal/linker` (and a boundary test keeps it
that way).

Two corollaries worth stating outright:

- **Adapters raise no events and own no `Op`.** An adapter runs inside a
  flow that already has one (`OpInstall`, `OpVerify`, `OpMergeRegen`); a
  second vocabulary would mean two sources of truth for the SSE stream.
  Adapter diagnostics travel as `Layout.Warnings` and `Finding` rows, which
  every frontend already renders.
- **No adapter finding is `--fix`-able.** Adapters report; core repairs.
  That is what the evidence says rather than a shortcut — see
  [Verifier](#verifier) below.

## The required interface

Three methods. An adapter that implements only these is a legal adapter and
behaves exactly as lmm behaved before adapters existed.

```go
type GameAdapter interface {
    ID() string    // the games.yaml value, and the registry key
    Label() string // the display name a frontend shows; nothing branches on it

    // NormalizeArchive is the adapter's rule TABLE, not its executor: it
    // maps an archive's member list to the cache-entry-relative paths those
    // members take, and says nothing about disk.
    NormalizeArchive(req NormalizeRequest) (Layout, error)
}
```

`NormalizeRequest` carries no filesystem handle on purpose — layout rules
that are a pure function of the member list are table-testable without a
temp directory:

```go
type NormalizeRequest struct {
    Game    *domain.Game // never mutated
    ModName string       // the mod's name: from the archive on import, from the source on download
    Members []string     // slash-separated, archive-relative, files only, SORTED
}
```

Core normalises `Members` once, at the seam, which is why an adapter can
string-match `"BepInEx/config/"` without caring what separator the host
filesystem uses, and why a plan and the ingest that applies it hand the
adapter the same order.

One question genuinely cannot be answered from a member list: for a game
whose `mod_path` is its install path, is an archive's root directory one the
*game* already owns (`<Game>_Data/`)? Ask `adapter.GameOwnsDir` rather than
writing your own probe. It is read-only, it is the same copy core's
verify repairs use, and it is tested once, in `internal/adapter`.

`Layout` is one answer. **The zero `Layout` is the identity** — `Applies()`
is false and `Rewrite()` returns its argument — so core holds one
unconditionally instead of branching at every member:

```go
layout := adapter.NewLayout("game-root-relative", map[string]string{
    "MyPack/BepInEx/plugins/Foo.dll": "BepInEx/plugins/Foo.dll", // moved
    "MyPack/manifest.json":           "",                        // dropped
    // a member absent from the table keeps its own name
})
layout.Warnings = append(layout.Warnings, "…")  // surfaced verbatim; an adapter warns, it never guesses
```

Destinations are canonicalised (`path.Clean`) at this single point, so the
destination an adapter gets back is the destination core writes. Core
refuses a destination that is unusable, escapes the cache entry, collides
with another member's, is another member's *source*, or lands in lmm's
reserved `.lmm-` namespace — before it performs a single rename, so a
refused layout leaves the staging tree byte-identical.

**The only error `NormalizeArchive` should return is a refusal.**
`adapter.ErrNotAMod` is the shipped one: "this archive is the loader, the
framework or the base game, not a mod for it." An archive the rules simply
do not recognise is a `Warning` on an identity `Layout`, never a failure —
the caller holds the result unconditionally.

## Optional capabilities

Core type-asserts for each of these, the same idiom `internal/source`
already uses for `CapabilityReporter` and `WorkshopScanner`, so no adapter
pays for a capability it does not have and core never imports a concrete
adapter.

| Interface | Method | Implemented by | What core does with it |
| --- | --- | --- | --- |
| `FileRouter` | `RouteFile(g, rel) FileRoute` | `bepinex` | decides link / copy-once / skip, per deployable file |
| `ArchiveClaimer` | `ClaimArchive(members) (Claim, error)` | `bepinex` | refuses an archive that is unmistakably ANOTHER kind of game's |
| `Verifier` | `Verify(ctx, req) ([]Finding, error)` | `bepinex` | appends adapter findings to `lmm verify`'s result |
| `Guide` | `Guidance(g) []GuidanceNote` | `bepinex` | *(not rendered yet — see below)* |
| `MergeCompiler` | 11 methods | `icarus` | the whole merged-artifact compile path |
| `Preconditioner` | `CheckPreconditions(g, mods) error` | *(none yet)* | refuses a flow before it starts |

### FileRouter

`FileRoute` is how "this file is not ordinary mod content" is said.

```go
const (
    RouteLink     FileRoute = iota // the default: the linker deploys it
    RouteCopyOnce                  // a real file, copied on first deploy, never overwritten
    RouteSkip                      // ingested and cached, never deployed
)
```

`RouteCopyOnce` is not a new mechanism — it is the semantics
`applyProfileOverrides` already had, for an identical reason. BepInEx
generates its plugin configs on first run and the user hand-edits them
afterwards, so a mod shipping one is seeding a *default*: deploying it as a
symlink would push the user's edit back into the shared cache entry, where
the next re-download destroys it and every other profile using that entry
inherits it.

A copy-once member's full standing, which core enforces structurally rather
than by convention:

- written once, on the first deploy, by whichever path deploys the mod —
  install, import, update, rollback, profile switch, profile apply, or
  `verify --fix`'s re-deploy;
- never overwritten, including by a later version's own default;
- never entered into `deployed_files`;
- never removed by an uninstall, a replace's obsolete-file sweep, a profile
  switch or a purge.

An adapter with no `FileRouter` routes everything `RouteLink`, which is
today's behaviour exactly.

### ArchiveClaimer

This is the one capability core asks of an adapter that is **not** the
game's own, and it exists for one shipped refusal: importing a BepInEx
plugin into a game with no BepInEx deploys an assembly nothing will ever
load and reports success. The game's own adapter cannot catch that — it is
the identity, and the identity has no opinion — so core asks every *other*
registered adapter.

```go
type Claim struct {
    Evidence string // the shape, in the adapter's own words: "game-root-relative"
    Requires string // the domain loader kind the game must declare, or ""
}
```

**Unmistakable is the whole bar, and it is narrower than
`NormalizeArchive`'s.** An adapter asked about someone else's game may only
claim a shape no other game's mod plausibly has. `plugins/` at an archive
root is an ordinary directory name; claiming it would tell a 7 Days to Die
user to install a loader they do not need. A directory literally named
`BepInEx` is the other kind of evidence. Return a non-nil error (wrapping
`ErrNotAMod`) instead of a claim when the archive is the framework itself:
that user's problem is different in kind, because no amount of configuring
their game makes it installable as a mod.

When more than one adapter answers, `Registry.ClaimArchive` asks every one
of them and a **refusal beats every claim**, whichever adapter sorts first.
Among claims (and among refusals) the first in registered-name order wins,
so a build shipping two claimers answers deterministically.

### Verifier

An adapter's `Verify` is **read-only** — it must not write to the game
directory — and its `Finding` maps field-for-field onto core's
`VerifyFinding`:

```go
type Finding struct {
    Status              string // "loader_missing", …
    Note                string
    Recorded, Effective string          // the two halves of a DRIFT report
    Severity            Severity        // zero value is SeverityIssue
    Fixable             bool
    FixableReason       string
}
```

`Severity` is the one thing core cannot read off the finding itself: the
Issues/Warnings counters decide `lmm verify`'s exit code, and only the
adapter knows whether "the loader has never run" is a problem or a remark.
The zero value is `SeverityIssue`, because a report an adapter bothered to
make is a problem by default.

**No adapter finding is fixable, and that is the evidence rather than a
shortcut.** Of BepInEx's checks, the only repairable one — "every enabled
plugin is linked" — is not an adapter check at all: core's own per-file
pass and `verify_repair.go` already own it. What the adapter adds is
precisely the set core cannot see (a preloader present, declared-version
drift, bootstrap files matching the declared mode), and each of those is
honestly an instruction to the user, carrying a `FixableReason` that says
so.

### Guide

`Guidance(g) []GuidanceNote` returns `{Title, Body}` pairs — the setup
advice a game still needs. Keep them *specific to what is missing*: the
BepInEx adapter reports "not installed", "installed but not declared" and
"installed but has never run", and says nothing at all about a game that is
correctly set up.

It deliberately does **not** repeat the exact Steam launch option. That
string is computed once, for every game, by core's `LoaderStatus` — which
resolves the effective bootstrap from the declaration or from the install
directory's own Unity markers — and a second copy of a string whose wrong
value leaves a game that launches perfectly and loads nothing is not worth
the duplication. The note names `lmm game show <game>` instead.

**No frontend renders `Guidance` yet.** The capability and its `bepinex`
implementation exist, but no core flow asks for them, so nothing an adapter
returns here reaches a user today. BepInEx's own setup advice reaches them
through `LoaderStatus` instead — `lmm game show`'s loader section and the
web UI's loader panel — which already covers the same three states. Wiring
`Guidance` into `lmm game list --json`, `lmm verify` and the web game card
is the design's frontend pass (§5 of the design document) and has to land
in both frontends at once.

### MergeCompiler

The 11-method contract a game whose mods must merge into ONE profile-level
artifact implements (`icarus`): the merge operations (`ValidateSource`,
`MergeCompile`) plus the format vocabulary core needs to orchestrate them
without knowing the game's artifact format — where the base artifact lives,
how to fingerprint it, which files are the native merge format, which are
convertible raw artifacts, what the merged output is called. See
`internal/adapter/adapter.go` for the full declaration; a second
compile-mode game is a new package plus one registration line, and
`internal/core` does not change.

### Preconditioner

`CheckPreconditions(g, mods) error` refuses a flow before it starts — a
loader that has to be installed first, a game directory that is not what the
adapter expects. Core calls it wherever a Plan acquires its freshness
precondition, so every Plan that deploys is gated by it and none can be
forgotten, and wraps a refusal into `core.AdapterPreconditionError` (which
carries the remedy to both frontends through the `--json` error envelope's
`Details()`). The two removal flows, `lmm purge` and `lmm uninstall`, are
never gated — not by a precondition, and not by an adapter that will not
resolve: they remove what lmm recorded deploying, and they are the first
step out of every configuration an adapter refuses.
Nothing ships an implementation yet; it is the seam for the day something
needs to refuse a flow on facts about the mods it is about to act on, rather
than about one archive.

## Composition with the rest of `games.yaml`

### `deploy_mode`

Orthogonal for `deploy_mode`'s two ordinary values: `extract` and `copy`
describe how a *downloaded file* is handled, and any adapter composes with
either. `compile` is different — whether a game's mods merge into one
artifact is a property of the game — so a `deploy_mode: compile` game with
no `adapter:` key derives `adapter: icarus`, in memory, and an explicit
adapter that cannot compile is refused by name. `deploy_mode: compile` is
**deprecated** in favour of the adapter and will be removed in a future
MAJOR release.

### `loader:`

`loader:` stays a property of the game, not of the adapter: it describes the
**installation** — which loader build is present, Mono or IL2CPP, native or
Proton — and it is populated by detection and checked by verify, which are
facts that outlive any adapter decision. The adapter is what makes the block
*meaningful*: only `bepinex` reads it.

A game that deploys into its game root and declares `loader: kind: bepinex`
resolves to the `bepinex` adapter on its own, and so does one that merely
*has* BepInEx installed in its directory. Those are two halves of one fact
and a user has typically supplied only one of them: the declaration is a
statement of intent lmm asks for, while the preloader on disk is a fact lmm
can read. "Has BepInEx" means the same declared-or-installed test
everywhere lmm asks it, including the loader requirement a source or an
archive claim makes: BepInEx installed in the game directory satisfies it
exactly as a declaration does.

A game can still have BepInEx and resolve elsewhere: an explicit
`adapter:` always wins, `deploy_mode: compile` selects `icarus`, and a
`mod_path` off the game root keeps `generic-files` (below). Such a game gets
none of the BepInEx rules — a package's `manifest.json` is deployed with the
mod, a plugin is not moved under `BepInEx/`, and a `BepInEx/config` file is
linked from the shared mod cache — so lmm says so. Where it says it follows
one rule: **a warning must be silenceable by the fix it suggests, and a
persistent warning is for a contradiction, not for a deliberate choice.**

- **Every such game**, on the import plan or download of an archive the
  `bepinex` rules would have laid out — where the harm happens. That
  warning offers one remedy, the change that makes lmm lay such an archive
  out, because nothing else silences it.
- **A contradiction**, persistently: when lmm loads `games.yaml`, in `lmm
  game show`'s loader report and the web loader panel, as a
  `loader_adapter_ignored` WARNING in `lmm verify` (which the web Health
  count includes), and right after the `lmm game edit` or `lmm game add`
  that creates it. A contradiction is a `loader:` block the adapter
  ignores, or an installed BepInEx that an adapter **nobody chose** ignores
  (`deploy_mode: compile`, or a `mod_path` off the game root). That
  warning offers both ways out: lay BepInEx archives out, or state the
  choice.
- **A stated choice**, never persistently. `adapter: generic-files` (or any
  explicit adapter) with **no** `loader:` block on a game whose BepInEx is
  merely installed is a legitimate "treat this game's archives as plain
  files": the explicit key is the acknowledgement. It gets the per-archive
  warning and nothing else — no load-time line, no loader-panel line, and
  no verify row at all (not even a note, since the Health card lists every
  row that is not OK).

Every remedy either warning offers is one that, applied, silences it. A
command that reports a game's warning itself (`lmm game show`, `lmm
verify`, `lmm game edit`) prints it once, not once at load and again in its
own output. A game lmm refuses to run any flow on (an unknown adapter, a
compile game on an adapter that cannot compile) gets no such warning:
lmm deploys nothing there, and the refusal names the fix.

### `mod_path`

The user and the curated known-games entry own it; no adapter rewrites it.
But one adapter depends on it. Every path `bepinex` produces
(`BepInEx/plugins/…`, `BepInEx/config/…`) is relative to the **game root**,
so it is only right for a game whose `mod_path` IS its `install_path` —
the same directory, however the two are spelled (a trailing slash, `~`, a
relative `mod_path`, or a symlink such as Steam's `~/.steam/steam` all
count) — and never `mod_path: ""`, which is joined verbatim and deploys
relative to the working directory. So:

- bepinex is **derived** only for a game-root `mod_path`. A game that
  predates lmm's loader support and points `mod_path` at
  `<install>/BepInEx/plugins` keeps `generic-files`, and its archives keep
  deploying into that directory exactly as packaged. Deriving bepinex there
  nested every plugin under `BepInEx/plugins/BepInEx/plugins/`, where
  nothing loads it. That game is a contradiction in the sense above, with
  both remedies: move it to the game root (purge, set `mod_path`,
  re-deploy, and `lmm verify --fix` re-lays out what was already
  imported), or pin `adapter: generic-files`;
- an **explicit** `adapter: bepinex` with any other `mod_path` is refused
  by name, like a compile game's non-compiling adapter, and `lmm game
  edit`/`lmm game add` cannot write it;
- `lmm game add` with a BepInEx loader or adapter — or, naming no other
  adapter, on an install directory that already holds BepInEx — defaults
  `mod_path` to the install path rather than `<install>/mods`, with
  `--from-detected` and `POST /api/v1/games` too; every curated BepInEx entry
  uses the game root, and the known-games ratchet refuses one that does
  not.

## Adding an adapter

The walkthrough, in the order the existing two were built.

**1. Create the package.** `internal/adapter/<name>/`, one file per
capability. It may import `internal/domain` and `internal/adapter` — and
nothing else in the module. `internal/adapter/boundary_test.go` enforces
that with a live `go list`, and asserts that `internal/core` appears in no
adapter's imports; add your package to its `adapterPackages` list, or
`TestEveryAdapterPackageIsChecked` fails.

**2. Implement the three required methods**, and add a compile-time
assertion for every capability you mean to have:

```go
var (
    _ adapter.GameAdapter = (*Adapter)(nil)
    _ adapter.FileRouter  = (*Adapter)(nil)
)
```

Core type-asserts, so a method renamed out of an interface is otherwise
invisible — it simply stops being asked.

**3. Test it as a table.** `NormalizeArchive` takes a member list and
returns a `Layout`, so the whole rule surface is testable with no
filesystem: name the exact destination each member takes, because the cache
entry's layout *is* the game directory's layout and a wrong prefix is a mod
the game never sees. Add the package's own `doc_comment_test.go` (copy
`internal/adapter/bepinex/doc_comment_test.go`) — every exported identifier
needs a doc comment.

**4. Register it.** One line in `internal/app/adapters.go`:

```go
func registerAdapters(svc *core.Service) {
    svc.RegisterAdapter(icarus.New())
    svc.RegisterAdapter(bepinex.New())
    svc.RegisterAdapter(yourgame.New())
}
```

That is the *only* place a concrete adapter is named. Core resolves through
the registry and never imports one; `cmd/lmm` validates `--adapter` against
`Service.ListAdapters()`, so its own import allow-list does not grow either.

**5. Decide whether a derivation is warranted.** `Service.AdapterName`
carries one per adapter that needs to keep an existing `games.yaml` working
untouched (`deploy_mode: compile` ⇒ `icarus`; a game with BepInEx ⇒
`bepinex`). A derivation is in memory only — `games.yaml` is never rewritten
— and it fires only for an adapter that is actually registered, so a build
without yours behaves exactly as before. A **new** adapter with no installed
base usually needs none: users opt in with the key.

**6. Document and record it.** Add a row to [What ships
today](#what-ships-today) and to `docs/configuration.md`'s adapter list, and
a `CHANGELOG.md` entry under `[Unreleased]`.

### What stays in core

Two things you might expect to write in an adapter, which are deliberately
not yours:

- **Anything that writes.** The tree rewriter, the file copies, the
  repairs. Return a table; core executes it.
- **`--fix` repairs.** Report the finding; if a repair is genuinely
  possible, it belongs beside the ones core already owns, so that the
  profile's link method and the deployed-files bookkeeping stay the deploy
  path's own.
