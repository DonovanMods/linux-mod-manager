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
| `d2r` | `adapter: d2r` only, which Battle.net detection's Diablo II: Resurrected entry sets (#546) | Lays every archive out as `<name>/<name>.mpq/` under the game's `mods` folder, and names the `-mod <name> -txt` the deployed profile needs (`LaunchAdvisor`). |
| `bethesda` | `adapter: bethesda` only, which Steam detection sets for Skyrim Special Edition/Anniversary Edition, Skyrim VR, Fallout 4, Fallout 4 VR, Starfield and Skyrim (#606) | Lays a game-root-relative archive (a `Data/` folder, SKSE's version-named wrapper) out relative to `Data/`, and carries each game's plugin rules (`PluginOrderer`, which core sorts and writes `Plugins.txt` by: #616) and game-root file table (`RootRouter`, which core deploys by: #629). |
| `witcher3`, `baldurs-gate-3` | `adapter: witcher3` / `adapter: baldurs-gate-3` only, which Steam detection sets for The Witcher 3 and Baldur's Gate 3 (#635) | A table of the Vortex mod types a NexusMods collection states for the game and the root each deploys to (`RootRouter`); otherwise the identity. One package, `internal/adapter/modtypes`. |

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
| `Verifier` | `Verify(ctx, req) ([]Finding, error)` | `bepinex`, `bethesda` | appends adapter findings to `lmm verify`'s result |
| `MergeCompiler` | 11 methods | `icarus` | the whole merged-artifact compile path |
| `Preconditioner` | `CheckPreconditions(g, mods) error` | *(none yet)* | refuses a flow before it starts |
| `LaunchAdvisor` | `LaunchAdvice(g, mods, previous) Advice` | `d2r` | shows the advice as a warning after a deploy (a switch's and an apply's too) and on a launch |
| `PluginOrderer` | `PluginRules(g) PluginSpec` | `bethesda` | the plugin-list sync after every deploying flow (`syncMergedPak`), verify's plugin tier, and purge's restore (#616) |
| `RootRouter` | `RouteRoots(g, files) []DeployRoot`, `RootForModType(g, type) (DeployRoot, bool)` | `bethesda`, `witcher3`, `baldurs-gate-3` | deploys a root-routed file under `install_path`, and every removal, verify, purge and conflict report finds it there (#629) |
| `LayoutNeutral` | `LayoutNeutral()` (a marker) | `generic-files`, `witcher3`, `baldurs-gate-3` | treats a switch between two layout-neutral adapters as moving nothing: no reinstall warning on `lmm game edit --adapter`, the game page or verify's `adapter_suggested` note (#635) |
| `PrefixConsumer` | `UsesPrefix(g) bool` | `bethesda` | reports a prefix it cannot resolve as a `prefix_unresolved` finding (`lmm verify`, `lmm game show`) |

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

### Setup advice is not a capability

A game's loader setup advice — install the loader, declare it, set the
Steam launch option, check that it ran — is core's `LoaderStatus`
(`lmm game show`'s loader section, the web UI's loader panel,
`GET /api/v1/games/{id}`), not an adapter capability. `LoaderStatus`
resolves the game's effective bootstrap from the declaration or from the
install directory's own Unity markers, so it is the one place the exact
launch option is computed, and its `warnings` name what is still missing
for a BepInEx game.

An earlier draft of the seam carried a separate guidance capability
(`Guidance(g)`) that the `bepinex` adapter implemented and nothing
rendered; it was dropped before 2.0 (#443), with every sentence it held
folded into `LoaderStatus`, since no second loader adapter is planned for
2.0. A future adapter with advice of its own should extend `LoaderStatus`'s
answer rather than add a parallel channel.

### LaunchAdvisor

What the user must do outside lmm for the DEPLOYED mods to load, when lmm
cannot arrange it itself (#546). Diablo II: Resurrected loads a mod only
when started with `-mod <name> -txt`, Battle.net's `--exec` cannot pass
game arguments on, and the name depends on which mods the profile has
deployed and in what order. So `d2r`'s advice names the folder of the mod
last in load order, says when mods in other folders compete (the game
loads one), and when several mods share that folder (merged, loaded
together).

Core hands it the profile's enabled, deployed mods in load order, each
with its deployable files (mod-path-relative, slash-separated), and
`previous`: the `Advice.Args` it last returned for the game, which core
records under `<DataDir>/launch-args/<game-id>` after every deploy. That
is how a deploy that drops the last mod can say to remove the `-mod` line.
Core appends `Advice.Lines` to `DeployResult.Warnings` (with a
`DeployWarning` event), to `SwitchResult.Warnings` and
`ProfileApplyResult.Warnings` (with a `SwitchInstallWarning` event), so
both frontends show it, and to `LaunchResult.Warnings`; a launch reads the
record but never writes it. Advice never fails a flow: anything core
cannot read is skipped.

This is not the setup advice the section above rules out. That advice is
about the game's INSTALLATION (is the loader there, which launch option
does it need), which `LoaderStatus` computes once per game. This advice is
about the PROFILE: it changes with every deploy, so it is said where the
profile's mods change and where the game starts.

### PrefixConsumer

`UsesPrefix(g) bool` says the game keeps state lmm writes inside its Wine
or Proton prefix rather than its install directory (#605) — a Bethesda
game's `Plugins.txt`, under `drive_c/users/<user>/AppData/Local`. Core
resolves the prefix itself (`prefix:` in `games.yaml`, else Steam's
compatdata in any library; see
[configuration.md](configuration.md#prefix-gamesyaml)); the adapter only
answers yes or no. A prefix lmm cannot resolve is a `prefix_unresolved`
finding for a game whose adapter answers yes, and nothing at all for any
other game.

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
precondition, and before every deploy that has no Plan — `lmm mod enable`,
the `lmm verify --fix` repairs that re-link or re-deploy, and the
merged-artifact rebuild a mutation ends with — so every flow that deploys
is gated by it and none can be forgotten: a ratchet fails the build for a
deploy path without the check, and an Installer built for a game whose
adapter does not resolve deploys nothing at all. It wraps a refusal into
`core.AdapterPreconditionError` (which carries the remedy to both frontends
through the `--json` error envelope's `Details()`). The two removal flows, `lmm purge` and `lmm uninstall`, are
never gated — not by a precondition, and not by an adapter that will not
resolve: they remove what lmm recorded deploying, and they are the first
step out of every configuration an adapter refuses. Where the adapter will
not resolve, they remove ONLY what a deployment record names — a path the
cache entry merely lists, such as a seeded config the user has since
replaced with a link of their own, stays — and a name that still resolves
(an adapter refused only for `deploy_mode: compile`) still routes the
removal, so its never-remove rules hold. Their `--dry-run` names exactly
the files and the merged artifact the real run removes.
Nothing ships an implementation yet; it is the seam for the day something
needs to refuse a flow on facts about the mods it is about to act on, rather
than about one archive.

### PluginOrderer

A Bethesda game loads plugin files (`.esm`, `.esp`, `.esl`) in an order the
user controls, from an active-plugin list in the game's Wine prefix. That
order is a second axis beside lmm's mod order (design §5,
[2026-11-10-collections-design.md](plans/2026-11-10-collections-design.md)).
`PluginRules(g)` answers, as pure data, everything about that list that is a
fact about the game rather than about the profile: the list's kind
(`ListAsterisk`: one `Plugins.txt`, `*` for active; `ListTextfile`:
`Plugins.txt` plus `loadorder.txt`), the AppData folder candidates (with the
install-directory file that selects a GOG or Epic variant), the plugin
extensions, the header flag bits, the extensions that imply a master or a
light plugin, the implicit plugins, the Creation Club files in precedence
order, extra plugin directories, and the active limits.

An adapter that does not recognise the game answers the zero `PluginSpec`,
and `adapter.PluginRules` reports that as "no rules": an adapter never
guesses which game it is looking at. `bethesda` recognises a game by its
`catalog:` entry (the known-games catalog entry games.yaml says it is; see
[configuration.md](configuration.md#catalog-gamesyaml)), else by the Steam
app id games.yaml states (`launch.steam_app_id`, or a numeric
`steamworkshop` mapping), else by its game id (`skyrim-se`, `skyrim-vr`,
`fallout4`, `fallout4-vr`, `starfield`, `skyrim`); never by its NexusMods
domain, which Skyrim VR and Fallout 4 VR share with Skyrim SE and Fallout 4.
A game it cannot identify gets a `bethesda_game_unknown` verify note.

Core reads the plugin headers itself (`core.ReadPluginHeader`, a
standard-library TES4 reader) and applies the spec to one with
`core.ClassifyPlugin`. With them, core sorts the deployed plugins with the
profile's plugin list and rules (`internal/core/plugins_sort.go`) and writes
the list into the prefix after every deploying flow
(`internal/core/plugins_sync.go`, #616).

### RootRouter

A Bethesda game's `mod_path` is `Data/`, but some mods belong beside the
game's executable: a script extender's loader and DLLs, an ENB. `FileRoute`
is unchanged; the answer comes from a separate capability, a `DeployRoot`
per file (`RootModPath`, the zero value, or `RootInstallPath`).

- `RouteRoots(g, files)` answers for every file of ONE mod's cache entry at
  once, because some rules are about the whole mod: an ENB preset is
  recognised by its top-level `enbseries.ini`, and then all of it belongs at
  the game root.
- `RootForModType(g, type)` answers for a mod whose kind was stated rather
  than detected: a Vortex mod type, as a NexusMods collection records it.
  `bethesda` maps `""` (Vortex's default) to `mod_path`, and `dinput` (the
  type Vortex's script-extender installer sets) and `enb` to `install_path`,
  for every game it recognises; a type one game's own Vortex extension
  registers is known for that game alone, from its row in the games table
  (Starfield's `starfield-data-folder`, #635). The `modtypes` adapters
  answer from one table per game (see below).

**Core honours it (#629).** A file routed to `RootInstallPath` deploys
under the game's `install_path`, relative to it. Core names every deployed
file by a `domain.DeployPath` - its root and its path relative to that root
- so every flow that keeps files by path (deploy, install/update/rollback,
uninstall, disable, purge, converge, verify and `--fix`, conflicts,
snapshots, switch/apply, and #630's overridden copies) finds it under its
own root, and plan/apply matching compares roots, never display strings.
`deployed_files` records the root in a `root` column (schema v26, CHECKed
to `mod_path`/`install_path`). Wire documents that report a deployed file
as an object carry an additive `root` field (`"install_path"`); frontends
show such a file as `<game root>/d3d11.dll`. Writes and removals under the
game root, and the restore of an original there, go through an
openat/no-follow walk beneath `install_path` (`rootLinker`) that refuses any
folder on the way that is a link; a stock file a game-root file replaces is
captured under the originals store's `install_path` root and put back when
lmm's file goes. A game whose `mod_path` is its `install_path` has one
directory, so the answer changes nothing there. A root-routed member that
lands inside the `mod_path` - a whole-mod route's `Data/` folder, matched
case-insensitively - is that `mod_path` file, deployed from its own cache
member (`Data/Weather.esp` becomes `Weather.esp` under `Data/`). The game
root's routing applies at deploy time, so after `adapter: bethesda` is set
the next deploy moves an installed mod's game-root files there.

`RootForModType` is honoured by `deployItemsForType` for a caller that
states a mod type: the whole mod goes to the root the adapter names, and a
type it does not know is a `*core.ModTypeUnsupportedError`, never
`mod_path`. The type is the profile reference's `mod_type`, copied onto the
installed row (#607), and the collections converter's gate asks the same
question before an import: a `mod_type:<type>` feature is supported for a
game exactly when its adapter knows the type.

A type is only ever routed to one of the two roots. A type that deploys
somewhere else (Witcher 3's `witcher3dlc`, under the game's `DLC/`) is left
out of every table, so it stays refused, rather than approximated.

### The `bethesda` adapter's facts, and where each comes from

libloadorder, esplugin, libloot and Vortex are GPL; lmm is MIT. They were
read for **facts** (file names, flag bits, implicit plugin lists, limits);
no code was copied. A value no readable source confirmed is left out.

| Fact | Value | Source |
| --- | --- | --- |
| List kind | asterisk: SSE/AE, Skyrim VR, Fallout 4, Fallout 4 VR, Starfield; textfile: Skyrim | libloadorder `src/game_settings.rs` (`load_order_method`) |
| List file | `Plugins.txt` (the games read the capitalised name) | libloadorder `plugins_file_path` |
| Order file | `loadorder.txt`, beside `Plugins.txt`, UTF-8 without a BOM (Skyrim only) | libloadorder `doc/src/textfile_standard.md` |
| AppData folders | `Skyrim Special Edition` (`… GOG` when `Galaxy64.dll`, `… EPIC` when `EOSSDK-Win64-Shipping.dll` is in the game directory), `Skyrim VR`, `Fallout4` (`Fallout4 EPIC`), `Fallout4VR`, `Starfield`, `Skyrim` | libloadorder `appdata_folder_name` |
| Implicit plugins | SSE: `Skyrim.esm`, `Update.esm`, `Dawnguard.esm`, `HearthFires.esm`, `Dragonborn.esm`; VR adds `SkyrimVR.esm`; FO4: `Fallout4.esm`, `DLCRobot.esm`, `DLCworkshop01.esm`, `DLCCoast.esm`, `DLCworkshop02.esm`, `DLCworkshop03.esm`, `DLCNukaWorld.esm`, `DLCUltraHighResolution.esm`; FO4 VR: `Fallout4.esm`, `Fallout4_VR.esm`; Starfield: `Starfield.esm`, `Constellation.esm`, `OldMars.esm`, `ShatteredSpace.esm`, `SFBGS003/004/006/007/008/00D/047/050.esm`; Skyrim: `Skyrim.esm` | libloadorder `*_HARDCODED_PLUGINS` |
| Implicitly active, no fixed position | Skyrim: `Update.esm` | libloadorder `implicitly_active_plugins` |
| CCC files, in precedence order | `Skyrim.ccc`, `Fallout4.ccc` in the game directory; Starfield: `My Games/Starfield/Starfield.ccc`, then the game directory's (the first that exists wins, even when empty) | libloadorder `ccc_file_paths` |
| Extra plugin directory | Starfield: `My Games/Starfield/Data` | libloadorder `additional_plugins_directories` |
| Header | TES4 record, 24-byte header; `MAST` subrecords name the masters | esplugin `src/record.rs`, `src/plugin.rs` |
| Master flag | `0x1`; `.esm`/`.esl` imply master for SSE, FO4 and Starfield only | esplugin `is_master_file` |
| Light flag | `0x200` (SSE, FO4 and their VR editions), `0x100` (Starfield); `.esl` implies light | esplugin `is_light_flag_set`; libloadorder `src/enums.rs` (VR read as SSE/FO4) |
| Starfield medium / update / blueprint flags | `0x400` / `0x200` / `0x800`; the update flag stops `.esl` implying light | esplugin `is_medium_flag_set`, `is_update_flag_set`, `is_blueprint_plugin` |
| Delisted by the game | Starfield: blueprint plugins and names starting `BlueprintShips-` | libloadorder `src/load_order/asterisk_based.rs` |
| Limits | 255 full (one fewer for each of the light and medium classes in use), 4096 light, 256 medium (Starfield) | libloadorder `doc/src/active_plugins.md`, `src/load_order/writable.rs`, `src/load_order/mutable.rs` |
| Script extender files at the game root | top-level `.dll`/`.exe` named `skse64_*`, `skse_*`, `sksevr_*`, `f4se_*`, `f4sevr_*`, `sfse_*` | each extender's readme (skse.silverlock.org; github.com/ianpatt/f4se, /sfse; github.com/Neanka/f4sevr); SKSEVR's build targets (github.com/Odie/sksevr-mirror) |
| ENB and dinput8 at the game root | a top-level `enbseries.ini` or `dinput8.dll` takes the whole mod; `d3d11.dll` and `enbseries/` on their own | Vortex `extensions/modtype-enb`, `extensions/modtype-dinput`; the collections design §4 |
| Vortex mod types | `""` deploys to `Data`, `dinput` and `enb` to the game directory | Vortex `extensions/games/game-skyrimse` (`queryModPath`), `modtype-dinput`, `modtype-enb`, `script-extender-installer` |
| Starfield's own mod type | `starfield-data-folder` ("Data Folder") deploys to the game directory's `Data/`, Starfield's `mod_path`; registered for Starfield alone. (Vortex's default type for Starfield is the game directory, `queryModPath: "."`; lmm's `""` routes file by file, as for every Bethesda game.) | github.com/Nexus-Mods/game-starfield at 3d3332d: `src/index.ts:159` (`queryModPath`), `:277-284` (`registerModType`), `src/modTypes/dataPath.ts:6-13` (`getDataPath`), `src/common.ts:25` |
| Steam app ids | 489830, 611670, 377160, 611660, 1716740, 72850 | store.steampowered.com `appdetails` |
| NexusMods domains | the VR editions have none of their own (`skyrimspecialedition`, `fallout4`) | the NexusMods `v1/games.json` list |

Left out: the Microsoft Store variants (they do not run on Linux), Enderal,
the INI `sTestFile` overrides, and `d3dcompiler_46e.dll` / `enblocal.ini` as
ENB files on their own (enbdev.com, the only source, refuses scripted
readers; an ENB preset's `enbseries.ini` still carries them to the game
root).

### The `witcher3` and `baldurs-gate-3` adapters' facts (#635)

Vortex's game extensions are GPL; they were read for which directory each
type deploys to, and no code was copied. Sources: `[VXG]`
github.com/Nexus-Mods/vortex-games at 18bbee9; `[VX]`
github.com/Nexus-Mods/Vortex at 88dd2f6.

| Game | Type | Root | Source |
| --- | --- | --- | --- |
| The Witcher 3 | `witcher3tl` | `install_path` | `[VXG]` `game-witcher3/index.ts:168` (`registerModType('witcher3tl', …, getTLPath)`), `util.ts:66-72` (`getTLPath`: the discovery path) |
| The Witcher 3 | `dinput` | `install_path` | `[VX]` `extensions/modtype-dinput/src/index.ts:55-63, 91` (every game but Factorio and Microsoft Flight Simulator) |
| Baldur's Gate 3 | `bg3-loose` | `mod_path` (the game's `Data/`) | `[VXG]` `game-baldursgate3/index.tsx:289-292` (`registerModType(MOD_TYPE_LOOSE, …, getGameDataPath)`), `util.ts:17-25`, `common.ts:85` |

Neither table lists the default type (`""`): core never asks for it, since
an untyped mod routes file by file, and these adapters send every untyped
file to the `mod_path`. For The Witcher 3 that matches Vortex, whose default
type deploys to the game's `Mods/` (`[VXG]` `game-witcher3/index.ts:141`),
the catalog's `mod_path`.

A `witcher3tl` mod is laid out from the game directory: its `mods/` folder
is the `mod_path` (matched case-insensitively, as `Data/` is), its `bin/`
folder deploys beside it. The recipes' file lists (`hashes`) confirm it
(`mods\modSharedImports\content\...`, `bin\config\r4game\...`).

Left out, so a collection that needs them is still refused:

| Type | Why | Source |
| --- | --- | --- |
| `witcher3dlc` | deploys under the game's `DLC/` folder, which is neither of lmm's two roots (the recipes' `hashes` are relative to it: `dlcTrueFires\content\...`) | `[VXG]` `game-witcher3/index.ts:169`, `util.ts:58-64` (`getDLCPath`) |
| `witcher3menumodroot` | Vortex merges the menu XML files a mod ships that are named like the game's own (`audio`, `gameplay`, `input`, …) into the game's file; lmm would replace the game's file instead | `[VXG]` `game-witcher3/index.ts:167, 174`, `mergers.ts:29-106`, `common.ts:136-138` |
| `SMAPI` (Stardew Valley) | not a route at all: SMAPI's Vortex installer unpacks the platform's `install.dat` from inside the download and generates `StardewModdingAPI.deps.json`, which no deploy of the downloaded archive can do. The collections converter lists it as a prerequisite step instead (its page and SMAPI's own installer, `internal/core/collection_prerequisites.go`) | `[VXG]` `game-stardewvalley/index.ts:445-450, 462-538, 754`; SMAPI's `src/SMAPI.Installer/assets/README.txt` (github.com/Pathoschild/SMAPI at e5b8bf3) |

**Baldur's Gate 3's default type.** Vortex deploys a BG3 mod of the default
type (`.pak` files) to `%LOCALAPPDATA%/Larian Studios/Baldur's Gate 3/Mods`
(`[VXG]` `game-baldursgate3/index.tsx:236`, `util.ts:27-33`: its
`documentsPath()` is Vortex's `localAppData` path, despite the name), but
lmm's catalog `mod_path` for the game is `Data`. This story leaves the
catalog as it is; a collection's untyped `.pak` mods still deploy under
`Data/`, where the game does not read them (tracked as a separate issue).

**Wrapper folders.** These adapters lay archives out as packaged. Vortex's
installers strip a folder that wraps the content: `witcher3tl`'s
(`installTL`) drops whatever comes before the archive's first `mods/`
folder (`[VXG]` `game-witcher3/installers.ts:195-215`), and the installer
behind most `bg3-loose` mods (`installReplacer`) lays files out relative to
the folder holding `Public/` or `Generated/`, or to a `Data/` folder
(`[VXG]` `game-baldursgate3/installers.ts:221-257`). An archive packed
inside such a wrapper (`MyMod-1.2/mods/modMyMod/...`,
`MyMod/Data/Public/...`) deploys with the wrapper in its path, where the
game does not find it. Known limitation; most archives are packed without
one.

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
- **A contradiction**, persistently: when lmm loads `games.yaml` — as one
  short line naming the game, the contradiction and `lmm game show <id>`,
  since every command prints it until the configuration changes (#456) —
  in full in `lmm game show`'s loader report and the web loader panel, as a
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
  both remedies: move it to the game root (`lmm purge --profile <name>`
  for each profile with files deployed, then `lmm game edit <id>
  --mod-path <install path>`, then re-deploy, and `lmm verify --fix`
  re-lays out what was already imported), or pin
  `adapter: generic-files`;
- `lmm game edit --mod-path` (and `PUT /api/v1/games/{id}`'s `mod_path`)
  refuses to move a `mod_path` while any profile has files deployed under
  it — lmm records deployed files relative to it, so the purge comes
  first, and the refusal names every profile to purge (and, for a live
  file the active profile lists but only another profile records, the
  `lmm profile apply` or `lmm deploy` that records it first — or, when
  the active profile lists that mod at a version lmm can neither find in
  the cache nor download, the profile file to edit). `lmm game detect`'s
  repair of an already-configured game runs the same check. The edit also
  refuses a move that would leave an explicit `adapter: bepinex` off the
  game root, while a game already refused can be moved back; `--adapter`
  and `--mod-path` are checked together, so a game moves onto bepinex at
  its root, or off it, in one command;
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
the game never sees. Add the package's directory as a row in the doc-comment ratchet
(`internal/testutil/doccomment_test.go`) — every exported identifier needs
a doc comment.

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
