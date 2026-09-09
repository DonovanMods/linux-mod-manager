# BepInEx support — spike (#267)

**Date:** 2026-09-09 · **Issue:** [#267](https://github.com/DonovanMods/linux-mod-manager/issues/267)
**Status:** complete — **PARTIAL GO** (tiered; see [Verdict](#verdict))
**Scope:** research only. No code, no game was launched, and nothing was
installed into any real game directory. Evidence came from BepInEx's own
releases and docs, Thunderstore's public API, and archives downloaded to a
scratch directory and listed with `unzip -l`.

---

## TL;DR

BepInEx is a smaller change to lmm than it looks, and Thunderstore is a
bigger one than it looks.

The framework installs into the game **root**, which lmm can already express
— by setting the game's `mod_path` to its `install_path`, the same absolute
path twice — and the overwhelmingly common plugin archive is already laid
out **game-root-relative** (`BepInEx/plugins/Foo.dll`), so the existing
linker deploys it with no new deploy-rule type at all. What genuinely does
not exist yet is (a) a way to say "this mod requires the framework, at this
version, and the framework is not a mod you found on a source", (b) the
Linux bootstrap, which is a **launch-options** problem lmm cannot solve for
the user and should not pretend to, and (c) Thunderstore, whose only public
listing endpoint returns **the entire community index in one unpaginated
response — 3.8 MB on the wire for a small community, 34.6 MB for a large
one, decoding to 33 MB and 329 MB of JSON** — a real design problem, not a
plumbing detail.

Recommendation: **GO on Tiers 1–2** (deploy plugins into an
already-installed BepInEx; framework as a first-class per-game
prerequisite with `verify` coverage), **GO on Tier 3 behind a design
decision** (Thunderstore as a source), **NO-GO for now on Tier 4** (lmm
installing the framework and editing launch options).

---

## 1. Evidence

Everything in this section was observed directly on 2026-09-09.

### 1.1 What BepInEx actually ships

`BepInEx/BepInEx` latest release is **v5.4.23.5** (published 2026-02-08),
with per-platform archives: `BepInEx_win_x64`, `BepInEx_win_x86`,
`BepInEx_linux_x64`, `BepInEx_linux_x86`, `BepInEx_macos_universal`, all
~640 KB. The README's own compatibility table says **Unity Mono is supported
on Windows/macOS/Linux; Unity IL2CPP on Windows and Linux but not macOS or
ARM**, and that "currently only Unity Mono has stable releases" — IL2CPP
lives in the **BepInEx 6 bleeding-edge** builds at `builds.bepinex.dev`,
which are not GitHub releases.

`BepInEx_linux_x64_5.4.23.5.zip` contains, at its root:

```
.doorstop_version
changelog.txt
libdoorstop.so          31 KB
run_bepinex.sh          10.6 KB
BepInEx/core/*.dll      (BepInEx.dll, BepInEx.Preloader.dll, 0Harmony, Mono.Cecil, MonoMod…)
```

Note what is **not** there: no `BepInEx/plugins/`, no `BepInEx/config/` —
those are created on first run.

### 1.2 The two Linux bootstrap modes are genuinely different

- **Native Linux build** → `run_bepinex.sh`. Its own header documents two
  usages: `./run_bepinex.sh <path to game> [doorstop args] [game args]`, or
  edit `executable_name=` inside the script and run it bare. It sets up
  `LD_PRELOAD`/`LD_LIBRARY_PATH` for `libdoorstop.so`, and it carries an
  explicit **Steam special case**: "program is launched via Steam on Linux —
  in that case rerun the script via their bootstrapper to delay adding
  Doorstop to `LD_PRELOAD`", citing UnityDoorstop issue #88. The user's part
  is a Steam **launch-option** change (`./run_bepinex.sh %command%`).
- **Proton/Wine** → the Windows `winhttp.dll` proxy DLL plus
  `doorstop_config.ini`, and BepInEx's own Proton/Wine guide says the proxy
  "should be configured manually" — its recommended route is
  `protontricks --gui` → `winecfg` → Libraries → add a `winhttp` override.
  The community shorthand is a `WINEDLLOVERRIDES="winhttp=n,b" %command%`
  launch option instead.

Both are edits to state lmm does not own: Steam's `localconfig.vdf` launch
options, or a Proton prefix's `user.reg`. That is the single most important
finding in this document — see [§3](#3-bootstraplaunch-integration-question-3).

The `doorstop_config.ini` shipped in the Windows/Proton pack is an INI with
`enabled=true` and `target_assembly=BepInEx\core\BepInEx.Preloader.dll`
(backslashes — it is the Windows build), plus `[UnityMono]` and (in 6.x)
CoreCLR/IL2CPP sections. `run_bepinex.sh` exposes the same options as shell
variables, including `CoreCLR options (IL2CPP)`.

### 1.3 Archive layouts — three shapes, observed

Downloaded from Thunderstore and listed:

| Package | Root entries | Shape |
| --- | --- | --- |
| `RugbugRedfern/Skinwalkers` 5.0.0 | `BepInEx/plugins/SkinwalkerMod.dll`, `icon.png`, `manifest.json`, `README.md` | **A: game-root-relative** |
| `Sligili/More_Emotes` 1.3.3 | `BepInEx/plugins/MoreEmotes1.3.3.dll`, `BepInEx/plugins/MoreEmotes/…` | **A**, with an asset subdirectory |
| `Evaisa/HookGenPatcher` 0.0.5 | `patchers/BepInEx.MonoMod.HookGenPatcher/…`, `config/HookGenPatcher.cfg` | **B: BepInEx-relative** (missing the `BepInEx/` prefix) |
| `BepInEx/BepInExPack` 5.4.2305 | `BepInExPack/BepInEx/…`, `BepInExPack/winhttp.dll`, `BepInExPack/doorstop_config.ini` | **C: wrapper directory** |
| `denikson/BepInExPack_Valheim` | `BepInExPack_Valheim/BepInEx/…` | **C** |

Every package also carries `icon.png`, `manifest.json` and `README.md` at
the archive root — metadata, never deployable.

**Shape A is the common case and needs nothing new.** With the game's
`mod_path` pointed at its own install directory,
`BepInEx/plugins/Foo.dll` lands exactly where it belongs through the
existing linker:

```yaml
install_path: /home/you/.steam/steam/steamapps/common/Lethal Company
mod_path: /home/you/.steam/steam/steamapps/common/Lethal Company
```

**Not `mod_path: ""`.** An earlier draft of this document said that, and it
is wrong in a way that would ship a broken Tier 1. `mod_path` is never
joined with `install_path`: `internal/storage/config/games.go:111` takes
the YAML value verbatim (`ExpandPath(cfg.ModPath)`), and the deploy path is
`filepath.Join(game.ModPath, file)` (`internal/core/installer.go:70`) — so
an empty `mod_path` yields a RELATIVE path and deploys into the process's
working directory. Elsewhere an empty value is not "the root" either:
`scanModPath` refuses it outright (`internal/core/importer.go:571`), and
`game add`/`game detect` default it to `<install_path>/mods`
(`game_add.go:213`, `game_detect.go:94-98`). The conclusion survives — a
game-root deploy is expressible today and needs no new deploy-rule type —
but the mechanism is the install path, not the empty string. Tier 1 should
carry a test that a game-root `mod_path` round-trips through `games.yaml`.

Shape C is the same wrapper-strip problem lmm already
solved for `.EXMODZ` assets in #237. Shape B is the only one needing a new
rule, and it is a small one: an archive whose root is `plugins/`,
`patchers/`, `monomod/` or `config/` is BepInEx-relative and gets a
`BepInEx/` prefix.

**Note the Thunderstore BepInExPack is Windows-only** — `winhttp.dll` and a
Windows `doorstop_config.ini`, no `libdoorstop.so`, no `run_bepinex.sh`. It
is the right pack for a Proton game and the *wrong* one for a native Linux
build, which needs the GitHub `BepInEx_linux_x64` release. Any tooling that
treats "install BepInEx" as one action will get this wrong.

### 1.4 Thunderstore's public API

No authentication needed for anything below.

- `GET https://thunderstore.io/c/<community>/api/v1/package/` — the whole
  community index, unpaginated. Re-measured 2026-09-09 during the Track D1
  review, separating what crosses the wire from what it decodes to (an
  earlier draft of this document reported the DECODED sizes as the response
  sizes — 9x the real transfer — and called the endpoint uncacheable):

  | Community | On the wire (gzip) | Decoded JSON | Packages |
  | --- | --- | --- | --- |
  | `repo` | **3,759,444 B (3.8 MB)** | 33,463,364 B (33.5 MB) | 6,350 |
  | `lethal-company` | **34,602,147 B (34.6 MB)** | 329,007,636 B (329 MB) | 50,703 |

  It is also **conditionally cacheable**: the response carries
  `Last-Modified` (`Wed, 09 Sep 2026 20:01:32 GMT`) and
  `Cache-Control: max-age=30` behind Cloudflare (`cf-cache-status: HIT`),
  and a conditional `If-Modified-Since` request answers **304 with zero
  bytes** when nothing has changed. So a refresh of an unchanged index is
  free; the cost that remains is decoding and parsing the full document
  when it HAS changed, plus its disk footprint.

  Each entry carries `full_name`, `owner`, `package_url`, `date_updated`,
  `categories`, `is_deprecated`, `uuid4`, and a `versions[]` array where
  each version has `version_number`, `description`, `icon`,
  `dependencies[]`, `download_url`, `downloads`, `file_size`,
  `date_created`, `website_url`.
- `GET https://thunderstore.io/api/experimental/package/<ns>/<name>/` — one
  package, with `latest` and `community_listings[]` (which communities list
  it, and under which categories).
- `GET https://thunderstore.io/api/experimental/package/<ns>/<name>/<ver>/`
  — one version, exactly the object embedded in the listing above.
- `GET https://thunderstore.io/api/experimental/community/?page_size=N` —
  cursor-paginated community list (`identifier`, `name`, `wiki_url`, …).
- `download_url` is a plain public URL
  (`https://thunderstore.io/package/download/<ns>/<name>/<ver>/`) that
  serves the zip with no token.
- `GET /api/experimental/frontend/c/<community>/packages/` → **403**. The
  frontend index is not public API.

**Dependencies are strings of the form `Namespace-Name-Version`** —
`["BepInEx-BepInExPack-5.4.2100", "Ozone-Runtime_Netcode_Patcher-0.2.5"]`
— an exact-version pin, resolved by Thunderstore clients as a minimum. The
BepInEx dependency is how a package declares "I am a BepInEx plugin", and
in the sample of most-downloaded Lethal Company packages, five of six
declared it.

The **package format** is fixed: a zip with `manifest.json`
(`name`, `version_number`, `website_url`, `description`, `dependencies[]`),
`icon.png` (256×256), `README.md`, and the mod's own files.

---

## 2. Framework dependency modelling (question 1)

**Answer: a per-game prerequisite, not a mod.**

The framework fails every test for "just another mod": it installs to the
game root rather than the mod path, its presence is a property of the game
installation (it survives a profile switch — you do not want the loader
torn out because you moved to a vanilla-ish profile), a plugin is unusable
without it, and the *correct build* depends on facts about the game
(Mono vs IL2CPP, native vs Proton) rather than about the mod.

Concretely: `domain.Game` gains a small optional block — something like

```yaml
loader:
  kind: bepinex          # the only value for now
  version: 5.4.23.5      # what is installed, or what is required
  runtime: mono          # mono | il2cpp
  bootstrap: proton      # native | proton
```

…and `domain.Mod` gains a way to say "requires loader bepinex >= X". The
existing `DependencyResolver` is the wrong seam for this: it orders mods
within a profile, and the loader is not in the profile. The right shape is
a **precondition checked at plan time** — `PlanInstall` refuses (or warns,
under a flag) when a mod requires a loader the game has not declared, with
the same typed-error-with-`Details()` treatment `ConflictError` gets, so
both frontends can render "this mod needs BepInEx; here is how to set it
up" rather than deploying a DLL into a game that will never load it.

The one thing to resist is inventing a general "framework" abstraction
before there is a second framework. MelonLoader exists and is the obvious
second one, but designing for it now buys nothing: `loader.kind` as a
string leaves the door open at zero cost.

---

## 3. Deploy mapping (question 2)

**Answer: the existing linker already covers the common case. One new
normalisation rule, no new deploy-rule type.**

Per [§1.3](#13-archive-layouts--three-shapes-observed): with the game's
`mod_path` set to its install path, shape A deploys correctly today, and
lmm's per-file deployed-files table gives conflict detection between two
plugins that ship the same file for free.

What is needed is an **archive-root normaliser** for BepInEx games, run at
ingest, the same place #237's `.EXMODZ` wrapper strip runs:

1. Drop `manifest.json`, `icon.png`, `README.md`, `CHANGELOG.md` at the
   archive root — metadata, never deployed.
2. If the remaining root is a single directory that itself contains
   `BepInEx/`, strip it (shape C).
3. If the remaining root is one of `plugins/ patchers/ monomod/ config/`,
   prefix with `BepInEx/` (shape B).
4. Otherwise, if the root already contains `BepInEx/`, deploy as-is
   (shape A).
5. Anything else: a loose `.dll` at the archive root is a plugin
   (`BepInEx/plugins/<ModName>/`), and anything unrecognised is a warning,
   not a guess.

**Classification** (plugin vs patcher vs full pack) falls out of the same
rules — the directory the payload lands in *is* the classification, so no
separate detector is needed. A package whose payload is `BepInEx/core/` is
a framework pack and should be refused as a mod with a message pointing at
the loader configuration.

**`BepInEx/config/`** deserves an explicit decision. Configs are generated
by the game on first run and then hand-edited; a mod that ships one is
seeding a default. Deploying them as symlinks like everything else would
make a user's edits either fail or silently write back into the cache.
lmm already has the right primitive: profile config overrides
(`ApplyProfileOverrides`, real files written into the game dir). Route
`BepInEx/config/**` through that path, copy-on-first-deploy, never
overwrite an existing file.

---

## 4. Bootstrap / launch integration (question 3)

**Answer: guidance, not automation. This is the part to say no to.**

lmm can honestly do three things:

1. **Detect** which mode a game needs. lmm already scans the Steam library
   (`internal/source/steam`, #206); the same `libraryfolders.vdf` /
   `appmanifest_*.acf` data plus the presence of `<Game>_Data/il2cpp_data/`
   versus `<Game>_Data/Managed/Assembly-CSharp.dll` on disk answers both
   "Mono or IL2CPP" and "native build or Proton" without launching
   anything.
2. **Print the exact launch option** for that combination —
   `./run_bepinex.sh %command%` for a native Linux build,
   `WINEDLLOVERRIDES="winhttp=n,b" %command%` for Proton — and say where to
   paste it.
3. **Verify** afterwards that the bootstrap looks intact (files present,
   `BepInEx/LogOutput.log` produced on the last run).

What lmm should **not** do is write Steam's `localconfig.vdf` or a Proton
prefix's registry. Those are Steam-owned files that must be edited with the
client closed, the format is undocumented and has changed, a bad write
loses every launch option for every game in the account, and the failure
would surface as "Steam ate my settings", not as "lmm has a bug". Add to
that: `run_bepinex.sh` itself carries a workaround for an *open*
UnityDoorstop issue about Steam's bootstrapper and `LD_PRELOAD`, so the
mechanism is not stable enough to automate blind.

**Downloading the framework** is a different question from configuring it,
and is a "later, maybe": the GitHub release API is public and the archives
are ~640 KB, so it is technically easy. It is deferred because the *choice*
of build is the hard part (§1.3's Windows-pack-vs-Linux-release trap), and
getting it wrong leaves a user with a game that silently loads nothing.
Tier 2 asks the user to install BepInEx and then tells them, precisely,
whether they got it right — which is most of the value at a fraction of the
risk.

---

## 5. Source fit (question 4)

**Answer: Thunderstore is worth its own built-in source, but not as a
custom `api` source, and the listing endpoint is a real design problem.**

The custom `api` source type is the wrong tool. It maps one endpoint to one
search and one get-mod; Thunderstore's model is community-scoped, its
dependency strings need parsing into a structured form, and there is no
per-query search endpoint at all — the only public listing is the whole
community index. That is `internal/source/thunderstore/` work, not YAML.

The blocker to design around, in one line: **`/c/<community>/api/v1/package/`
is unpaginated — 34.6 MB on the wire for `lethal-company`, 3.8 MB for
`repo`, decoding to 329 MB and 33.5 MB of JSON.** "Search Thunderstore"
cannot mean "fetch and parse that per keystroke". The workable shape is a
**local index**: fetch once per community, cache it on disk, search
locally, and refresh with `If-Modified-Since` — which is a 304 and zero
bytes when nothing changed (see [§1.4](#14-thunderstores-public-api)).

Size the decision on the real numbers, not on the earlier draft's. The
transfer is ordinary and the refresh is nearly free; what is not ordinary
is **decoding and parsing 329 MB of JSON** whenever a big community's index
does change, holding the searchable form of it, and keeping it on disk.
Tier 3 stays **L** and stays gated, but for that reason — a new
locally-indexed capability in a tool whose sources all search remotely —
rather than because the endpoint is huge and uncacheable, which it is not.

The good news is everything else is easy: no auth, no key, no rate-limit
signalling encountered, direct `download_url`s, stable
`Namespace-Name-Version` identity, and `date_updated` per package makes
update checking trivial. `dependencies[]` maps onto lmm's existing
`DependencyResolver` almost directly, with the BepInEx entry filtered out
and routed to the loader precondition instead (§2).

NexusMods, meanwhile, already works: a BepInEx plugin hosted there is just
an archive, and Tiers 1–2 make it deploy correctly with no source work at
all. **That is the argument for shipping Tiers 1–2 without Tier 3.**

---

## 6. Verify / health (question 5)

**Answer: a new verify tier scoped to loader-enabled games.** lmm's verify
engine already has the tier vocabulary (`VerifyTier`) and the repair
vocabulary (`--fix`), so this is population, not architecture:

| Check | Fix available |
| --- | --- |
| `BepInEx/core/BepInEx.Preloader.dll` present | no — points at the loader setup |
| Installed loader version matches what the game declares | no — reports drift |
| Bootstrap intact for the declared mode: `run_bepinex.sh` + `libdoorstop.so` (native), or `winhttp.dll` + `doorstop_config.ini` (Proton) | no |
| `BepInEx/LogOutput.log` exists and is newer than the last deploy | no — this is the only honest "did it actually load?" signal without launching the game |
| Every enabled plugin's files are linked under `BepInEx/plugins/` | yes — the existing relink repair |
| A plugin whose declared loader requirement the game does not meet | no — reports it |

The log-file check is the one worth calling out: it is the difference
between "the files are in the right place" and "the loader ran", and it
costs nothing. It is also why Tier 2 is worth shipping even without
Tier 4 — a user who set the launch option up wrong finds out from
`lmm verify` rather than from a mod that mysteriously does nothing.

---

## Verdict

**PARTIAL GO, tiered.** Tiers 1–2 are a genuinely small, low-risk change
that unlocks a large class of games. Tier 3 is a real project with an
unresolved design question. Tier 4 should not be built.

This is a **material scope addition** to the v2.0.0 bar, so it comes back to
the owner before anything is filed (per #267's ruling).

### Proposed issues (NOT filed — for the coordinator)

#### Tier 1 — deploy BepInEx plugins correctly (S)

> **`feat(core): normalise BepInEx archive layouts at ingest`**
> A BepInEx plugin archive comes in three shapes: game-root-relative
> (`BepInEx/plugins/X.dll`), BepInEx-relative (a bare `patchers/`), and
> wrapped in a single directory (`BepInExPack/…`). Normalise all three at
> ingest, dropping the `manifest.json`/`icon.png`/`README.md` metadata every
> Thunderstore package carries, and refuse a full framework pack as a mod.
> **Seam:** `archive_listing.go`'s member normalisation, beside #237's
> `.EXMODZ` wrapper strip. No new deploy-rule type — with the game's
> `mod_path` set to its own `install_path` (the same absolute path twice,
> NOT `mod_path: ""`, which deploys relative to the working directory) the
> existing linker already places the normalised paths correctly.

> **`feat(core): route BepInEx/config/** through profile config overrides`**
> Plugin configs are hand-edited after first run, so deploying them as
> symlinks either fails a user's edit or writes it back into the cache.
> Deploy them as real files, copy-on-first-deploy, never overwriting an
> existing one.
> **Seam:** `overrides.go` (`ApplyProfileOverrides`) — the mechanism exists;
> this is a routing rule, not a new one. **Size:** S.

#### Tier 2 — the framework as a first-class prerequisite (M)

> **`feat(domain,config): a per-game mod-loader block`**
> `domain.Game` gains an optional `loader:` (kind, version, runtime
> mono/il2cpp, bootstrap native/proton) and `games.yaml` learns to round-trip
> it. The loader is a property of the game installation, not a mod: it lives
> in the game root and must survive a profile switch.
> **Seam:** `internal/domain/game.go` + `storage/config`, following
> `deploy_mode`'s existing shape. **Size:** S.

> **`feat(core): refuse to deploy a plugin into a game with no loader`**
> A mod that declares a loader requirement the game does not meet fails at
> PLAN time with a typed error carrying `Details()`, so both frontends can
> render the setup instructions instead of deploying a DLL that will never
> load. Not the `DependencyResolver` — that orders mods within a profile,
> and the loader is not in the profile.
> **Seam:** `plan.go`/`install.go` preconditions + `errors.go`. **Size:** M.

> **`feat(core,cli): detect the bootstrap mode and print the launch option`**
> Answer Mono-vs-IL2CPP and native-vs-Proton from the existing Steam scan
> plus on-disk markers (`<Game>_Data/il2cpp_data/` vs
> `Managed/Assembly-CSharp.dll`), then print the exact launch option to
> paste: `./run_bepinex.sh %command%` or
> `WINEDLLOVERRIDES="winhttp=n,b" %command%`. lmm does not write Steam's
> `localconfig.vdf` or a Proton prefix — see the spike's §4.
> **Seam:** `internal/source/steam` + `game_detect.go` (#206). **Size:** M.

> **`feat(core): a verify tier for loader-enabled games`**
> Preloader present, installed version matches the declaration, the
> bootstrap files match the declared mode, `BepInEx/LogOutput.log` is newer
> than the last deploy (the only honest "did it load?" signal), and every
> enabled plugin is linked. Only the last is `--fix`-able.
> **Seam:** `verify.go`'s existing `VerifyTier`. **Size:** M.

#### Tier 3 — Thunderstore as a source (L, gated)

> **`feat(source): a built-in Thunderstore source`**
> Public, keyless API; stable `Namespace-Name-Version` identity; direct
> `download_url`s; `date_updated` makes update checks trivial. NOT a custom
> `api` source — the model is community-scoped and dependencies need
> parsing.
> **Blocker to settle first:** the only public listing endpoint returns the
> whole community index unpaginated (measured: 34.6 MB gzipped for
> `lethal-company`, decoding to 329 MB of JSON; 3.8 MB / 33.5 MB for
> `repo`), and there is no per-query search endpoint. It IS conditionally
> cacheable — `If-Modified-Since` answers 304 with zero bytes — so the cost
> is the decode, the parse and the disk, not the transfer. Search has to
> run against a locally cached index: a new capability for lmm, whose
> sources all search remotely. **Size:** L.

> **`feat(core): map Thunderstore dependency strings onto the resolver`**
> `["BepInEx-BepInExPack-5.4.2100", "Ozone-Runtime_Netcode_Patcher-0.2.5"]`
> parses into lmm's existing dependency model, with the BepInEx entry
> filtered out and routed to the Tier-2 loader precondition instead of being
> resolved as a mod. **Seam:** `dependencies.go`. **Size:** M.

#### Tier 4 — lmm installs the framework (NO-GO for now)

> Downloading is easy (public GitHub releases, ~640 KB); **choosing** is
> not. The Thunderstore `BepInExPack` is Windows-only and correct only for
> Proton; a native Linux build needs the GitHub `BepInEx_linux_x64` release;
> IL2CPP needs a BepInEx 6 bleeding-edge build that is not a GitHub release
> at all. Get it wrong and the user has a game that silently loads nothing.
> Revisit once Tier 2's detection has been exercised against real
> installations.

### Questions for the owner

1. **Ship Tiers 1–2 without Tier 3?** — *Recommended: yes.* They stand on
   their own: a BepInEx plugin downloaded from NexusMods (already
   supported) deploys correctly and is verifiable. Tier 3 is a separate,
   larger project that should not hold them up.
2. **Is a locally cached search index acceptable for Thunderstore?** —
   *Recommended: yes, if Tier 3 is wanted at all.* There is no alternative:
   the public API offers no per-query search. It should be an explicit,
   documented design decision rather than something discovered during
   implementation. **Note if this was already answered:** it was asked on
   the wrong figures. The endpoint is 3.8–34.6 MB on the wire (not 33–329
   MB) and refreshes for free with `If-Modified-Since`; the real cost is
   decoding and parsing up to 329 MB of JSON per refresh of a large
   community and keeping the index on disk. The recommendation does not
   change, but the answer was given against a worse picture than the true
   one.
3. **Confirm lmm never edits Steam launch options or a Proton prefix?** —
   *Recommended: confirm.* Print the exact string, verify the result. The
   downside of a bad `localconfig.vdf` write is losing every launch option
   for every game in the account.
4. **Does any of this belong in the v2.0.0 bar, or after it?** —
   *Recommended: after.* Tiers 1–2 are small but they are new surface
   (a new `games.yaml` block, a new verify tier, a new precondition error),
   and v2.0.0's bar is "feature complete and polished" against the issues
   that already exist. #267 asked for a spike, and the spike's answer is
   "worth doing, and it is its own release".

---

## Sources

- BepInEx releases — <https://github.com/BepInEx/BepInEx/releases> (v5.4.23.5, 2026-02-08; asset list and archive contents verified by download)
- BepInEx README compatibility table — <https://github.com/BepInEx/BepInEx>
- BepInEx installation guide — <https://docs.bepinex.dev/articles/user_guide/installation/index.html>
- BepInEx Proton/Wine guide — <https://docs.bepinex.dev/articles/advanced/proton_wine.html>
- `run_bepinex.sh` and `doorstop_config.ini` — read from the v5.4.23.5 Linux and Thunderstore archives
- UnityDoorstop — <https://github.com/NeighTools/UnityDoorstop> (issue #88, cited by `run_bepinex.sh` itself)
- Thunderstore API — `thunderstore.io/c/<community>/api/v1/package/`, `thunderstore.io/api/experimental/package/…`, `thunderstore.io/api/experimental/community/` (all probed directly)
- Thunderstore package format — <https://thunderstore.io/c/repo/create/docs/>
- Thunderstore index sizes and cache headers in [§1.4](#14-thunderstores-public-api) — re-measured 2026-09-09 during the Track D1 review, one read-only GET per community (`repo`, `lethal-company`) plus one conditional `If-Modified-Since` repeat, recording transfer size, decoded size, package count and response headers only
- Package archives listed: `BepInEx/BepInExPack` 5.4.2305, `denikson/BepInExPack_Valheim`, `RugbugRedfern/Skinwalkers` 5.0.0, `Evaisa/HookGenPatcher` 0.0.5, `Sligili/More_Emotes` 1.3.3
