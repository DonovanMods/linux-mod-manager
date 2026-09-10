# lmm - Linux Mod Manager

A native, terminal-first Linux mod manager focused on reproducible profiles,
multiple mod sources, and scriptable game-mod deployment — with a full local
web UI over the same engine.

A profile is a declaration of the mods and versions a game should be running.
`lmm` resolves that declaration against the sources you have configured, caches
what it downloads, deploys it into the game directory by a method you choose,
and can tell you at any point whether what is on disk still matches. Two
first-class frontends drive the same engine: a CLI built for scripting,
automation and remote work over SSH, and `lmm serve`, a local web UI built for
day-to-day use.

## Features

### The model

- **Profiles are desired state**: a profile records the mods, versions and load
  order a game should be running, and `lmm profile apply` **converges** the
  installation to it — installing what is missing, undeploying and disabling
  what it no longer lists, and moving versions in either direction, downgrades
  included
- **Locks are enforced when converging**, not merely when checking.
  `lmm mod lock` pins a build in the profile, it travels with
  `lmm profile export`/`import`, and it holds through apply, deploy, update and
  rollback — see [Locking mods to a version](#locking-mods-to-a-version)
- **Verification reasons about provenance**: `lmm verify` checks the cache
  against recorded checksums, the deployed tree against what the profile
  actually provides, and each recorded version against its source. `--fix`
  removes only the files lmm itself deployed — never a file it did not put
  there — and it names a finding it will not attempt rather than guessing at
  one; a locked mod's record is left alone on purpose
- **Snapshots and an originals store**: lmm keeps the game files it displaces,
  so `lmm snapshot create|list|restore|delete` can bring a game back to a
  recorded arrangement — see [Snapshots](#snapshots)
- **Sources are first-class, not hard-coded**: NexusMods, CurseForge, Steam
  Workshop, Thunderstore and Icarus are built in, and `directory`, `manifest`
  and `api` sources are defined in YAML — with advertised capabilities, live
  validation
  (`lmm source validate --probe`), their own authentication, and graceful
  degradation where a capability is missing — see
  [Custom Sources](#custom-sources)
- **Steam Workshop, three ways**: track the items Steam already installed,
  search the Workshop and import a collection as a profile, or download an item
  so lmm manages its own copy — see [Steam Workshop](#steam-workshop)
- **Update policies and rollback**: `auto`, `notify` (default) or `pinned` per
  mod, with `lmm update rollback` to step back a version

### Getting it onto disk

- **Flexible deployment**: symlink, hardlink or copy, set globally, per game or
  per profile
- **Dependency resolution**: dependencies are fetched and installed in order,
  with cycle detection (opt out with `--no-deps`)
- **Conflict detection**: `lmm conflicts` names every contested file, its
  contenders and the rule that decides the winner
- **Two first-class frontends, one core**: everything below is the CLI;
  `lmm serve` is a full single-page web UI over the same database, profiles
  and engine — the same games, sources, profiles, plans, locks, conflicts,
  snapshots and imports, with live progress over SSE. Neither is a cut-down
  view of the other — see [Web UI](#web-ui-lmm-serve)
- **Scriptable**: `--json` prints exactly one document on stdout and nothing
  else, and `/api/v1` returns those same documents — see
  [JSON output](#json-output)
- **Pure Go**: one static binary, no CGO, easy cross-compilation

## Why lmm?

Vortex, Limo and Mosaic are **GUI-first interactive mod managers**: you arrange
your setup by hand, in an application, and the application holds the result.
The difference is not whether there is a window — lmm has one — it is where
the truth about your setup lives.

lmm is aimed at **reproducible, source-agnostic mod environments** — the setup
is a document you can export, review, diff and re-apply. If you have ever
wanted your mod list to behave more like a package manifest than a pile of
state managed by buttons, that is the pitch.

You drive that model from **either of two first-class frontends**: the CLI,
for scripting, automation, `--json` pipelines and a headless box over SSH, and
`lmm serve`, a local web UI meant for daily use — the library, search,
updates, health, conflicts, profiles, snapshots, imports and setup, every
mutation previewed as the same plan the CLI prints. They are the same code
underneath — one engine, one database, one binary, nothing extra to
install — which is why neither drifts behind the other, and why the web UI
keeps growing with the engine rather than trailing it.

The whole program is one pipeline:

```text
mod sources          NexusMods, CurseForge, Steam Workshop, Icarus,
      ↓              plus directory / manifest / api sources you define
normalized mod/version model
      ↓              one Mod + version + file shape, whatever answered
profile (desired state)
      ↓              mods, versions, locks, load order — exportable YAML
cache
      ↓              one copy per source/mod/version, checksummed
deployment strategy
      ↓              symlink, hardlink or copy into the game directory
game-specific adapter
                     generic file deployment, or a real compile step
                     (Icarus `.pak`/`.exmodz` merging) where a game needs one
```

**What lmm does not have.** There is no FOMOD installer UI, no _automated_
LOOT-style plugin sorting (masterlists, a generated `plugins.txt`) and no
Nexus Collections import. Load order itself lmm does have: a profile records
it, `lmm profile reorder` and the web UI's reorder modal change it, and it
decides precedence at merge and deploy time — what is missing is the Bethesda
ecosystem's _automatic_ sorting of plugins, not ordering as such.

**When to pick something else.** lmm installs, deploys, updates, locks and
reproduces Skyrim and Fallout mods perfectly well. What it cannot do for that
ecosystem _today_ is walk you through a FOMOD option tree or sort your plugins
the way LOOT does, and a Bethesda setup that leans on those two is better
served by [Limo](https://github.com/limo-app/limo) or Mosaic for now — both
are tracked here as work, not dismissed
([#354](https://github.com/DonovanMods/linux-mod-manager/issues/354),
[#355](https://github.com/DonovanMods/linux-mod-manager/issues/355)). Reach
for lmm when you want your mod setup scripted, reproduced across machines,
driven over SSH or from a browser, or extended to a source nobody has written
a client for.

### Non-goals for 2.0

These are deliberate omissions, tracked so they are not mistaken for oversights:

- **FOMOD installers** — [#354](https://github.com/DonovanMods/linux-mod-manager/issues/354)
- **Automated LOOT-style plugin sorting** — masterlists and a generated
  `plugins.txt`, for the games that need one:
  [#355](https://github.com/DonovanMods/linux-mod-manager/issues/355). Load
  ordering itself is a shipped feature (`lmm profile reorder`, the web UI's
  reorder modal, and precedence at merge and deploy time); it is the
  _automatic_ sorting that is out of scope for 2.0
- **Nexus Collections** — no tracking issue; a Steam Workshop collection
  already imports as a profile, and the Nexus equivalent is not planned for 2.0
- **A separate native desktop application** — the graphical frontend is
  `lmm serve`, and it is a first-class one: a full web UI over the same `core`
  package the CLI calls, in the same binary, with no Node, no bundler and no
  config of its own. What is not planned is a _second_ application with its
  own build, its own packaging and its own drift.

### The adapter seam

The Icarus support is the interesting one: lmm does not merely copy that game's
archives, it converts prebuilt `.pak` mods, derives their changes against the
current base game, merges them by profile precedence and regenerates the result
when load order changes. Making that a **documented adapter seam** — so Unreal,
Unity and the rest land without contaminating the generic core — is in
progress for 2.0: [#353](https://github.com/DonovanMods/linux-mod-manager/issues/353)
(design approved; the seam and a generic adapter land first, then Icarus and
BepInEx move behind it).

## Installation

### Arch Linux (AUR)

```bash
yay -S lmm-bin        # or: paru -S lmm-bin
```

[`lmm-bin`](https://aur.archlinux.org/packages/lmm-bin) installs the released
binary, the man pages, and shell completions for bash, zsh and fish. It
`provides`/`conflicts` `lmm`, so it will not fight a source build of the same
name.

### Debian, Ubuntu, Fedora, openSUSE, Alpine

`.deb`, `.rpm` and `.apk` packages are attached to every release on the
[Releases page](https://github.com/DonovanMods/linux-mod-manager/releases),
for `amd64` and `arm64`:

```bash
# Debian / Ubuntu
sudo dpkg -i lmm_<version>_linux_amd64.deb

# Fedora / RHEL
sudo rpm -i lmm_<version>_linux_amd64.rpm

# openSUSE
sudo zypper install ./lmm_<version>_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted lmm_<version>_linux_amd64.apk
```

These install `/usr/bin/lmm`, the man pages under `/usr/share/man/man1`
(`man lmm`), shell completions, and the README, CHANGELOG and LICENSE under
`/usr/share/doc/lmm`. `p7zip` is a _recommended_ dependency, not a required
one: lmm extracts `.zip` archives itself and shells out to `7z` (and `unrar`)
only for `.7z` and `.rar` mods.

### From GitHub Releases

Download the latest release for your architecture from the [Releases page](https://github.com/DonovanMods/linux-mod-manager/releases):

- `lmm_<version>_linux_amd64.tar.gz` - 64-bit x86
- `lmm_<version>_linux_arm64.tar.gz` - 64-bit ARM

Extract and install:

```bash
tar -xzf lmm_*.tar.gz
sudo mv lmm /usr/local/bin/
```

The tarball also carries the man pages under `docs/man/man1/`; copy them to
`/usr/share/man/man1` (or `~/.local/share/man/man1`) to get `man lmm`.

### With Go Install

Requires Go 1.27 or later.

```bash
go install github.com/DonovanMods/linux-mod-manager/v2/cmd/lmm@latest
```

The module path carries the `/v2` suffix, so `@latest` resolves to the newest
2.x release. Until the v2.0.0 tag is published, name the branch instead:
`...@v2`.

### From Source

```bash
git clone https://github.com/DonovanMods/linux-mod-manager.git
cd linux-mod-manager
go build -o lmm ./cmd/lmm
```

This checks out `main`, which still holds the v1.x line: the 2.0 work lives on
the `v2` branch until the release merges it. Run `git checkout v2` before
`go build` to build what this README describes.

### Shell completions

The distro packages and the AUR package install completions for you. If you
installed from a tarball, `go install`, or source, generate them yourself —
`lmm completion --help` covers every shell, including PowerShell:

```bash
# bash (needs bash-completion)
lmm completion bash | sudo tee /usr/share/bash-completion/completions/lmm >/dev/null

# zsh
lmm completion zsh | sudo tee /usr/share/zsh/site-functions/_lmm >/dev/null

# fish
lmm completion fish > ~/.config/fish/completions/lmm.fish
```

Start a new shell afterwards. To try one without installing it, source it in
the current shell instead: `source <(lmm completion bash)`.

## Quick Start

### `lmm init` — the guided first run

```bash
lmm init
```

One command that walks the whole setup, once: it scans your Steam libraries
for moddable games and adds the ones you pick (filling in their paths and
sources from the install itself), sets a default game so you can leave
`--game` off from then on, signs you in to each mod source your games use
with that source's own instructions, and scans the game's mod directory for
mods that are already there so lmm can manage them.

Every step is skippable — press Enter to take the default, or answer `n` to
move on — and **re-running it is safe**: a game that is already configured
is marked and not added again, a source that is already authenticated is not
asked for a key, and a default game that is already set is left alone. So
it doubles as "add the game I just installed".

It is interactive by design. Under `--json`, or with nothing to read from,
it prints the equivalent commands instead of prompting — a script wants
those, not a wizard.

The rest of this section is the same setup done by hand, which is worth
reading once even if you used `lmm init`.

### `lmm serve` — the same first run in a browser

```bash
lmm serve
# lmm serve listening on http://127.0.0.1:7420/
```

If you would rather click than type, `lmm serve` opens a local web UI on the
same database and profiles, and with no games configured yet it opens on the
same first-run flow: detect your Steam games and add them, sign in to each
source, define a custom source if you need one, and adopt the mods already in
the game folder. Everything the CLI does below is there too — see
[Web UI](#web-ui-lmm-serve).

### Authentication

Mod sources require API keys for downloading mods.

#### NexusMods

Get your personal API key from [NexusMods API settings](https://www.nexusmods.com/users/myaccount?tab=api):

```bash
lmm auth login nexusmods
# Or set the environment variable
export NEXUSMODS_API_KEY="your-api-key"
```

#### CurseForge

Get your API key from [CurseForge Console](https://console.curseforge.com/):

```bash
lmm auth login curseforge
# Or set the environment variable
export CURSEFORGE_API_KEY="your-api-key"
```

#### Where the key is stored

A key from `lmm auth login` is stored **encrypted** (AES-256-GCM) in
`~/.local/share/lmm/lmm.db`, under a 32-byte key file at
`~/.local/share/lmm/key` that lmm creates `0600` the first time you log in.
`lmm auth status` identifies a stored credential by a short fingerprint and
never prints it back.

Be clear about what this buys you: it protects a database that gets
**copied, synced, or backed up** — the write-ahead log included — not
against someone already running as you on your own machine, who can read
the key file just as easily. **The key file is the secret: back it up with
the database, or expect to run `lmm auth login` again.** The full threat
model, the failure modes and their fixes are in
[docs/security.md](docs/security.md).

### Set Default Game

Set a default game to avoid specifying `--game` for every command:

```bash
# Set default game
lmm game set-default baldurs-gate-3

# Now all game commands work without --game
lmm search "camera"
lmm install "camera"
lmm list
lmm update
lmm uninstall 12345
lmm mod set-update 12345 --auto
lmm profile list

# Show current default
lmm game show-default

# Clear the default
lmm game clear-default

# Everything lmm knows about one game, including its mod-loader status and
# the Steam launch option a BepInEx game needs (see BepInEx (Unity games))
lmm game show skyrim-se
```

### Basic Usage

**Source auto-detection:** Commands automatically use the mod source configured for your game. If a game has multiple sources, you will be prompted to choose (or use `-y` to auto-select, or `--source` to specify explicitly) — **except `search`**, which queries every configured source concurrently by default instead of prompting (see [Search](#search) below).

```bash
# Search for mods (all configured sources by default)
lmm search "camera" --game baldurs-gate-3

# Install a mod (interactive selection)
lmm install "camera" --game baldurs-gate-3

# Install multiple mods (select with 1,3-5 or 1..3 syntax)
lmm install "ui" --game baldurs-gate-3

# Install by mod ID (for scripting)
lmm install --id 12345 --game baldurs-gate-3

# List installed mods
lmm list --game baldurs-gate-3

# Check for updates (shows partial results and a warning if some mods can't be checked)
lmm update --game baldurs-gate-3

# Update a specific mod
lmm update 12345 --game baldurs-gate-3

# Rollback to previous version
lmm update rollback 12345 --game baldurs-gate-3

# Show status
lmm status
```

### Update Policies

Control how each mod handles updates:

```bash
# Auto-update when checking
lmm mod set-update 12345 --game baldurs-gate-3 --auto

# Notify only (default)
lmm mod set-update 12345 --game baldurs-gate-3 --notify

# Mute update checks for this mod (does not hold a version — see Locking below)
lmm mod set-update 12345 --game baldurs-gate-3 --pin
```

`--pin` is a check-mute, not a version freeze: it stops `lmm update` from asking
the source about the mod at all, but the mod is free to be reinstalled,
rolled back, or otherwise moved to a different version by anything other than
an update check. If what you actually want is "this profile deploys exactly
version X, and nothing changes that", lock it instead (below). `--pin`
remains the only freeze available on sources that cannot resolve versions
(e.g. plain `directory` sources), since locking requires that capability.

### Locking mods to a version

`lmm mod lock <mod-id> [version]` locks the mod's entry in the current
profile to an exact version. With no version argument it locks at the
version currently recorded for the mod; with a version argument, that
version is resolved and validated against the source before the lock is
written — an unresolvable version is refused instead of writing a lock that
can never be satisfied. Locking requires a source that can resolve versions
(NexusMods, CurseForge, `manifest`/`api` sources with `mod_files`); a
version-less source is refused with a pointer to `lmm mod set-update --pin`
instead.

```bash
# Lock at the currently installed version
lmm mod lock 12345 --game baldurs-gate-3

# Lock at a specific version
lmm mod lock 12345 1.2.3 --game baldurs-gate-3

# Clear the lock (recorded version is left untouched)
lmm mod unlock 12345 --game baldurs-gate-3
```

Locking is a metadata write, not a deploy: if the locked version differs
from what's currently installed, the command says so and the game directory
doesn't change until the next `lmm profile apply` (or `lmm deploy`), which
converges the mod to the locked version — downgrades included. `lmm mod
unlock` clears only the lock marker; the mod's recorded version is left
exactly as-is, since that's the record, not the lock.

**Lock vs. pin, in one line**: pinning mutes a mod's update _notifications_;
a lock is a lockfile entry that pins a _build_.

|                                        | `--pin` (update policy)                 | lock                                         |
| -------------------------------------- | --------------------------------------- | -------------------------------------------- |
| Statement about                        | "stop asking the source about this mod" | "this profile deploys exactly version X"     |
| Enforced at                            | check time only                         | deploy time (converges, downgrades included) |
| Scope                                  | per-install (SQLite)                    | per-profile (profile YAML)                   |
| Travels with `profile export`/`import` | no                                      | yes — imports reproduce the exact build      |
| Works on version-less sources          | yes                                     | no — refused with a capability error         |

The two are orthogonal — a mod can be locked, pinned, both, or neither — and
wherever they'd conflict, the lock wins and the output names it:

- **Locked, any other policy ("locked but informed")**: `lmm update` still
  checks the mod and reports a newer version if one exists, but deploy/apply
  still converges to the locked version.
- **Locked + `auto`**: auto-update skips the mod instead of applying an
  update to it, reported as a distinct "N locked mod(s) skipped by
  auto-update" line (`lmm update --all` skips it the same way).
- **Locked + `pinned` ("locked and silent")**: the mod isn't checked at all,
  same as any other pinned mod.
- **`lmm update <locked-mod-id>` (explicit single-mod update)**: refused —
  "locked at v*X*; move the lock (`lmm mod lock <id> <version>`) or unlock
  first."
- **`lmm install` of a locked mod at any other version** (an explicit
  `--version`, or a plain reinstall that would land on a newer latest):
  refused with the same remedies before anything downloads or deploys.
  Installing at exactly the locked version (reinstall/repair) still works
  and keeps the lock.
- **`lmm mod edit` of a locked mod**: refused before anything is written —
  `--version` (other than the locked version itself) with the same
  move-the-lock/unlock remedies, and `--to-source`/`--to-source-id` re-linking
  with the unlock remedy alone, since a re-link would replace the locked
  profile entry with a fresh, unlocked one and moving the lock can't help.
  Metadata-only edits (`--name`/`--author`) still work.

Lock state shows up alongside version info wherever it's installed: `lmm
list -v`'s `LOCKED` column (the locked version, or `-`), `lmm mod show`'s
Installed section, and `lmm update`'s table, where a locked mod's `POLICY`
cell gets a `[locked@<version>]` suffix. `--json` output for `list` and `mod
show` carries the same information (`locked`, `locked_version`), and bulk
`lmm update --json` marks a locked mod's `updates[]` entry with
`"locked": true`; single-mod `lmm update --json`
instead reports a refused apply as `status: "skipped", reason: "locked"`, and
`lmm update rollback` of a locked mod is refused the same way — before its
"Rolling back..." header, with the same remedies and JSON document. `lmm verify` still reports a locked mod's version-record
mismatches, but `--fix` refuses to rewrite a locked mod's record, and
refuses its missing-file, missing-checksum and pak re-ingest repairs too
whenever the source cannot identify the recorded version's own file — every
one of those repairs downloads into the _recorded_ (locked) version's cache
slot, so filling it with whatever the source serves today is exactly what a
lock exists to prevent. A source that does not version its files at all
(Icarus, for one) can never identify it, so a locked mod there is repaired
by unlocking first. Other, unlocked mods in the same run are still fixed,
and each refusal reports as a `--fix skipped:` line naming the unlock
remedy, not as a repair that failed. Separately, when the installed version
hasn't yet converged to a lock's target, `verify` prints an informational
"lock pending convergence" note rather than treating it as drift to repair.

### Pak conversion (Icarus)

Icarus mods sometimes ship as a prebuilt `.pak` instead of (or in addition
to) a mergeable `.exmodz`. Deployed as-is, a prebuilt pak is frozen in
time — a whole-file snapshot of whatever `data.pak` existed when the
author built it — so it gets whole-table shadowed by the merged pak
(`zzz_LMM_Merged_P.pak` always mounts last) wherever a merged table
overlaps it, and silently reverts any base-game field it touches on every
weekly base update.

lmm fixes this by converting prebuilt paks into the merged pak at merge
time instead of deploying them raw: it **rebases** each one onto the
game's _current_ `data.pak`. A pak that embeds a `data.EXMOD` manifest
converts exactly — pure author intent, replayed against the current base
— otherwise lmm diffs the pak's tables against the current base by row
name and derives the changes. Drift baked into author-touched rows across
a rebase is the intended semantic, not a bug — the same
"later-in-load-order wins the field, not the whole row" rule from [Merge
precedence](#games-gamesyaml) applies identically to converted paks and
`.exmodz` mods.

Conversion is on by default, and controlled at two levels: a per-game
`convert_paks: false` in `games.yaml` turns it off for every pak mod on
that game, and `lmm mod convert <mod-id> off` keeps one specific mod's
pak raw regardless of the game setting — either one is enough to keep a
pak raw. An irreconcilable pak (unreadable
or unresolvable layout, a hyphen-ambiguous table path, a `RowStruct`
mismatch) produces a per-mod error and falls back to raw deploy instead
of blocking the rest of the merge; `lmm verify` reports it as
`conversion_failed` (see [Verify output](#verify-output)).

Pak mods cached before this feature shipped have no retained raw source
to convert from. `lmm verify` flags these `needs_reingest`, and `--fix`
re-ingests them — redownloading via the normal cache path — into the
conversion pipeline; there is no separate migration step to run.

### BepInEx (Unity games)

Many Unity games are modded through [BepInEx](https://github.com/BepInEx/BepInEx),
a loader that installs into the **game root** and injects plugins from
`BepInEx/plugins/`. lmm supports those games, and the split of
responsibility is deliberate and worth reading before you start: **lmm
deploys and verifies plugins; you install the loader and set the Steam
launch option.**

#### Configure the game

BepInEx-managed content is game-root-relative, so the game's `mod_path` is
its own `install_path` — the same absolute path twice:

```yaml
games:
  lethal-company:
    name: "Lethal Company"
    install_path: "~/.steam/steam/steamapps/common/Lethal Company"
    mod_path: "~/.steam/steam/steamapps/common/Lethal Company"
    sources:
      nexusmods: "lethalcompany"
    loader:
      kind: bepinex
      version: 5.4.23.5
      runtime: mono # mono | il2cpp - omit and lmm reads it off the game directory
      bootstrap: proton # native | proton - likewise
```

Not `mod_path: ""` — an empty value is not "the game root", it is a
relative path, and lmm would deploy into whatever directory you ran it
from.

The same thing from the command line, or in the web UI's Setup → Games row
(the **Edit loader…** control):

```bash
lmm game add --name "Lethal Company" --source nexusmods --id lethalcompany   --path "$HOME/.steam/steam/steamapps/common/Lethal Company"   --mod-path "$HOME/.steam/steam/steamapps/common/Lethal Company"   --loader bepinex --loader-version 5.4.23.5

lmm game edit lethal-company --loader bepinex --loader-bootstrap proton
lmm game edit lethal-company --loader ""   # remove the declaration
```

#### What lmm does

- **Normalises plugin archive layouts at install.** A BepInEx plugin
  archive comes in three real shapes, and lmm places all of them
  correctly: game-root-relative (`BepInEx/plugins/Foo.dll`, the common
  case), wrapped in a single directory (`BepInExPack/BepInEx/…`, whose
  wrapper is stripped), and BepInEx-relative (a bare `plugins/`,
  `patchers/`, `monomod/` or `config/` root, which gains its `BepInEx/`
  prefix). A loose `.dll` at the archive root becomes
  `BepInEx/plugins/<ModName>/`. The package metadata every Thunderstore
  archive carries — a `manifest`, an `icon`, a `readme`, a `changelog` or a
  `license`, whatever extension it is spelled with — is dropped, not
  scattered into your game directory. That drop runs both before and after
  the wrapper strip, so a package that keeps its metadata _inside_ the
  wrapper — which is how Thunderstore builds one — does not deliver four
  files into your Steam install directory. A layout lmm does not recognise
  is reported, never guessed at.

  The `BepInEx/`-rooted and wrapped shapes are recognised for any game. The
  two ambiguous ones — a bare `plugins/` root and a bare `.dll` — need the
  `loader:` declaration above, so a mod for a different game that happens
  to be rooted at `plugins/` keeps deploying exactly where it always did.

- **Seeds plugin configuration instead of linking it.** BepInEx writes its
  `BepInEx/config/*.cfg` files on first run and you hand-edit them
  afterwards, so a mod that ships one is offering a default. Those files
  are written as **real files, copied only when nothing is there already**
  — the same treatment a profile's own config overrides get. Your edits are
  never overwritten by a later deploy, never written back into the mod
  cache, and never removed by an uninstall.

- **Refuses to deploy a plugin into a game with no loader.** A
  BepInEx-shaped archive installed into a game that declares no loader
  fails at plan time with the setup instructions, rather than putting a DLL
  somewhere nothing will ever load it from — which fails silently and is
  the hardest kind of failure to diagnose.

- **Refuses to install BepInEx itself as a mod.** An archive carrying
  `BepInEx/core/` is the loader, not a plugin: it belongs to the game
  installation and must survive a profile switch, so tracking it as a
  profile member would tear it out from under every plugin the next time
  you switched. lmm says so and points at the setup.

- **Tells you the exact launch option.** `lmm game show <game>` — and the
  web UI's loader panel — read the game directory to work out whether it is
  a native Linux build or a Windows one running under Proton, and print the
  string to paste:

  ```text
  Steam launch options for this game:
    WINEDLLOVERRIDES="winhttp=n,b" %command%
  ```

  (`./run_bepinex.sh %command%` for a native Linux build.) Where lmm cannot
  tell, it says so instead of guessing — the wrong option launches the game
  normally and loads nothing.

- **Verifies the result.** For a game with a `loader:` block, `lmm verify`
  adds five checks: the preloader is present, the installed version matches
  what you declared, the bootstrap files match the declared mode,
  `BepInEx/LogOutput.log` exists and is newer than your last deploy, and
  every enabled plugin is actually linked. The log check is the point of
  the tier — it is the only honest evidence the loader **ran**, as opposed
  to being installed correctly, and it is how you find out you pasted the
  launch option wrong instead of finding out from a mod that mysteriously
  does nothing. Only the last check is `--fix`-able (it re-deploys the mod);
  the others report and point at the setup.

- **Keeps the download when it refuses one.** A plugin archive installed
  into a game that declares no loader is refused at ingest, which is the
  earliest point the archive's shape is knowable — but the downloaded file
  is kept, so the retry after `lmm game edit <game> --loader bepinex` does
  not fetch it again. Only this refusal keeps anything: a checksum
  mismatch or a truncated download means the bytes are suspect, and those
  are discarded as before.

#### What lmm never does

- **It does not install BepInEx.** Choosing the build is the hard part: the
  Thunderstore `BepInExPack` is Windows-only and correct only under Proton,
  a native Linux build needs the `BepInEx_linux_x64` archive from BepInEx's
  own GitHub releases, and IL2CPP needs a BepInEx 6 bleeding-edge build
  that is not a GitHub release at all. Get it wrong and you have a game
  that silently loads nothing.
- **It does not write Steam's configuration.** Launch options live in
  `localconfig.vdf`, which must be edited with the client closed, in an
  undocumented format that has changed; a bad write loses every launch
  option for every game in your account, and the failure would look like
  "Steam ate my settings". lmm prints the string; you paste it.
- **It does not touch a Proton prefix.** No `user.reg`, no `winecfg`
  automation, no `protontricks` shell-outs.

## Configuration

Configuration files are stored in `$XDG_CONFIG_HOME/lmm/` (default `~/.config/lmm/`; override with `--config`):

### Main Config (`config.yaml`)

```yaml
default_link_method: symlink # Global default: symlink, hardlink, or copy
default_game: baldurs-gate-3 # Optional, set via 'lmm game set-default'
cache_path: ~/.local/share/lmm/cache # Optional, defaults to <data_dir>/cache
```

The `cache_path` setting allows you to store downloaded mod files in a custom location. This is useful if you want to:

- Store mods on a separate drive with more space
- Share cached mods between multiple installations
- Use a faster SSD for mod storage

Paths support `~` expansion for the home directory.

### Games (`games.yaml`)

```yaml
games:
  baldurs-gate-3:
    name: "Baldur's Gate 3"
    install_path: "/path/to/Steam/steamapps/common/Baldurs Gate 3"
    mod_path: "Data" # Relative to install_path; an absolute path works too
    sources:
      nexusmods: "baldursgate3"
    # link_method: symlink  # Optional: override default_link_method for this game
    # cache_path: ~/bg3-mods  # Optional: override global cache_path for this game

  starfield:
    name: "Starfield"
    install_path: "/path/to/starfield"
    mod_path: "/path/to/starfield/Data"
    sources:
      nexusmods: "starfield"
    link_method: copy # This game requires file copies instead of symlinks
    cache_path: /mnt/fast-ssd/starfield-mods # Store this game's mods on fast storage

  icarus:
    name: "Icarus"
    install_path: "/path/to/Steam/steamapps/common/Icarus"
    mod_path: "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods"
    sources:
      icarus: "icarus"
    deploy_mode: compile
    # convert_paks: true  # Optional: default; set false to deploy every prebuilt .pak mod raw instead of converting it

  lethal-company:
    name: "Lethal Company"
    install_path: "/path/to/Steam/steamapps/common/Lethal Company"
    mod_path: "/path/to/Steam/steamapps/common/Lethal Company" # BepInEx deploys into the game root
    sources:
      nexusmods: "lethalcompany"
    loader: # Optional: this game uses a mod loader (see BepInEx (Unity games))
      kind: bepinex
      version: 5.4.23.5
      # runtime: mono        # mono | il2cpp - omit and lmm reads it off the game directory
      # bootstrap: proton    # native | proton - likewise
```

An optional **`loader:`** block declares that a mod loader is installed in
the game directory. Only `kind: bepinex` means anything to lmm today;
`version`, `runtime` and `bootstrap` are each optional, and an unrecognised
`runtime` or `bootstrap` fails the load naming the game, the value and the
valid set rather than defaulting silently. What the declaration changes is
which rules apply: archive-layout normalisation for the two ambiguous
BepInEx shapes, a plan-time refusal to deploy a plugin the game cannot load,
and `lmm verify`'s loader checks. lmm never installs the loader itself. See
[BepInEx (Unity games)](#bepinex-unity-games).

`mod_path` may be **absolute, or relative to `install_path` — everywhere**. A relative value — `mod_path: Data` — is resolved against that game's `install_path`, never against your current working directory (`~` expands first, so `~/mods` is absolute, not relative). That rule is the same on every path that writes a game, not just for a hand-written `games.yaml`: `lmm game add`, `lmm game add --from-detected`, `lmm game detect`, `POST /api/v1/games` and the web UI's add-game form all accept `Data` and store the resolved absolute path, so what lmm writes reads back identically from any shell. A relative `mod_path` with no `install_path` to resolve it against is refused — naming the game and the field when `games.yaml` is read, and naming `install_path` (the value that is actually missing) at the prompt, the form and the API.

Steam auto-detection (`lmm game detect`) knows about all three of these — Baldur's Gate 3, Starfield and Icarus (App ID `1149460`) — and generates the equivalent entry for you, `install_path`/`mod_path` filled in from your actual Steam library — the YAML above is kept here as reference for what gets written, not something you need to type by hand.

**Merge precedence**: with more than one `compile`-mode mod installed (currently Icarus only), the profile's load order — the same order `lmm list` displays and `lmm profile reorder` changes — decides how conflicting changes resolve. Mods are merged in load order, so a mod later in the list is applied later and wins conflicting _fields_ on a shared data-table row; it's a per-field upsert, not a whole-row overwrite, so untouched fields from earlier mods still survive. Bundled asset files can't compose that way — a same-path collision between two mods is whole-file last-wins, and installing or updating a colliding mod prints a warning naming both. Either way, the bottom of the load order has final say, and `lmm profile reorder` regenerates the merged pak immediately, so a reorder's effect on precedence is visible right away rather than at the next deploy. Prebuilt `.pak` mods participate in this same merge: at merge time each one is converted and rebased onto the game's current base pak — a pak embedding a `data.EXMOD` manifest converts exactly, otherwise lmm diff-derives the changes against the current base — and only an irreconcilable pak falls back to a raw, unconverted deploy, with a warning naming it (see [Pak conversion (Icarus)](#pak-conversion-icarus)). Set `convert_paks: false` in a game's `games.yaml` entry, or `lmm mod convert <mod-id> off` for one mod, to keep specific paks deployed raw instead.

### Deployment Methods

Mods can be deployed using three methods:

| Method     | Description                                                    |
| ---------- | -------------------------------------------------------------- |
| `symlink`  | Symbolic links to cached files (default, space efficient)      |
| `hardlink` | Hard links (transparent to games, requires same filesystem)    |
| `copy`     | Full file copies (maximum compatibility, uses more disk space) |

**Priority**: A profile-level `link_method` (in the profile's YAML) takes precedence over the per-game `link_method` in `games.yaml`, which takes precedence over `default_link_method` in `config.yaml`. If none is set, defaults to `symlink`. An explicit `--method` flag (e.g. `lmm deploy --method`) beats all three. See [Configuration reference](docs/configuration.md) for details, including an upgrade note for profiles saved before v1.14.1.

`lmm status -g <game>` shows the effective method for the active profile, marked `(per-profile)` or `(per-game)`; with no override anywhere the line appears only under `--verbose`, marked `(global default)`. In `--json` output, `link_method` reports the game-level resolution (game override or global default, unchanged for compatibility), while `effective_link_method` and `link_method_source` (`profile`, `game`, or `global`) report what a deploy into the active profile actually uses.

### Cache Path Priority

The mod cache location is determined by:

1. Per-game `cache_path` in `games.yaml` (if set)
2. Global `cache_path` in `config.yaml` (if set)
3. Default: `<data>/cache/` (`~/.local/share/lmm/cache/` unless `XDG_DATA_HOME` is set)

This allows you to store different games' mods on different drives (e.g., large games on HDD, frequently accessed games on SSD).

## Custom Sources

In addition to built-in mod sources (NexusMods, CurseForge), lmm lets you declare custom sources in YAML files instead of writing code. Three types are fully implemented: `directory` (a local folder of mods), `manifest` (a JSON/YAML mod list you publish, over `https://` or as a local file), and `api` (a GET+JSON REST API described declaratively) — all three work from `search`/`install`/`update` like any built-in source (within each type's capabilities), and `manifest`/`api` sources also support optional API-key authentication. Because `lmm search` queries every source configured for a game concurrently by default (see [Search](#search)), a game mapping several of these alongside NexusMods/CurseForge surfaces results from all of them in one query.

Custom source definitions are loaded from `<config>/sources/*.yaml` (or `*.yml`). Each file must define exactly one source. Broken definition files are skipped with a warning — they never prevent lmm from starting.

### Source Definition Format

```yaml
id: donovan-mods # required; must match ^[a-z0-9-]+$ and be unique
name: Donovan's 7D2D Modlets # required display name
type: directory # required: directory (local folders) | manifest | api
allow_http: false # optional; permit http:// URLs (default false)

# Type-specific configuration (one block required, must match type)
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

### Common Fields

| Field        | Type    | Required | Description                                                                                                                                                                                                                          |
| ------------ | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `id`         | string  | yes      | Unique source identifier; must contain only lowercase letters, numbers, and hyphens                                                                                                                                                  |
| `name`       | string  | yes      | Display name shown in source lists and commands                                                                                                                                                                                      |
| `type`       | string  | yes      | Source type: `directory`, `manifest`, or `api`. All three are fully supported, each within its own capabilities (see the sections below; `api` in particular can be install-by-ID-only if its definition omits a `search` endpoint). |
| `allow_http` | boolean | no       | If `true`, allow unencrypted http:// URLs (default `false`, HTTPS only)                                                                                                                                                              |

### Directory Sources

A `directory` source scans a local folder every time it's queried — no indexing, no caching of the listing — so edits to the folder show up immediately without restarting lmm. Each entry directly under the configured path becomes one mod:

- A **subdirectory** is a mod whose contents are used as-is.
- A **`.zip` or `.jar` file** is a mod whose archive is extracted like any downloaded mod.
- Anything else (loose files, `README.md`, `LICENSE.md`, other subfolders that aren't mods, etc.) is ignored by the scan but still shows up as a listed entry if it happens to be a directory — see the note on metadata fallback below.
- **Dot-prefixed entries** (`.git`, `.DS_Store`, dotfiles, ...) are always ignored, whether they're a directory or a file.

Because every non-hidden subdirectory becomes an entry, point `directory.path` at a folder dedicated to mods (as in the example below) rather than something like a repository root that also holds unrelated project files — those would otherwise show up as listed (if harmless) entries in `search`/`lmm source list` output.

```yaml
id: donovan-mods
name: Donovan's 7D2D Modlets
type: directory
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

**Metadata resolution** — for each subdirectory, lmm resolves name/version/summary/author in this order:

1. **`ModInfo.xml`** (7 Days to Die's mod metadata format), if present. Both layouts are supported:
   - **V2**: fields directly under `<xml>` — `<xml><Name value="..."/><Version value="..."/>...</xml>`
   - **V1**: fields nested in `<ModInfo>` — `<xml><ModInfo><Name value="..."/>...</ModInfo></xml>`
2. **Dirname parsing**, if no `ModInfo.xml` (or it fails to parse): the directory name is split into a name and version, e.g. `PlainMod-0.5` → name `PlainMod`, version `0.5`. If no version-like suffix is found, the whole name is used as-is and the version is empty.

Archive files (`.zip`/`.jar`) get the same metadata resolution: lmm looks for `ModInfo.xml` inside the archive (at its root or exactly one directory deep, e.g. `donovan-aio.zip` containing `donovan-aio/ModInfo.xml`) before falling back to dirname-style parsing on the filename.

**The mod ID is the directory (or archive) name, verbatim.** There is no separate ID field — `BiggerBackpack/` is mod `BiggerBackpack`. This means **renaming the directory creates a new mod identity**: lmm has no way to know `BiggerBackpack/` and `Bigger-Backpack/` are the same mod, so a rename shows up as the old mod disappearing (update checks silently stop finding it) and a new, unrelated mod appearing. Keep directory names stable once you've installed from them.

Directory sources support search, file listing, downloads (via local copy, no network), and update checks. They do not support dependency resolution (`GetDependencies` returns "not supported") since there's no manifest to declare dependencies from.

To use a directory source with a specific game, map it under that game's `sources:` block in `games.yaml` (the value is ignored — directory sources apply to any game that maps them — but the key must be present):

```yaml
games:
  7daystodie:
    sources:
      nexusmods: 7daystodie
      donovan-mods: "" # directory sources ignore this value
```

### Manifest Sources

A `manifest` source treats a JSON or YAML document you publish — an `https://` URL, or a local file path — as a full mod list: search, install, within-source dependency resolution, and update checks all work against it, the same as a built-in source.

```yaml
id: my-repo
name: My Mod Repo
type: manifest
manifest:
  url: https://example.com/mods.yaml # https:// URL, or a local path (~ expanded)
  refresh: 15m # optional cache TTL for remote URLs (default 15m)
```

- **Remote URLs** (`https://...`) are fetched on demand and cached in memory for `refresh` — a Go duration string like `30s`, `15m`, or `2h` (default `15m` when omitted). **Local file paths** are read fresh on every operation instead of being cached, so edits show up immediately.
- Fetch/parse problems (unreachable URL, malformed document, unsupported `version`) surface as an operation error naming the source and the manifest URL, at the point something actually uses the source. This is different from a broken _definition_ file, which is caught at load time and skipped with a warning before lmm ever starts (see above).
- `https://` is required for the manifest `url`, and for every file `url` inside the document, unless the definition sets `allow_http: true`; local paths are exempt.
- Remote manifest fetches are bounded by a 30-second timeout, so a hung server can't block other operations indefinitely.

The manifest document itself:

```yaml
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [baldursgate3] # matched against this source's mapped `sources:` value
    url: https://example.com/mods/cool-mod # optional web page
    updated_at: 2026-07-01T00:00:00Z # optional, RFC 3339
    dependencies: [other-mod] # optional, IDs of other mods in this manifest
    files:
      - id: main
        name: Main File
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 123456
        url: https://example.com/files/cool-mod-1.2.0.zip
        sha256: <hex digest> # optional; verified on download if present
        primary: true
```

`version: 1` is the only manifest version lmm understands today; any other value is rejected.

**`mods[]` fields:**

| Field          | Type     | Required | Description                                                                                                                                                                                                                                       |
| -------------- | -------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`           | string   | **yes**  | Unique mod ID within this manifest; also its dependency-reference ID                                                                                                                                                                              |
| `name`         | string   | **yes**  | Display name                                                                                                                                                                                                                                      |
| `version`      | string   | no       | Compared against installed versions for update checks                                                                                                                                                                                             |
| `author`       | string   | no       | —                                                                                                                                                                                                                                                 |
| `summary`      | string   | no       | Shown in search results                                                                                                                                                                                                                           |
| `game_ids`     | []string | no       | Restricts the mod to specific games, matched against the value that game maps for this source under its `sources:` block in `games.yaml` (same convention as NexusMods/CurseForge IDs); omitted or empty matches every game that maps this source |
| `url`          | string   | no       | Web page for the mod (informational only)                                                                                                                                                                                                         |
| `updated_at`   | string   | no       | RFC 3339 timestamp; an unparseable value is silently treated as unset rather than an error                                                                                                                                                        |
| `dependencies` | []string | no       | Other mods' `id`s within this same manifest; resolved automatically like NexusMods dependencies                                                                                                                                                   |
| `files`        | []object | no       | Downloadable files for this mod, see below                                                                                                                                                                                                        |

**`files[]` fields:**

| Field      | Type    | Required | Description                                                                                                               |
| ---------- | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------- |
| `id`       | string  | **yes**  | File ID, used to request a download                                                                                       |
| `filename` | string  | **yes**  | Name given to the downloaded/cached file                                                                                  |
| `url`      | string  | **yes**  | Download URL (`https://` unless `allow_http: true`)                                                                       |
| `name`     | string  | no       | Display name                                                                                                              |
| `version`  | string  | no       | —                                                                                                                         |
| `size`     | integer | no       | Size in bytes                                                                                                             |
| `sha256`   | string  | no       | Hex-encoded SHA-256 checksum; when present, lmm verifies it after download and **aborts the install if it doesn't match** |
| `primary`  | boolean | no       | Marks the default file when a mod publishes more than one                                                                 |

To use a manifest source with a game, map it under that game's `sources:` block in `games.yaml`, the same as any built-in source — the mapped value should match the IDs used in the manifest's `game_ids` (unlike `directory` sources, this value is not ignored):

```yaml
games:
  baldurs-gate-3:
    sources:
      nexusmods: baldursgate3
      my-repo: baldursgate3
```

### API Sources

An `api` source describes a GET+JSON REST API declaratively — endpoint URL templates plus JSON dot-path mappings — and lmm calls it directly: search, install, and update checks all work without writing a client. Every endpoint is optional; a definition with only enough endpoints to fetch and download a mod by a known ID (no `search`) is a valid "install-by-ID-only" source.

```yaml
id: esoui
name: ESOUI
type: api
api:
  base_url: https://api.example.com
  page_start: 1 # optional; first page number the API expects (default 1)
  auth: # optional, same block as manifest sources
    api_key:
      in: header # "header" or "query"
      name: X-API-Key
    validate: # optional (api sources only): the live key check `lmm auth login` runs
      method: GET # optional: GET (default), HEAD or POST — nothing that could mutate state
      path: /me # required: appended to base_url, like an endpoint path
      status: 200 # optional: the exact status a valid key must produce (default: any 2xx)
      field: user.id # optional: a JSON dot-path that must be present in the response
  endpoints: # each endpoint is optional; an undefined one is a capability gap (see below)
    search:
      path: /mods?game={game_id}&q={query}&page={page}&limit={page_size}
      list: results # required: dot-path to the results array
      total: pagination.total # optional: dot-path to a total-count field
    get_mod:
      path: /mods/{mod_id}
    mod_files:
      path: /mods/{mod_id}/files
      list: files # required: dot-path to the files array
    download_url:
      path: /files/{file_id}/download
      field: url # required: dot-path to the URL string in the response
    dependencies: # declaring this is what enables dependency resolution for the source
      path: /mods/{mod_id}/dependencies
      list: requires # required: dot-path to the dependency array
  mappings:
    mod: # domain field -> JSON dot-path
      id: id
      name: name
      version: latest_version
      author: author.name
      summary: description
      downloads: download_count
      updated_at: updated # RFC 3339 expected; unparseable is left unset
      url: web_url
    file: # domain field -> JSON dot-path
      id: id
      name: title
      filename: file_name
      version: version
      size: size_bytes
    dependency: # one entry of the dependencies list -> a mod reference
      mod_id: id # required when endpoints.dependencies is defined
      source_id: source # optional; defaults to this source's own id
      version: min_version # optional
```

**Placeholders** — every `{placeholder}` in an endpoint's `path` is substituted with a URL-escaped value before the request is made; a placeholder with no value for that request is left in the URL as-is:

| Placeholder   | Value                                                                                                                                    | Used by                                                          |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `{game_id}`   | The current game's ID for this source (from the search query, the mod being fetched/installed, or an installed mod during update checks) | `search`, `get_mod`, `mod_files`, `download_url`, `dependencies` |
| `{query}`     | The search text                                                                                                                          | `search`                                                         |
| `{page}`      | The internal 0-based page number, plus `page_start` (default `1`)                                                                        | `search`                                                         |
| `{page_size}` | The requested page size (defaults to 20 when unspecified or ≤ 0)                                                                         | `search`                                                         |
| `{offset}`    | The internal 0-based page × `page_size` — independent of `page_start`, for offset-paginated APIs                                         | `search`                                                         |
| `{category}`  | The search query's category filter (NexusMods: a name, CurseForge: an id), empty when unset                                              | `search`                                                         |
| `{tags}`      | The search query's tag filters, comma-joined, empty when unset                                                                           | `search`                                                         |
| `{mod_id}`    | The mod ID                                                                                                                               | `get_mod`, `mod_files`, `download_url`, `dependencies`           |
| `{file_id}`   | The file ID                                                                                                                              | `download_url`                                                   |

**`mappings.mod` keys** (`id` and `name` are required; every other key is optional and left at its zero value when unmapped or the path doesn't resolve):

| Key           | Required | Domain field                                           |
| ------------- | -------- | ------------------------------------------------------ |
| `id`          | **yes**  | Mod ID                                                 |
| `name`        | **yes**  | Display name                                           |
| `version`     | no       | Compared against installed versions for update checks  |
| `author`      | no       | —                                                      |
| `summary`     | no       | Shown in search results                                |
| `description` | no       | Falls back to `summary` when unmapped or empty         |
| `downloads`   | no       | Download count                                         |
| `updated_at`  | no       | RFC 3339 timestamp; unparseable is silently left unset |
| `url`         | no       | Web page for the mod                                   |
| `picture_url` | no       | Main image URL                                         |

**`mappings.file` keys** (`id` is required only when `mod_files` is defined):

| Key        | Required                       | Domain field                             |
| ---------- | ------------------------------ | ---------------------------------------- |
| `id`       | **yes** (when `mod_files` set) | File ID, used to request a download      |
| `name`     | no                             | Display name                             |
| `filename` | no                             | Name given to the downloaded/cached file |
| `version`  | no                             | —                                        |
| `size`     | no                             | Size in bytes                            |

**`mappings.dependency` keys** (`mod_id` is required only when `dependencies` is defined):

| Key         | Required                          | Domain field                                           |
| ----------- | --------------------------------- | ------------------------------------------------------ |
| `mod_id`    | **yes** (when `dependencies` set) | The required mod's ID                                  |
| `source_id` | no                                | The source the dependency lives in (default: this one) |
| `version`   | no                                | Minimum/required version, recorded on the reference    |

Unknown keys anywhere in `mappings.mod`, `mappings.file` or `mappings.dependency` fail validation at load time (typo detection) instead of silently mapping to nothing.

**Capability gaps** — an endpoint you don't define makes the corresponding operation report "not supported" instead of failing at load time:

- no `search` → searching is unsupported (a valid install-by-ID-only source; probe one with `lmm source validate --probe --id <mod-id>`, see below)
- no `get_mod` → fetching a single mod is unsupported, and so are update checks (`api` sources check for updates by calling `get_mod` on each installed mod and comparing versions)
- no `mod_files` → listing a mod's files is unsupported, and so is the `versions` capability (per-file version→file resolution, used by `install --version` and profile version convergence)
- no `download_url` → resolving a download URL is unsupported
- no `dependencies` → dependency resolution is unsupported, and lmm installs the source's mods on their own

`lmm source list`'s `CAPABILITIES` column reflects exactly this: a definition with only `get_mod` shows `updates`; adding `search` adds `search` to that list; `auth` appears only when the definition declares an `auth` block; `versions` appears once `mod_files` is defined; `deps` appears once `dependencies` is defined. That `versions` flag only advertises the endpoint's presence, though — whether `install --version` can actually resolve a given mod depends on whether the files that mod's `mod_files` call returns carry version info, checked dynamically per call (see `install --version`'s own entry below).

**Guardrails:**

- Mod-data requests are `GET` only, and only JSON responses are understood — no GraphQL or scraping. (`auth.validate` may declare `HEAD` or `POST` for its own probe, which fetches no mod data.)
- `api.base_url` must be `https://` unless the definition sets `allow_http: true` (same rule as `manifest` sources).
- Every request is bounded by a 30-second timeout.
- Responses are capped at 10 MiB; a larger response fails the operation instead of being read into memory.

**Key validation** — an `api` source that declares `auth.validate` is checked **live** by `lmm auth login <id>` (and by the web UI's authentication screen) before the key is stored, exactly as NexusMods and CurseForge are. The probe request goes to `base_url` + `path`, carrying the candidate key the same way every other request to that source carries it, and the key passes when the response matches `status` (or, with no `status`, is any 2xx) and — when `field` is set — contains that JSON dot-path. `field` exists for an API that answers `200` with an anonymous document rather than refusing outright. `401`/`403` mean the key was rejected; anything else means the service could not answer, which is reported as a failure to check rather than a bad key, and the response body is never quoted back (the one thing a rejection body might echo is the key itself). A definition **without** `auth.validate` keeps the old behaviour: the key is stored unvalidated and exercised on first use, and `lmm auth login` says so instead of claiming it authenticated.

**Dependencies** — an `api` source resolves mod dependencies only when it declares `endpoints.dependencies` and `mappings.dependency`; without them the source reports no dependency capability and lmm installs its mods on their own, exactly as before. Each entry of the endpoint's `list` becomes one mod reference: `mod_id` is required, `version` is optional, and `source_id` is optional and defaults to **this** source — a self-contained catalogue needs only `mod_id`. A dependency naming another source is resolved against that source when the game maps it, and reported as a missing dependency when it does not.

**Credentials** — `api` sources use the same `auth.api_key` block as `manifest` sources (see [Authentication](#authentication) below): the resolved key is attached to every API request per `in: header` / `in: query`. For downloads, both header- and query-mode keys are only sent when the URL returned by `download_url` shares scheme and host with `api.base_url` — an endpoint that hands back a third-party CDN URL never receives the source's key, in either form. If a download is redirected to a different scheme or host, a header-mode key is stripped before the redirect is followed (the same v1.8.0 machinery `manifest` sources use).

### Authentication

A custom source can require an API key, attached to every request as either a header or a query parameter. Today this is available to `manifest` and `api` sources (`directory` sources need no auth):

```yaml
manifest:
  url: https://example.com/mods.yaml
  auth:
    api_key:
      in: header # "header" or "query"
      name: X-API-Key # header name, or query parameter name, the key is sent as
```

- **Key resolution**, checked in order:
  1. The `LMM_<ID>_API_KEY` environment variable, with the source's `id` uppercased and `-` replaced by `_` (source `my-repo` → `LMM_MY_REPO_API_KEY`).
  2. A key saved with `lmm auth login <id>` — this works for any registered source whose definition declares `auth`, not just NexusMods/CurseForge, and stores the key in the same local token store. An `api` source that also declares `auth.validate` has its key checked live before it is stored (see [API Sources](#api-sources)); every other custom source stores it unvalidated and exercises it on first use.

  When both exist the environment variable wins, and `lmm auth status` (and the web UI's Auth card) say so: the row names the variable and adds `(stored key present, shadowed by $VAR)`, so the answer to "which key is lmm sending?" is always the key it is really sending (#356). The stored key is left alone — unset the variable, or `lmm auth logout <id>` to drop the stored one.

- The resolved key is always attached to the manifest fetch itself (the request for the mod list document); for `api` sources, it's attached to every request built from an `endpoints.*.path` template (search, get_mod, mod_files, download_url).
- File downloads follow the same same-origin rule regardless of whether the key is `in: header` or `in: query`:
  - **Remote manifests** (`https://` URL): the key (as a header, or appended to the URL) is only sent to file downloads whose scheme and host match the manifest URL's — a manifest pointing files at a third-party CDN never receives the source's key, in either form.
  - **Local-file manifests**: the key is attached to every file download regardless of host, since a local manifest is user-authored and already trusted.
  - **`api` sources**: the key is only sent to a `download_url` response whose scheme and host match `api.base_url`'s — see [API Sources](#api-sources) above.
- If a file download is redirected to a different scheme or host, an `in: header` key is stripped before the redirect is followed — Go's HTTP client otherwise forwards custom headers across redirects even when it would strip `Authorization`/`Cookie`.
- Keys are never printed or logged; `lmm source list` only reports whether one is configured (`AUTH` column: `yes` / `no` / `n/a`), and `lmm auth status` masks stored keys to their first/last 3 characters (keys of 8 characters or fewer are fully masked). `lmm auth status` also lists any registered custom source whose definition declares `auth`, alongside the built-in nexusmods/curseforge rows, plus any stored token whose source is no longer registered (with a hint to remove it). `lmm auth logout <id>` removes a stored token even if the source's definition file has since been removed.

### Source Management Commands

List sources (built-in and custom). With a resolvable game (`-g`, or a default set via `lmm game set-default`), the list scopes to that game's configured sources by default:

```bash
lmm source list
```

Output:

```text
ID            NAME                    TYPE       AUTH  CAPABILITIES                       ERROR
nexusmods     Nexus Mods              built-in   yes   search,deps,updates,auth,versions
donovan-mods  Donovan's 7D2D Modlets  directory  n/a   search,updates
```

Pass `--all` to see every registered source regardless of what the active game has configured, with an `IN USE` column marking which ones the active game maps:

```bash
lmm source list --all
```

Output:

```text
ID            NAME                    TYPE       AUTH  CAPABILITIES                       IN USE  ERROR
nexusmods     Nexus Mods              built-in   yes   search,deps,updates,auth,versions  yes
curseforge    CurseForge              built-in   yes   search,deps,updates,auth,versions  no
donovan-mods  Donovan's 7D2D Modlets  directory  n/a   search,updates                     yes
my-repo       My Mod Repo             manifest   no    search,deps,updates,auth,versions  no
esoui         ESOUI                   api        no    search,updates,auth                no
```

With no game resolvable (no `-g`, no default game set), `--all` has no effect: the full registry is shown either way, with no `IN USE` column, exactly as when no game exists at all. Definitions that failed to load are always shown, in every view, as an `error` row. `--json` follows the same scoping; the `"in_use"` key is only ever present in the `--all`-with-game-resolvable combination.

Validate a source definition file before use:

```bash
lmm source validate ~/.config/lmm/sources/my-source.yaml
```

On success:

```text
~/.config/lmm/sources/my-source.yaml: valid (directory source "my-source")
```

On error (exits with code 1):

```text
Error: invalid definition: id "my-bad-source!" must match ^[a-z0-9-]+$
```

Add `--probe` to also perform a live smoke test — a directory scan, a manifest fetch+parse, or an API call, depending on the definition's `type`:

```bash
lmm source validate --probe ~/.config/lmm/sources/my-source.yaml
```

For an `api` definition with no `search` endpoint (install-by-ID-only), pass `--id` with a known mod ID so `--probe` has something to call `get_mod` with. Captured against a local test definition (a `get_mod`-only `api` source pointed at a throwaway local server):

```text
$ lmm source validate --probe --id 42 demo-api.yaml
demo-api.yaml: valid (api source "demo-api")
probe: ok — get_mod 42 returned "Cool Mod"
```

Without `--id` on a search-less `api` definition, `--probe` fails with a clear message instead of silently doing nothing:

```text
Error: probe: this definition has no search endpoint; provide a known mod id with --id to probe get_mod
```

### Adding a Custom Source

1. Create `~/.config/lmm/sources/` if it doesn't exist:

   ```bash
   mkdir -p ~/.config/lmm/sources
   ```

2. Create a YAML file with your source definition:

   ```bash
   cat > ~/.config/lmm/sources/my-mods.yaml <<'EOF'
   id: my-local-mods
   name: My Local Mods
   type: directory
   directory:
     path: ~/projects/mods
   EOF
   ```

3. Validate the definition:

   ```bash
   lmm source validate ~/.config/lmm/sources/my-mods.yaml
   ```

   Steps 2 and 3 have a one-command form: `lmm source add ./my-mods.yaml`
   validates a definition and installs it under the config directory's
   `sources/` folder, refusing an id a built-in already owns or a
   definition that cannot be constructed. `lmm source remove <id>` takes
   one out again, and refuses while any configured game still maps it,
   naming those games.

4. Map it under the game(s) that should use it in `games.yaml` (see [Directory Sources](#directory-sources) above):

   ```yaml
   games:
     baldurs-gate-3:
       sources:
         nexusmods: baldursgate3
         my-local-mods: ""
   ```

5. Search and install from it like any built-in source:

   ```bash
   lmm search bigger -g baldurs-gate-3 --source my-local-mods
   lmm install --source my-local-mods --id BiggerBackpack -g baldurs-gate-3
   ```

A `directory` source now shows up with real capabilities in `lmm source list` (`search,updates`, `auth=n/a`), and it will show as an `error` row if the configured path is missing or not a directory. A `manifest` source shows `search,deps,updates,versions` (plus `auth` if the definition declares one, with the `AUTH` column reporting `yes`/`no` once a key is or isn't configured). An `api` source shows only the capabilities its defined endpoints provide — `updates` alone for a `get_mod`-only definition, `search,updates` once a `search` endpoint is added, plus `auth` if the definition declares one, plus `versions` once a `mod_files` endpoint is defined — and never `deps` (dependency resolution isn't supported for `api` sources). Any type will show as an `error` row if construction fails (e.g. a directory source's path doesn't exist). A definition whose `id` collides with an already-registered source (a built-in, or another definition) also produces an `error` row (`id already in use`); the source that was already registered keeps its original row and type unchanged.

## Web UI (`lmm serve`)

`lmm serve` starts a local, browser-based UI over the same database and
profiles the CLI uses — no separate install, no separate config, and (by
default) nothing reachable beyond your own machine:

```bash
lmm serve
# lmm serve listening on http://127.0.0.1:7420/
```

It opens your default browser automatically; add `--no-open` to skip that
and open the printed URL yourself, or `--addr` to bind somewhere other than
the default `127.0.0.1:7420`:

```bash
lmm serve --addr 127.0.0.1:8080
lmm serve --no-open
```

The CLI stays usable while it runs, and the two agree: `lmm serve` re-reads
`games.yaml` whenever it changes, so a game you add, edit or remove with
`lmm game add`/`edit` in another terminal shows up in the browser on the
next request — no restart. Stored credentials are the one exception: a key
saved after the server started needs a restart before the server can use
it, and the Setup page says so when that happens.

It is a single-page application: one small shell, then everything happens
in place. It is desktop-first: it needs JavaScript and a current desktop
browser, and there is no small-screen layout yet — on a phone, reach for the
CLI over SSH, which drives the same flows. It still installs nothing: Preact and htm are vendored in the
repo at pinned versions and embedded in the binary, so `go build` remains
the entire build and the UI never fetches anything from the network.

### Screens

**Mission Control** (`/g/{game}/{profile}`) is home. Its top bar carries the
game and profile pickers, the undeployed-changes indicator and **Deploy**,
the omnibar, the activity bell, **⚙ Setup** and the theme toggle. Beneath
it, attention cards render only when they have something to say, and each
acts in place:

| Card          | What it shows                                                                                                          | What it does                                                                                                                                                             |
| ------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Updates**   | every mod with a newer version                                                                                         | tick rows → "Update selected" applies them as one batch                                                                                                                  |
| **Health**    | `lmm verify`'s findings, and when it last ran (or "Unchanged since …" when the answer came from the verify memo, #336) | per-finding **Repair**, **Repair all**, **Re-verify** (a real re-run, never the memo); a finding that `verify --fix` would not attempt says so in the engine's own words |
| **Conflicts** | each contested file, its contenders and the winning rule                                                               | **Resolve…** opens the reorder modal scrolled to that file                                                                                                               |
| **Profile**   | which way the profile and the installed set have drifted                                                               | **Apply profile…** runs `lmm profile apply`; **Sync…** runs `lmm profile sync`                                                                                           |

The **library** is the spine: an enabled toggle, the name, the installed
version (with its update target), badges (⬆ update, ⚠ health, ⇄ conflict,
🔒 lock, update policy), load order, and a ⋯ menu per row
(Update / Uninstall / Lock / pak conversion / Re-link… / Reorder here).
Pak conversion appears only where it applies. Filter (all/enabled/updatable/
unhealthy) and sort (load order/name/recently installed) narrow it, and
selecting rows raises a batch bar (Enable / Disable / Uninstall / Update
selected). More columns appear as the display widens: author and install
date at 1440px, source and link method at 1920px. Its toolbar also carries
**Add mods ▾**: Search sources… (focuses the omnibar), Import an archive…
and Adopt untracked mods… — the same three flows, and the same component,
the empty-library state offers before you have installed a first mod.

Below the library sits the **Snapshots** card, which — unlike the attention
cards — renders whether or not it has anything to show, because its value is
knowing the safety net is there. It lists the game's snapshots newest first
(with what each recorded and how much a restore would put back), and each
row carries **Restore…** (through the same confirm-plan modal every other
mutation uses, with the refusals up front) and **Delete** (confirmed in
place on the row, and it says what it keeps: the stored originals). **Snapshot
now** records one with no name to type — see [Snapshots](#snapshots).

Clicking a row opens the **slide-over**: author, installed → available
version, an editable lock, update policy and (where it applies) pak
conversion, that mod's own findings and conflicts, a changelog preview, and
Update / Enable-or-Disable / Uninstall. **More info →** opens the **full mod
page** (`/g/{game}/{profile}/mod/{source}/{id}`), which carries all of that
plus what only it has room for: full description, complete changelog, a
files table, a versions table with per-version install and rollback,
**Re-link…** (`lmm mod edit`), dependencies, and that mod's own job history.

Every confirm-plan modal has an **Advanced** section holding that command's
own flags — `install --show-archived` / `--no-deps` / `--skip-verify`,
`uninstall --keep-cache`, `deploy <mod-id>` / `--method` / `--purge` /
`--all`, `--force` and the global `--no-hooks`. Flags that change what the
plan SAYS re-compute it, so the preview always describes the mutation
Confirm will submit.

The **Setup page** (`/g/{game}/{profile}/setup`) holds everything
administrative, in five sections: **Games** (the configured games table with
its sources, **Edit sources…** per row, Steam detection, manual add,
set/clear the default), **Authentication**
(per-source status, log in and out, the environment variable each source
reads shown beside its field, orphaned-token removal), **Custom sources**
(a line-numbered YAML editor with validate-then-save and an optional live
probe, delete, download), **Archive import** (upload an archive, optionally link it to a
source and mod id, then confirm), and **Adopt** (scan the game folder for
untracked mods, preview, confirm). With no games configured yet, `/` is the
first-run flow and shares those same detect/add forms — **and the
custom-source editor**, because a game can only be added against a source
that already exists and nothing about defining one is game-scoped.

Steam detection lists every installed game it finds, not only the curated
ones: a curated row is badged **Known** and adds by ticking its checkbox
same as always, while a game lmm has no known-games entry for is shown
beside it — never hidden — with an **Add with details…** action instead of
a checkbox (it has no scan index to select with). That opens the manual add
form pre-filled from the scan: the display name, a read-only install path,
and the Advanced section's game id, all as the scan reported them; the mod
path field shows `<install path>/mods` as a placeholder guess, editable
like any other field. Picking a mod source then auto-searches its catalog
by the detected game's own name and highlights a single exact-name match
without submitting anything — the same suggestion `lmm game add
--from-detected` makes non-interactively, just requiring a click here
instead of taking it silently. The manual add form also gets its own **Pick
an installed game…** control — the identical picker and prefill, reachable
without going through the detect list first, for when you already know you
want the manual form. Either path submits with `from_steam_app_id` plus
only the fields you actually changed; the browser never invents a slug, a
mod path or a source map of its own. On a **curated** (Known) row that is
the whole form: Submit is live on the app id alone, the form names the
source map the known-games list will supply, and the source fields become
an optional override you can layer on top — the same thing `lmm game add
--from-detected 489830` does with no further arguments. An uncurated row
still needs a source and identifier, because nothing on disk supplies them. If the scan that offered a game goes
stale (it was uninstalled between the scan and the submit), the form says
so by name and offers a **Rescan** rather than a dead end.

### Flows

**Every mutation works the same way**, and it is the CLI's own way: the plan
is computed and shown to you first — the exact mods, files, hooks and paths
`--dry-run` would print — and confirming runs it as a background job. The
control you clicked then _becomes_ that job's progress, with live phase
text and byte counters, and its outcome resurfaces in the same place. A job
runs under the server's own context, so closing the tab never interrupts
one.

The **activity bell** collects every job of the session: running (with
progress), queued, failed (with its next step right there — an install
refused for a file conflict offers **Overwrite?**), and recently finished.
Each entry expands to that job's own phase-by-phase event stream. A
completion whose control is no longer on screen arrives as a toast instead.

**Searching**: type in the omnibar to narrow your library as you go; press
**Enter** to fan the same text out to the game's configured sources and
append the results below your library, installable in place, with a version
picker where the source offers more than one. A source that fails to answer
shows a warning row beside whatever did — never in place of it. A **✕**
appears in the omnibar the moment it holds text; click it (or press **Esc**
while the field has focus) to clear the search, drop the fanned-out results
and return to your plain library, without losing focus. For heavier
browsing, the **search page** (`/g/{game}/{profile}/search?q=…`) adds source
badges, download counts, summaries, category and source filters, a tag
filter where a source honours one (`lmm search --tag`), sort and
pagination.

**Switching profiles**: picking a profile's _name_ in the profile picker
only changes what you are looking at. **Switch and deploy…** beside it runs
the real `lmm profile switch` — undeploy what the old profile deployed,
deploy what the new one lists, move the game's active profile — behind the
same plan-and-confirm every other mutation uses.

**Modals** stack at most one deep: the confirm-plan modal, the **reorder**
modal (drag a row — a ghost follows the pointer and a dashed outline marks
where it will land, since the drag itself is plain mouse events with no
browser-provided drag image — or use its ↑ ↓ First Last buttons, with a live
"current vs proposed winner" preview per contested path before Save
commits), the **profiles** modal (create, rename, delete, set default,
export, import, plus per-profile **Sync…** and **Purge…**), the **keyboard
shortcuts** help (`?`), and a batch-uninstall confirm.

**Purge** is the one mutation that asks for more than a click: its confirm
step keeps **Purge** disabled until you type the profile's own name back,
because it undeploys the whole profile and, with its own option set,
removes every mod record behind it.

### Keyboard

The whole UI is operable from the keyboard, and every focused control shows
a visible ring in both themes.

| Key                        | Where                                        | What it does                                                                                                   |
| -------------------------- | -------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `Tab` / `Shift+Tab`        | anywhere                                     | move through the controls; the first stop on every screen is **Skip to content**, which jumps past the top bar |
| `?`                        | anywhere outside a text field                | open this keyboard-shortcuts help                                                                              |
| `Enter`                    | omnibar                                      | search the game's sources for what you typed                                                                   |
| `Esc`                      | omnibar                                      | clear the search and return to your plain library                                                              |
| `Esc`                      | any modal, the slide-over, any open dropdown | close it and return focus to whatever opened it                                                                |
| `←` / `→`                  | the slide-over                               | step to the previous/next mod in the library's current order                                                   |
| `←` / `→` / `Home` / `End` | the Setup page's section tabs                | move between sections                                                                                          |

The same table is in the app itself: press `?` (or the **?** button beside
**⚙ Setup**) to open it. It is generated from one list, so the two cannot
drift.

Focus is contained inside a modal and the slide-over while either is open,
so `Tab` cannot wander onto the page behind the scrim. If your system asks
for reduced motion, every animation is disabled.

Every view names itself: the browser tab, your history and your window
switcher show the screen and the game/profile it is showing (for example
`Mission Control — baldurs-gate-3/default · lmm`), and a screen reader is told
the new view's name on each navigation.

### URLs

```text
/                                        game chooser
/g/{game}/{profile}                      Mission Control (home)
/g/{game}/{profile}/mod/{source}/{id}    a mod's full page
/g/{game}/{profile}/search?q=…           the search page
/g/{game}/{profile}/setup                the Setup page
```

The game and profile live in the **path**, not in a query parameter, so the
context you are working in cannot be lost or silently defaulted as you move
around, and any URL can be bookmarked or shared as-is. The old
query-parameter URLs (`/mods`, `/mods/{source}/{id}`, `/search`,
`/updates`, `/profiles`, `/health`, `/jobs/{id}`) permanently redirect into
this scheme, carrying whatever game/profile they resolved to; when they
resolve to nothing, they land on `/`.

### Theme

Light and dark, following your system by default, with a persisted
override you can set from the top bar. The override is remembered in the
browser's local storage and applied before the first paint, so switching
pages never flashes the wrong theme.

### `/api/v1` and Server-Sent Events

The UI is built entirely on this API, which returns exactly the documents
`lmm <command> --json` does, with the CLI's `{"error", "details"}` envelope
on failure — so anything the web UI can do, a script can do too:

```text
GET  /api/v1/status
GET  /api/v1/mods
GET  /api/v1/mods/{source}/{id}
GET  /api/v1/mods/{source}/{id}/files
GET  /api/v1/mods/{source}/{id}/versions
GET  /api/v1/search?q=&page=&page_size=&limit=&category=&source=&tag=
GET  /api/v1/updates
GET  /api/v1/profiles
GET  /api/v1/profiles/{name}/export
GET  /api/v1/health
GET  /api/v1/conflicts?order=
GET  /api/v1/games
GET  /api/v1/games/catalog?source=&q=
GET  /api/v1/games/detect?all=
GET  /api/v1/auth
GET  /api/v1/sources
GET  /api/v1/sources/{id}/definition
GET  /api/v1/snapshots
```

`GET /api/v1/conflicts?order=` is the reorder preview: a comma-separated
list of mod ids (`source:modid`, or a bare mod id where it is unambiguous)
answers "which mod would win each contested path under THIS load order",
from the same rule a real reorder applies. Leaving it off describes the
order the profile currently holds. `GET /api/v1/profiles/{name}/export`
serves the same document `lmm profile export --json` prints, as a
downloadable attachment.

Most mutations run as a Plan, then a background job:

```text
POST /api/v1/plans/{kind}       -> the plan, plus a single-use plan_id
POST /api/v1/jobs               -> {plan_id, options} -> {id}
GET  /api/v1/jobs/{id}          -> job status: running / succeeded / failed
GET  /api/v1/jobs/{id}/events   -> Server-Sent Events: live progress
```

The start response also carries `job_id`, the same value under the name it
originally shipped with. That spelling is **deprecated** — read `id`, which
is what `GET /api/v1/jobs` and `GET /api/v1/jobs/{id}` have always called it
— and it will be dropped in a later major version. (`job_id` on the event
stream is a different thing and stays: there it names the job an event
belongs to.)

`{kind}` is one of sixteen, each the browser-side twin of a CLI command:
`deploy`, `install`, `uninstall`, `updates`, `rollback`, `switch`,
`profile_apply`, `profile_import`, `profile_sync`, `purge`, `mod_relink`,
`verify_fix`, `import_archive`, `adopt`, `workshop_adopt` and
`snapshot_restore`. An unknown kind is a 400 whose details list the ones that
exist. (`mod_relink` is
`lmm mod edit`: it is named for the core flow it drives,
`PlanRelinkMod`/`ApplyRelinkMod`.)

`?tag=` on `GET /api/v1/search` is `lmm search --tag`, and is repeatable
the same way: `?tag=lore-friendly&tag=armor` narrows on both. Support
varies by source.

Two endpoints report on jobs as a whole rather than one at a time — what
the UI's activity tray is built on:

```text
GET  /api/v1/jobs               -> every retained job, newest first
GET  /api/v1/events             -> Server-Sent Events: every job's lifecycle
```

Profile management is the other set of synchronous mutations - a create, a
delete, a set-default, a rename and a reorder each write once with nothing
to preview, so like lock/policy they answer immediately with the same
document their `lmm profile ...  --json` twin prints:

```text
POST   /api/v1/mods/{source}/{id}/lock      {"version"?} -> the mod's settings
POST   /api/v1/mods/{source}/{id}/unlock               -> the mod's settings
POST   /api/v1/mods/{source}/{id}/update-policy {"policy"}
                                                       -> the mod's settings
POST   /api/v1/mods/{source}/{id}/convert   {"enabled"} -> the mod's settings
```

Those four are `lmm mod lock`/`unlock`/`set-update`/`convert`, each
answering the `core.ModSettingResult` document its `--json` twin prints.
`convert` is the pak-conversion toggle for a compile-mode game (Icarus); a
mod with no convertible `.pak` is a 400, the same refusal the CLI gives.

```text
POST   /api/v1/profiles                     {"name"}   -> the profile created
DELETE /api/v1/profiles/{name}                         -> the profile deleted
POST   /api/v1/profiles/{name}/rename       {"name"}   -> under its new name
POST   /api/v1/profiles/{name}/set-default             -> the new default
POST   /api/v1/profiles/{name}/reorder      {"ids"}    -> the new load order
```

The Setup surface — how a game and its credentials come to exist — is the
other set of synchronous mutations. None of these is game-scoped, so none
takes `?game=`:

```text
POST   /api/v1/games          {"source_id","identifier","name",
                               "install_path"[,"game_id","mod_path",
                               "from_steam_app_id","loader"]}
                                          -> the new game's `lmm game list` row
GET    /api/v1/games/{id}                 -> core.GameDetail (the row plus the
                                             mod-loader report, incl. the Steam
                                             launch option to paste)
PUT    /api/v1/games/{id}     {"sources"} -> the game's `lmm game list` row
PUT    /api/v1/games/{id}     {"loader","loader_set"}
                                          -> the game's `lmm game list` row
                                             (one request, one edit: a body
                                             carrying both is refused)
POST   /api/v1/games/{id}/set-default     -> the new default (core.SettingsResult)
DELETE /api/v1/games/default              -> the default cleared (core.SettingsResult)
POST   /api/v1/games/detect   {"select"}  -> what was added (index or slug)
POST   /api/v1/auth/{source}  {"api_key"} -> the authentication report
DELETE /api/v1/auth/{source}              -> the authentication report
POST   /api/v1/sources/validate  {"yaml"[,"probe","probe_id"]}
                                          -> the source validation report
PUT    /api/v1/sources/{id}      {"yaml"} -> the source list, re-read
DELETE /api/v1/sources/{id}               -> the source list, re-read
POST   /api/v1/snapshots      {"name"?}   -> the snapshot recorded
DELETE /api/v1/snapshots/{name}           -> {"name","game_id","deleted"}
```

The two snapshot writes are single-step for the same reason the lock and
policy writes are: there is nothing to preview and nothing to watch. An
omitted `name` gets the same date-and-time default `lmm snapshot create`
uses with no `--name`, so the UI's "Snapshot now" needs no text input; a
name already taken is a 409 (a snapshot is never silently overwritten) and
one that is not usable as a file name is a 400. A delete removes the record
and keeps the stored originals. RESTORE is the destructive, five-stage half
and is a plan kind (`POST /api/v1/plans/snapshot_restore` with
`{"snapshot":"<name>"}`), so it gets a real preview and a job.

`GET /api/v1/games` answers with the rows `lmm game list --json` prints —
an empty array is the first-run signal; each row carries the game's
`source_ids` map. `PUT /api/v1/games/{id}` rewrites that map (`lmm game
edit`'s twin) and answers with the same row: the body's `sources` object is
the FULL map the game ends up with, so an omitted source id is removed. An
id no registered source claims is a 400 whose `details.field` is
`"sources"`, an unknown game is a 404, and an empty map is refused — a game
must keep at least one source. `GET /api/v1/games/catalog` is the
game-add form's search, over any source with a searchable catalog
(CurseForge today); a source without one answers 400, which is the signal
to ask for an identifier instead. `POST /api/v1/games` answers 400 with a
`{"field","value","reason"}` details payload naming the input at fault,
and 409 when the game id is already taken; the install path must exist.
`game_id` is the local games.yaml key — omit it and it is derived from the
identifier, or pass the `game_id` a catalog match carries so a CurseForge
game is keyed `minecraft` rather than `432`. `from_steam_app_id` prefills
the whole body from an installed Steam game (the `steam_app_id` of a detect
row): the name, install path, game id, mod path and — for a game in the
known-games list — its source map all come from the scan, and every other
member becomes an override. A known installed game is therefore added with
that member alone; an unknown one needs only `source_id`/`identifier` on
top.

`GET /api/v1/games/detect` lists what a Steam scan found, each row with its
1-based index and an `already_configured` flag; the POST re-runs the scan
itself and applies the rows the request names, so the paths written to
games.yaml always come from the machine. `?all=1` widens the listing to
EVERY installed Steam game: the extra rows carry no `known` member at all
(absent = not in the known-games list; a curated row carries `"known":
true`), no sources, an empty `mod_path` and `index: 0` — they are listed, not
selectable, because nothing on disk says where they keep their mods.
(The known rows keep the same numbering either way.) Naming one in the
POST's `select` is a 400 pointing at `POST /api/v1/games` with its
`from_steam_app_id`, which is how such a game is added.

The three auth routes all answer
the document `lmm auth status --json` prints — the writes with it re-read.
A key is validated live where the source supports it and is never stored if
that check refuses it (400) or could not be performed at all (502); it never
appears in a log line, an error, or a response. A key stored or removed here
takes effect IMMEDIATELY: the affected source is rebuilt with the new
credential and swapped into the running registry, so the next search or
install uses it with no restart. (The swap waits at most a few seconds for
any in-flight mutation to finish; if it cannot get in, the key is still
stored and is picked up at the next start, and the server logs that it
was.) `GET /api/v1/games/catalog` answers 401 when the source refused for
want of a credential — distinct from the 502 every other failure of its own
call gets, so a client can offer "authenticate this source first" instead
of a generic upstream error.

The five source routes are the custom-source editor. `GET /api/v1/sources`
is the document `lmm source list --json` prints — the full registry plus
every definition that failed to load or construct — and is deliberately not
game-scoped: a source exists before any game maps it, and the editor's job
is to show every definition including the broken ones. `GET
/api/v1/sources/{id}/definition` serves a user-defined source's YAML as
`text/yaml`, comments and key order intact (404 for a built-in, which has
no definition file). `POST /api/v1/sources/validate` judges a draft that has
no file yet, with `lmm source validate --probe/--id`'s live smoke test
behind `probe`. `PUT` and `DELETE` save and remove one, and both answer with
the source list re-read — one save can change more than one row. A save
writes the file atomically and registers the source on the running server;
the path id must equal the document's id (400 otherwise), a built-in id is
409, and an invalid or unconstructable definition is 400 with the validation
report as the envelope's details. A delete refuses with 409 and a
`{"source_id","games"}` details payload while any configured game still maps
the source. `lmm source add <file>` and `lmm source remove <id>` are the
same two operations from the command line.

The profile is named in the path rather than taken from `?profile=`: these
routinely act on a profile other than the selected one. Profile IMPORT is
the exception that stays a plan (`POST /api/v1/plans/profile_import`,
body `{"data": "<the exported document>"}`), because it has a real preview:
which of its mods are already installed, which need re-downloading and which
are missing entirely.

Importing a mod ARCHIVE takes one more step, because `lmm import <archive>`
takes a path and a browser cannot hand a server one (and a server must not
accept one from a browser). The file itself travels first:

```text
POST   /api/v1/uploads        multipart/form-data, one file
                                          -> {"upload_id","filename","size"}
DELETE /api/v1/uploads/{id}               -> 204, the staged file is reclaimed
```

The upload is streamed straight into the same staging directory downloads
and extraction already use (never `/tmp`, which is tmpfs on most distros),
capped at **2 GiB**, and accepted only for an extension the extractor
handles (`.zip`, `.7z`, `.rar`). The id is opaque and is never a path. A
staged archive expires after **30 minutes**, is deleted when its import
succeeds, and is kept when its import fails so a retry does not mean
re-uploading it.

`import_archive` is then an ordinary plan kind: `POST
/api/v1/plans/import_archive` with `{"upload_id"[,"source_id","mod_id"]}`
answers with the same `core.ImportArchivePlan` `lmm import --dry-run
--json` prints — the resolved identity, the files, the conflicts, the
hooks — and the job takes
`{"accept_conflicts","force","skip_hooks"}`. `accept_conflicts` is the
Overwrite answer; `force` is a different question (it skips the conflict
check entirely and downgrades a failed install hook to a warning).

`adopt` is `lmm import`'s scan mode as a plan kind: `POST
/api/v1/plans/adopt` with `{"skip_match"}` answers with `core.AdoptPlan` —
the whole local scan, one match entry per untracked mod, and the duplicate
preview — and the job takes no options. Previewing without confirming IS
the dry run. Its job runs the metadata backfill and the adoption together
and reports both in one `core.AdoptResult` (`backfilled` alongside
`adopted`/`skipped`/`failed`).

`workshop_adopt` is `lmm import --workshop` as a plan kind: `POST
/api/v1/plans/workshop_adopt` with `{"refresh"}` (the CLI's `--refresh`,
bypassing the Workshop metadata cache) answers with `core.WorkshopAdoptPlan`
— every subscribed item lmm does not already track — and the job takes no
options at all, since the only choice this flow offers is made at plan time.

(Enable/disable are an exception: with no options and nothing to preview,
they skip the plan step entirely — `POST /api/v1/mods/{source}/{id}/enable`
and `.../disable` start the job directly and answer with the same
`{"id"}` document. Lock/unlock/update-policy skip jobs too, but for a
different reason: `POST /api/v1/mods/{source}/{id}/lock`, `.../unlock` and
`.../update-policy` are single DB writes with nothing to run in the
background at all, so they answer synchronously with the mod's full
settings snapshot — the same document `lmm mod lock`/`unlock`/`set-update
--json` print.)

The per-job events stream sends one JSON frame per typed core progress event
(`event:` names the event type), a comment heartbeat roughly every 15
seconds while a job is otherwise quiet, and a final `event: done` frame
carrying the same job status document `GET /api/v1/jobs/{id}` returns — so
a client never has to race "the stream closed" against "go fetch the final
status" separately.

`GET /api/v1/events` is the other shape: one stream for the whole session,
multiplexing every job. It opens with an `event: snapshot` frame carrying
the same document `GET /api/v1/jobs` answers with — so a client that
connects mid-deploy is caught up before it is told anything new — then
sends `event: job_started`, `event: job_progress` and `event: job_done`
frames, each naming the job it belongs to. Progress frames are summaries
(phase, mod, position, percent) rather than whole core events, and a
download's ticks are coalesced to whole percents; open the per-job stream
above for the full detail. The job index and the `job_done` frame both
carry a failed job's `{"error", "details"}` envelope, but never its result
document — read `GET /api/v1/jobs/{id}` for that.

### Security posture

There is **no authentication** in this release. `lmm serve` is meant for a
single trusted user on their own machine:

- Binds to `127.0.0.1:7420` by default. Binding a non-loopback address (a
  LAN IP, `0.0.0.0`) prints a loud warning on every startup: anyone who can
  reach that address can drive `lmm` exactly as you can.
- Every request's `Host` header is checked against an allow-list built from
  the bind address (plus `localhost`, for a loopback bind) — a
  DNS-rebinding guard. A **wildcard bind** (`0.0.0.0`, `[::]`, or a bare
  `:port`) has no single correct `Host` to pin, so rather than accept any
  `Host` unconditionally it accepts only a `Host` that is an **IP literal**
  or the name `localhost` — and, when that `Host` names a port, only if it
  matches the port actually bound. An IP literal or `localhost` can't be
  "rebound" by attacker-controlled DNS the way an arbitrary name could,
  which is what real LAN traffic to a wildcard bind normally uses anyway.
- `Origin` is checked against `Host` on every state-changing request, and a
  CSRF token — one per server process, delivered to the UI in the shell's
  `<meta name="csrf-token">` and sent back as an `X-CSRF-Token` header — is
  required for every state-changing request.
- Every response, shell or static asset, carries conservative headers
  (`X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, a
  same-origin `Referrer-Policy`, and a `default-src 'self'`
  Content-Security-Policy). The policy admits exactly one inline script —
  the theme bootstrap — and it does so by the SHA-256 of that script's own
  bytes, not by `'unsafe-inline'`. There is no `'unsafe-eval'`.
- **The Health card does not re-verify what has not changed** (#336). A
  hydrate — and the web UI hydrates on every route change, job completion
  and profile switch — asks core for the same full verify tier the CLI
  runs. Core answers from the previous run when nothing it inspects has
  moved: the profile's mods and locks, the installed rows, the recorded
  file checksums it compares against (so a `lmm verify --fix` typed in
  another terminal is noticed), and a stat-only walk (path, size,
  modification time) of the deployed tree. The card then
  says "Unchanged since …" instead of "Last verified …", and the document
  carries `cached: true` beside the original `checked_at`. **Any** lmm
  mutation drops the memo, and **Re-verify** (like `lmm verify` itself,
  which never uses the memo) forces a real run. The limit is the
  fingerprint's: a deployed file rewritten to the same size with its
  modification time preserved looks unchanged, so a memoised answer can be
  stale until a mutation or a forced run — which is exactly why a typed
  `lmm verify` always runs for real.

- **Cross-process mutations are serialized** (#317). Every lmm mutation —
  CLI or `serve` — takes an advisory `flock` on `<data dir>/.oplock` for as
  long as it holds the in-process mutation slot, so a `lmm deploy` typed
  while a `serve` job is mid-deploy cannot interleave its file operations
  with it. That is every lmm mutation, not only the ones that touch the
  game directory — `lmm auth login` during a long deploy is refused too. The second one waits up to two seconds and then refuses, naming
  the holder: `another lmm operation is in progress (pid 4242, since
2026-09-09T12:00:00Z)` — under `--json`, with `pid` and `started_at` in
  the error envelope's `details`. Reads never take the lock, so `lmm list`,
  `lmm status` and every `GET /api/v1` route keep working while a mutation
  runs. The lock is held by an open file descriptor, so a killed lmm
  releases it immediately: there is never a stale lock to clear by hand.
  Two installations (different `--data` directories) never contend.

## CLI Reference

### Global Flags

| Flag          | Short | Description                                                                                                                                     |
| ------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `--game`      | `-g`  | Game ID (optional if default set via `game set-default`)                                                                                        |
| `--verbose`   | `-v`  | Enable verbose output                                                                                                                           |
| `--config`    |       | Custom config directory                                                                                                                         |
| `--data`      |       | Custom data directory                                                                                                                           |
| `--json`      |       | Output JSON instead of text; mutating commands print their result, `--dry-run` prints the plan; never prompts — see [JSON output](#json-output) |
| `--no-hooks`  |       | Disable all hooks at runtime                                                                                                                    |
| `--no-color`  |       | Disable colored output (respects NO_COLOR env)                                                                                                  |
| `--log-level` |       | Diagnostic log level written to stderr: `off`, `error`, `warn`, `info`, `debug` (default `off`)                                                 |

Output is colorized by default whenever stdout is a terminal (headers, status accents like enabled/disabled/pinned, success/warning/error markers); piped or redirected output stays plain automatically, and `--json` output is never colored. Disable explicitly with `--no-color` or the `NO_COLOR` environment variable.

### JSON output

`--json` prints **exactly one JSON document to stdout**, 2-space indented,
with a single trailing newline and nothing else. Warnings and human notices
travel inside the document itself (its `warnings`/`notes` fields), not
stderr; stderr stays empty except for `--log-level` diagnostics, so
`lmm ... --json | jq` is always safe. Map keys and list order are
deterministic, so two runs over the same state produce byte-identical
output.

On failure the document is an envelope instead:

```json
{ "error": "game not found: baldurs-gate-3" }
```

An error that carries structured data adds a `details` object beside it;
plain errors have no `details` key at all. Either way, stdout still holds one
document and the exit code is non-zero. Three shapes populate `details`
today: a blocked `install`/`import <archive>` conflict names the colliding
files as `details.conflicts` (`[]core.Conflict`); a `game detect --json` run
that partially applied (some games saved, one failed) reports what it did
save as `details.saved`/`details.profiles` alongside the failure; and a
fatal error on `profile apply`/`switch`/`sync` after an already-printed
`UpsertMod` lock-refusal warning (Ruling 5, below) carries those warnings as
`details.warnings` since there is no result document left to carry them on.
Every type that implements this extension point is pinned by a named test
(`cmd/lmm/details_coverage_test.go`'s `TestDetailsTypesAreCovered`), so a
new one can't go undocumented.

Every document is a type from `internal/core`, `internal/domain` or
`internal/app` — never a shape invented by the CLI — and each of those types
has a recorded golden under `internal/core/testdata/json/`,
`internal/domain/testdata/json/` or `internal/app/testdata/json/` that pins
its exact wire shape. A field can only change by changing that golden, which
shows up as a diff in review.

| Command                        | Document                                                                                                                                                                                                                                                                                                                                  |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lmm list`                     | `core.ModList` — `{game_id, profile, mods[]}`                                                                                                                                                                                                                                                                                             |
| `lmm list --profiles`          | `core.ProfileNames` — `{game_id, profiles[]}`                                                                                                                                                                                                                                                                                             |
| `lmm profile list`             | `core.ProfileListing` — `{game_id, profiles[]}`                                                                                                                                                                                                                                                                                           |
| `lmm profile export <name>`    | `domain.ExportedProfile` — `{name, game_id, mods[], link_method?, overrides?, hooks, hooks_explicit}`                                                                                                                                                                                                                                     |
| `lmm status`                   | `core.StatusReport` — `{games[]}`                                                                                                                                                                                                                                                                                                         |
| `lmm status -g <id>`           | `core.GameStatus` — one game, flat                                                                                                                                                                                                                                                                                                        |
| `lmm search`                   | `core.SearchReport` — `{game_id, query, mods[], warnings[], total_results, attempted_count, page?, page_size?, has_more?}` (each of the last three is omitted when it is unset; `lmm search` always sets `page_size` from `--limit`, default 10, and never sets `page`, while `/api/v1/search` sets none of them unless the caller pages) |
| `lmm verify`                   | `core.VerifyReport` — `{game_id, profile, result{findings[], issues, warnings, …}}`; each finding carries `fixable` when `verify --fix` would attempt a repair for it                                                                                                                                                                     |
| `lmm snapshot create`          | `core.SnapshotResult` — `{name, game_id, profile, created_at, auto?, path, mods, deployed_files, originals, size_bytes}`                                                                                                                                                                                                                  |
| `lmm snapshot list`            | `core.SnapshotListing` — `{game_id, snapshots[], warnings[]}`                                                                                                                                                                                                                                                                             |
| `lmm snapshot restore`         | `core.SnapshotRestoreResult` — `{snapshot, profile, safety_snapshot?, purged, originals_restored, originals_skipped[], disabled, enabled, installed, replaced, deployed, refused[], notes[], warnings[]}`; `--dry-run` emits `core.SnapshotRestorePlan`                                                                                   |
| `lmm snapshot delete`          | `core.SnapshotDeleteResult` — `{name, game_id, deleted}`                                                                                                                                                                                                                                                                                  |
| `lmm conflicts`                | `core.ConflictReport` — `{game_id, profile, conflicts[]}`                                                                                                                                                                                                                                                                                 |
| `lmm mod show`                 | `core.ModDetail` — `{mod{…}, installed?{…}}`                                                                                                                                                                                                                                                                                              |
| `lmm mod files <mod-id>`       | `core.ModFilesReport` — `{mod{…}, files[], merged_pak_only}`                                                                                                                                                                                                                                                                              |
| `lmm source list`              | `[]app.SourceInfo` — a top-level array                                                                                                                                                                                                                                                                                                    |
| `lmm source validate <file>`   | `app.SourceValidationReport` — `{path, id?, type?, valid, errors[], warnings[], probe?}` (an invalid file/failed probe is the error envelope instead, `details` = this report)                                                                                                                                                            |
| `lmm source add <file>`        | `[]app.SourceInfo` — the full registry, re-read (an invalid definition is the error envelope instead, `details` = the validation report)                                                                                                                                                                                                  |
| `lmm source remove <id>`       | `[]app.SourceInfo` — the full registry, re-read (a source a game still maps is the error envelope, `details` = `core.SourceInUseError`'s `{source_id, games[]}`)                                                                                                                                                                          |
| `lmm game list`                | `[]core.GameListEntry` — a top-level array                                                                                                                                                                                                                                                                                                |
| `lmm game show-default`        | `core.DefaultGame` — `{set, id?, name?}`                                                                                                                                                                                                                                                                                                  |
| `lmm auth status`              | `app.AuthStatusReport` — `{sources[], orphaned[]}`                                                                                                                                                                                                                                                                                        |
| `lmm update` (bulk check)      | `core.UpdateCheckReport` — `{game_id, profile, updates[], skipped{}, error?}`                                                                                                                                                                                                                                                             |
| `lmm update <mod-id>`          | `core.UpdateApplyResult` — `{mod{}, name, from_version, to_version, status, …}`                                                                                                                                                                                                                                                           |
| `lmm update rollback <mod-id>` | `core.RollbackResult` — `{mod{}, mod_name, from_version, to_version, status, …}`                                                                                                                                                                                                                                                          |

Mutating commands emit their **result**, or - with `--dry-run` - the **plan**
that run would have applied:

| Command                                                     | Document                                                                                                                                                                                                                                        |
| ----------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lmm install`                                               | `core.InstallResult` — `{installed[], skipped[], failed[], …}`                                                                                                                                                                                  |
| `lmm import <archive>`                                      | `core.ImportArchiveResult` / `core.ImportArchivePlan` under `--dry-run` (conflicts need `--force`, above)                                                                                                                                       |
| `lmm import` (scan)                                         | `core.AdoptResult` — `{adopted, skipped, failed, backfilled?, warnings[]}` (`backfilled` is set only by a frontend that runs the metadata backfill in the same step — `lmm serve` does; the CLI reports it while rendering the scan)            |
| `lmm import --dry-run` (scan)                               | `core.AdoptPlan`                                                                                                                                                                                                                                |
| `lmm deploy`                                                | `core.DeployResult` / `core.DeployPlan` under `--dry-run`                                                                                                                                                                                       |
| `lmm uninstall <mod-id>`                                    | `core.UninstallResult` / `core.UninstallPlan`                                                                                                                                                                                                   |
| `lmm purge`                                                 | `core.PurgeResult` / `core.PurgePlan`                                                                                                                                                                                                           |
| `lmm profile apply`                                         | `core.ProfileApplyResult` / `core.ProfileApplyPlan`                                                                                                                                                                                             |
| `lmm profile switch <name>`                                 | `core.SwitchResult` / `core.SwitchPlan`                                                                                                                                                                                                         |
| `lmm profile sync`                                          | `core.ProfileSyncResult` / `core.ProfileSyncPlan`                                                                                                                                                                                               |
| `lmm profile import <file>`                                 | `core.ProfileImportResult`                                                                                                                                                                                                                      |
| `lmm profile create/delete/rename/reorder`                  | `core.ProfileResult` — `{profile{…}}`                                                                                                                                                                                                           |
| `lmm mod enable/disable`                                    | `core.EnableResult` / `core.DisableResult` — `{changed, …}`                                                                                                                                                                                     |
| `lmm mod lock/unlock/set-update/convert`                    | `core.ModSettingResult` — `{mod{}, locked, update_policy, …}`                                                                                                                                                                                   |
| `lmm mod edit <mod-id>`                                     | `core.RelinkResult` — `{mod{}, changes[], no_changes}`                                                                                                                                                                                          |
| `lmm game detect --all` / `--select`                        | `core.GameDetectResult` — `{saved[], profiles[], warnings[]}`                                                                                                                                                                                   |
| `lmm game add` (flag-driven)                                | `core.GameListEntry` — the same row `lmm game list --json` prints for it                                                                                                                                                                        |
| `lmm game add --query` (no `--pick`)                        | `core.GameCatalogReport` — `{source_id, query, matches[]}`                                                                                                                                                                                      |
| `lmm game detect --include-unknown` (no `--all`/`--select`) | `core.GameDetectListing` — `{games[], warnings[]}`; the CLI's twin of `GET /api/v1/games/detect?all=1`                                                                                                                                          |
| `lmm auth login --key-from-env`/`--key-stdin`               | `app.AuthStatusReport` — the same document `lmm auth status --json` prints                                                                                                                                                                      |
| `lmm auth logout [source]`                                  | `app.AuthStatusReport` — the same document, re-read after the removal                                                                                                                                                                           |
| `lmm update --all`                                          | `core.UpdateBatchResult` — `{game_id, profile, applied[], failed[], skipped[]}`, or `core.UpdateCheckReport` when the check found nothing to apply (see below); without `--all` a bulk run is a CHECK and always emits `core.UpdateCheckReport` |
| `lmm game edit`                                             | `core.GameListEntry` — the same row `lmm game list --json` prints for it                                                                                                                                                                        |
| `lmm game set-default` / `clear-default`                    | `core.SettingsResult` — `{default_game}`                                                                                                                                                                                                        |

**`lmm update --all --json` emits one of two documents.** With something
to apply it emits `core.UpdateBatchResult`. With nothing to apply it never
enters the batch at all and emits the check document,
`core.UpdateCheckReport` — which is the more useful answer there, since its
`skipped{}` names the mods that were passed over (pinned, locked, manual
download) and an empty batch result would say only that nothing happened.
The two are told apart by their keys: only the batch result carries
`applied`.

**`--json` never prompts.** Every confirmation has a flag that decides it
(`-y`/`--yes`, or `--force` where that is the existing meaning); without it
the run fails **before mutating anything** with the error envelope
(`"confirmation required: …"`, exit `1`). `lmm game detect --json` therefore
needs `--all` or `--select`; an `lmm install --json` or
`lmm import <archive> --json` that hits file conflicts needs `--force`, and
without it the envelope's `details.conflicts` names every conflicting file.
`lmm game add` and `lmm auth login` gained a flag for every prompt they had
(#307), so both now run fully non-interactively and under `--json`; a value
neither a flag nor a prompt supplied fails with an error naming the flag
that answers it. Progress and per-mod status lines are suppressed under `--json`, so
stdout holds the document and nothing else and stderr stays empty (except for
`--log-level` diagnostics, which are always stderr).

List fields are always arrays, never `null`: an empty listing is `[]` and a
game with no configured sources is `{}`. Enum-valued fields (link method,
deploy mode, update policy, update status, auth state) are their text names,
not integers. Times are RFC 3339.

**Behaviour deltas landed alongside the JSON contract.** A handful of
plain-text and event changes shipped in the same phase as the `--json`
switch, each pinned by a re-recorded capture and listed in the CHANGELOG
under its issue number:

- **#294 — lock refusals, one wording.** Every refusal of a locked mod
  (`lmm update`, `lmm mod edit`, `lmm mod lock`/`unlock`) now prints the same
  `core.LockedRefRefusalError` text, replacing four hand-worded messages. A
  `UpsertMod` refusal swallowed by `lmm profile apply`/`sync` is no longer a
  `--verbose`-only note — it prints unconditionally to stderr as
  `Warning: …` and rides along on the command's `--json` document as
  `warnings` (or `details.warnings` on the error envelope if the run then
  fails with nothing left to carry them).
- **#283 — a global update-check counter.** Checking updates across two or
  more sources under `-v` used to restart its `n/total` counter per source
  (`1/3 … 3/3`, then `1/2 … 2/2`); `UpdateCheckEvent` gained
  `GlobalIndex`/`GlobalTotal` and the line now runs unbroken (`1/5 … 5/5`).
  `--json` is unaffected — progress events are always suppressed there.
- **Ruling 8 — dry-run merged-artifact modelling.** `lmm uninstall --dry-run`
  and `lmm purge --dry-run` used to always print a merged-artifact line on a
  compile-mode game; the plan now models the actual effect
  (`merged_artifact: {action, path}` under `--json`, the key omitted
  entirely when nothing would change), so a dry run that would leave the
  artifact alone
  says nothing. `lmm import <archive> --json`'s `merged_pak_synced` is set
  from whether the sync actually ran and succeeded, not from the game's
  deploy mode alone.
- **#296 — hooks survive export/import.** `lmm profile export` now writes a
  `hooks:` block (including an explicitly-disabled override), so
  `export` → `import` round-trips hook configuration byte-for-byte; an
  export from before this change (no `hooks:` key) still imports.
- **Ruling 16 — cancellation never splits DB and profile state.** Ctrl-C
  (or any context cancellation) during a mutation that writes both the
  database and a profile file — install, dependency install, archive
  import, adopt, profile import/switch, uninstall, `purge --uninstall`, and
  `mod edit`'s relink/version paths — now finishes that pairing before
  stopping, so a run can no longer be cancelled between the two writes and
  leave a mod in one store but not the other. A cancelled run prints
  `Cancelled.` to stderr before exiting `2` in plain mode (`--json` stays
  silent; it was already unaffected).
- **A batch install aborts up front on an unloadable profile.** If the
  target profile.yaml exists but fails to parse (or can't be read), a
  multi-mod `install` now fails before installing anything, instead of
  running to completion and reporting per-mod warnings as if it had
  succeeded.
- **Ruling 17 — `game show-default`'s plain text moves to stdout.** Its two
  lines used to land on stderr, an accident of `cmd.Println`/`cmd.Printf`
  (which write to the command's error stream when no output writer is set);
  they now go to stdout like every other command's plain text, and `--json`
  on it now yields the `core.DefaultGame` document instead of an empty one.
  The bytes themselves are unchanged (#309).

> **v2 changed these shapes.** Before v2 each command projected its own
> ad-hoc view struct, so the JSON was a parallel, undocumented contract that
> could drift from what core actually knew. v2 emits core's own types
> directly — richer, self-describing documents (a `mods[]` row is a whole
> installed mod; a mod reference names its source, not just an ID) — and
> keys were renamed where the old name was ambiguous. Scripts written against
> the 1.x JSON need updating. See the CHANGELOG's "Changed — JSON output
> (v2)" section for the full field-by-field list.

### Commands

| Command                                                   | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lmm init`                                                | Guided first run: detect games → add → default game → sign in → import what is already there. Interactive; every step skippable, safe to re-run (see [Quick Start](#lmm-init--the-guided-first-run))                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `lmm search <query>`                                      | Search all configured sources concurrently                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm search <query> --source ID`                          | Search a single source instead of all configured ones                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm search <query> --category NAME`                      | Filter by category (NexusMods: the category name, e.g. `Armour`; CurseForge: its numeric id)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `lmm search <query> --tag TAG`                            | Filter by tag (NexusMods only; repeat for multiple)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm install [query]`                                     | Search and install a mod (query optional with `--id`)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm install --id <mod-id>`                               | Install by mod ID                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `lmm install --id <mod-id> --file <file-id>`              | Install a specific file, skipping file selection                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm install --version <version>`                         | Install the exact-match version (archived files searched automatically)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm install --show-archived`                             | Include archived/old files when selecting a file                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm install --no-deps`                                   | Skip automatic dependency installation                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm install --source ID` / `-s`                          | Use a specific source (default: sole configured source; prompts when several are configured, `-y` picks the first alphabetically)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `lmm uninstall <mod-id>`                                  | Uninstall a mod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `lmm uninstall <mod-id> --keep-cache`                     | Uninstall but keep the cached mod files                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm uninstall <mod-id> --dry-run`                        | Preview what an uninstall would do                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `lmm import`                                              | Scan `mod_path` for untracked mods and import them (see [Import](#import) below)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm import <archive-path>`                               | Import one local mod archive                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `lmm import <archive-path> --dry-run`                     | Preview what importing one archive would do (the archive is listed, never extracted)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `lmm list`                                                | List installed mods                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm list --profiles`                                     | List profiles for the game                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm status`                                              | Show current status                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm update`                                              | Check for and apply auto-updates                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm update <mod-id>`                                     | Update a specific mod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm update --all`                                        | Apply all available updates in one batch — locked mods are skipped and reported together, a failure on one mod does not stop the rest                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm update --dry-run`                                    | Preview what would update                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm update rollback <mod-id>`                            | Rollback to previous version                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `lmm snapshot create [--name N]`                          | Record a named point you can bring the game back to (metadata only; see [Snapshots](#snapshots) below)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm snapshot list`                                       | List the game's snapshots, newest first                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm snapshot restore <name>`                             | Bring the game back to a snapshot (undeploy → put originals back → restore the recorded mods at their recorded versions). Records the current state first                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm snapshot restore <name> --dry-run`                   | Preview the whole restore without changing anything                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm snapshot delete <name>`                              | Delete a snapshot's record (the stored originals are kept)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm verify`                                              | Verify cached mod files (see below)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm verify --fix`                                        | Re-download missing files, populate missing checksums, repair version-record mismatches, remove stale lmm-deployed files                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `lmm mod enable <mod-id>`                                 | Enable a disabled mod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm mod disable <mod-id>`                                | Disable mod (keep in cache)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `lmm mod set-update <mod-id> --auto`                      | Enable auto-updates for mod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `lmm mod set-update <mod-id> --notify`                    | Notify only (default)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm mod set-update <mod-id> --pin`                       | Mute update checks for mod (does not hold a version — see [Locking](#locking-mods-to-a-version))                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm mod lock <mod-id> [version]`                         | Lock mod's profile entry to its current or a specific version                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `lmm mod unlock <mod-id>`                                 | Clear a mod's lock (recorded version is left untouched)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm mod show <mod-id>`                                   | Show mod details (description, image, etc.)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `lmm mod files <mod-id>`                                  | List files deployed by mod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm mod edit <current-id>`                               | Edit mod details (name, version, author, source, ID)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `lmm mod edit <current-id> -s <source>`                   | Pick which installed mod to edit when the same ID exists in more than one source                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm mod convert <mod-id> <on\|off>`                      | Toggle pak-to-exmod conversion for a mod (merge-compile games only)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm game set-default <game-id>`                          | Set the default game                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `lmm game show-default`                                   | Show current default game                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm game clear-default`                                  | Clear the default game setting                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `lmm game add`                                            | Add a game — prompts for anything a flag did not supply                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm game add --source <id> --id <identifier>`            | Name the mod source and this game's identifier with it (a NexusMods slug, a CurseForge game id, a custom source's key) instead of being prompted                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm game add --query <q> [--pick <n>]`                   | Search a source's game catalog instead of naming an identifier; without `--pick` the matches are printed (`core.GameCatalogReport` under `--json`)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `lmm game add --name <n> --path <dir> [--mod-path <dir>]` | Display name, install path (must exist) and mod directory (default `<install>/mods`; absolute, or relative to the install path)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `lmm game add --from-detected <steam-app-id>`             | Prefill everything from an installed Steam game — name, install path, game id, mod path, and a known game's source map; every other flag still wins, and `--source` (with `--id`/`--query`/`--pick`) ADDS to a curated source map rather than replacing it — `--id`/`--query`/`--pick` alone need `--source`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `lmm game list`                                           | List configured games (ID, name, paths, deploy mode, sources; marks the default)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm game edit <game-id> --source <id>=<identifier>`      | Add or replace one of the game's source mappings (repeatable); the identifier may be empty for a source that needs none                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm game edit <game-id> --remove-source <id>`            | Drop one source mapping (repeatable; removals apply before additions, and a game must keep at least one source)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `lmm game detect`                                         | Scan Steam libraries for moddable games — every game in the known-games list (extend it via [`steam-games.yaml`](docs/configuration.md#steam-gamesyaml-optional)), plus any game with Steam Workshop items already downloaded. Rows are numbered continuously, curated first, each printing its Steam app id, and the prompt takes a row number **or** an app id — a bare number that is both a row number and another row's app id is refused rather than guessed at, so spell it `#3` (row 3) or `app:10` (Steam app id 10); if nothing is found but this machine has OTHER installed Steam games lmm has no known-games entry for, says how many and names `--include-unknown` instead of reporting nothing found                                                                                                                                                                                                                                                                                                                                                         |
| `lmm game detect --all`                                   | Non-interactively select every not-yet-configured detected game (same set the "all" prompt answer picks); required under `--json`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `lmm game detect --select <rows>`                         | Non-interactively select detected games by row number or Steam app id (e.g. `1,3` or `1,1133870`), or name one explicitly with `#3`/`app:10`; required under `--json`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm game detect --include-unknown`                       | Also list the remaining installed games with no known-games entry (the ones with no Workshop items either), with their Steam app ids for `lmm game add --from-detected` — selecting one here is refused with that pointer, since nothing tells lmm which mod source it belongs to; under `--json` (with no selection flag) emits the listing document, which only this flag produces: a plain `lmm game detect --json` emits **no** listing at all — with no `--all`/`--select` it refuses (nothing may prompt under `--json`), and with either one it emits the result document for what it added. What `--all`/`--select` choose from is the default listing, Workshop-bearing uncurated rows included. Steam's own tools (Proton, the Linux runtimes, the redistributables) are filtered out by a small deny-list, so a genuinely misidentified title never appears here — add it to your [`steam-games.yaml`](docs/configuration.md#steam-gamesyaml-optional) override to make it a known game (which skips the filter entirely), or fall back to a plain `lmm game add` |
| `lmm auth login [source]`                                 | Authenticate with a source (any source declaring auth; nexusmods/curseforge validated live)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `lmm auth login <source> --key-from-env`                  | Read the key from the source's own environment variable (`NEXUSMODS_API_KEY`, `CURSEFORGE_API_KEY`, or the derived `LMM_<ID>_API_KEY`) — no prompt                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `lmm auth login <source> --key-stdin`                     | Read the key as exactly one line from stdin — no prompt                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm auth logout [source]`                                | Remove stored credentials (under `--json`, prints the re-read `lmm auth status` document)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm auth status`                                         | Show authentication status                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm profile list`                                        | List profiles                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `lmm profile create <name>`                               | Create a profile                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm profile switch <name>`                               | Switch to a profile (installs missing mods)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `lmm profile switch <name> -y`                            | Skip the confirmation prompt; required under `--json`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm profile delete <name>`                               | Delete a profile                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `lmm profile rename <old> <new>`                          | Rename a profile (its mods, load order, hooks, overrides and default status move with it)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm profile export <name>`                               | Export profile to YAML                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm profile import <file>`                               | Import profile from YAML                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `lmm profile import <file> --force`                       | Import and overwrite existing                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `lmm profile import <file> -y`                            | Answer "Download and install mods?" without a prompt; required under `--json`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `lmm profile reorder [mod-id ...]`                        | Show or set load order                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm profile reorder -i`                                  | Pick the new load order from a numbered list — type positions ("1,3,2", ranges "2-5,1"), no mod IDs needed; Enter keeps it, `q` cancels                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm profile sync`                                        | Update profile to match installed mods                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm profile sync -y`                                     | Skip the confirmation prompt; required under `--json`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `lmm profile apply`                                       | Install/enable mods to match profile                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `lmm profile apply/switch/sync --dry-run`                 | Preview what an apply/switch/sync would do, changing nothing                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `lmm deploy`                                              | Deploy all enabled mods from cache                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `lmm deploy <mod-id>`                                     | Deploy specific mod from cache                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `lmm deploy --method hardlink`                            | Deploy using different link method                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `lmm deploy --purge`                                      | Purge then deploy all mods                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm deploy --dry-run`                                    | Preview what a deploy would do                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `lmm purge`                                               | Remove all mods from game directory                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `lmm purge --dry-run`                                     | Preview what a purge would do (no confirmation prompt)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm conflicts`                                           | Show file conflicts in current profile                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `lmm source list`                                         | List built-in and user-defined mod sources                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `lmm source validate <file>`                              | Validate a user-defined source definition                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm source validate --probe <file>`                      | Also live-smoke-test the definition (scan/fetch/API call)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `lmm source validate --probe --id <mod-id> <file>`        | Probe an `api` definition that has no `search` endpoint                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm source add <file>`                                   | Install a user-defined source definition under the config dir                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `lmm source remove <id>`                                  | Remove a user-defined source (refused while a game still maps it)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `lmm serve`                                               | Start the local web UI — the second frontend — on `127.0.0.1:7420` and open a browser (see [Web UI](#web-ui-lmm-serve))                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `lmm serve --addr <host:port>` / `--no-open`              | Bind somewhere other than the default, or don't open a browser                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |

`lmm install --version <version>` resolves the exact version against the mod's full file list — archived/old files are searched automatically, no `--show-archived` needed — and the matching file(s) become the pool for `--file`/`-y`/the interactive prompt; when the mod has dependencies, `--version` and `--file` apply to the named mod only (`--file` picks from the version's matches when both are given, and the whole install aborts up front if either fails to resolve) — dependencies are unaffected, still installing at latest with their primary file auto-selected. An unknown version fails with an error listing the versions the source actually has (`version not found: version "..." (available: ...)`). A source whose files carry no version information fails with the standard "not supported" gap instead, same as any other missing capability — this is decided dynamically from the actual file data returned for that mod, not from the source's advertised `versions` capability flag (a source can declare `versions` support and still hit this gap for a mod whose files happen to lack version strings). Omitting `--version` installs the latest, unchanged.

**Version behavior in profiles**: a mod reference's `version:` field in a profile is the record of what that profile deploys, not just a display value — `lmm profile apply` and `profile switch` converge the installed mod to match it, downgrades included, healing a stale on-disk deployment back to the recorded version whenever it's still available upstream; `profile import` converges the same way: a mod already installed at a different version than the imported profile records is reinstalled at the profile's version as part of the import itself — so a lock carried by a shared profile takes effect without a second command. Hand-edit a profile's `version:` (or export/share/import the profile) to reproduce an exact build across machines. Sources whose files carry no version information (decided dynamically from the actual file data, not the source's advertised `versions` capability flag) keep the previous file-ID-based behavior instead.

### Exit Codes

| Code | Meaning                                                     |
| ---- | ----------------------------------------------------------- |
| `0`  | Success                                                     |
| `1`  | Error                                                       |
| `2`  | Cancelled by the user (e.g. declined a confirmation prompt) |

`--dry-run` always exits `0` once it renders a plan, even when the plan's own data records a
selection that is certain to fail once applied (e.g. an unknown or disabled mod ID) — a plan is
data, not an attempt. `lmm deploy <id> --dry-run` therefore exits `0` for a mod ID the live
`lmm deploy <id>` would reject with exit `1`. A scripted pre-flight check cannot rely on
`--dry-run`'s exit code alone to detect a doomed deploy/uninstall/purge ahead of time. Combined
with `--json`, `--dry-run` emits the plan document itself rather than its rendering — see
[JSON output](#json-output).

### Import

`lmm import` has three modes: scan (no arguments), archive (an archive path), and `--workshop`. The first two are chosen by whether an archive path is given:

- **Scan mode** (`lmm import`, no arguments): scans the game's `mod_path` for files not yet tracked by lmm, tries to match each one by name against every search-capable source configured for the game (in ID-sorted order — e.g. `curseforge` before `nexusmods` when both are configured; skip matching entirely with `--skip-match`), and imports whatever is left after confirmation. Candidates are **scored** against the scanned name, and against the version too when the filename carries one: the best-scoring candidate across all sources wins, ties break deterministically (version agreement, then source ID, then mod ID), an exact name match ends the lookup early, and anything that does not clear the confidence bar is left **untracked** and imported as local rather than adopted as a similarly-named mod — searching `skyui` should never quietly attach your archive to `SkyUI Flashlite`. Three differences are refused outright, however close the rest of the name is: a **differing sequel number** (`Sim Settlements 2` is never `Sim Settlements 3`), **any difference at all in a pair whose longer name is under twelve letters** (`Vortex` is never `Vertex`, and `SkyUI` is never `SkyUI SE`), and **whole extra words** (`RaceMenu` is not `RaceMenu Special Edition`). A subtitle set off by punctuation is the exception: `Ordinator` matches `Ordinator - Perks of Skyrim`, and `HDT-SMP` matches `HDT-SMP (Skinned Mesh Physics)`, always as a `[probable match]` so the elided subtitle is visible before you confirm — unless two catalogue rows hang subtitles off the same name (`Alternate Start - Live Another Life` and `Alternate Start - Realm of Lorkhan`), where the tie is refused and the archive stays untracked instead of being attached to whichever sorts first. A mod that stays local this way is fully usable — it just has no update target; re-link it with `lmm mod edit --to-source`. The scan readout annotates any match short of an exact name with its confidence (`[strong match]`, `[probable match]`). Useful for mods that were installed manually — e.g. mods whose source has disabled API downloads. `--skip-match` only applies to this mode. Every mod imported this way is marked as requiring manual download (since lmm did not fetch it itself); re-link it to a source with `lmm mod edit --to-source` to clear that once it can be checked for updates normally.
- **Archive mode** (`lmm import <archive-path>`): imports that one specific mod file, deploying it and adding it to the profile. Pass `--id` (with `--source`, or it defaults to the game's sole configured source, prompting interactively when several are configured) to fetch and attach source metadata as part of the import. `--dry-run` previews it: the archive's table of contents is read (never extracted), so the preview names the mod, the files it would deploy, and any file it would overwrite, without writing anything ([#314](https://github.com/DonovanMods/linux-mod-manager/issues/314)).

Either way, a mod that ends up unmatched to any remote source is imported as local — it deploys and installs normally, but `lmm update` has nothing to check it against and will never notify about it.

```bash
lmm import --game hytale                    # Scan mod_path for untracked mods
lmm import --game hytale --dry-run          # Preview what would be imported
lmm import ./my-mod.zip --game baldurs-gate-3    # Import a specific archive
lmm import ./mod.zip --game baldurs-gate-3 --id 12345 --source nexusmods
lmm import --workshop --game space-engineers-2   # Track subscribed Steam Workshop items
```

The third mode, `lmm import --workshop`, is described under [Steam Workshop](#steam-workshop). It is mutually exclusive with an archive argument and with `--skip-match`.

### Thunderstore

lmm can **search Thunderstore** — the mod host behind Lethal Company, Valheim, Risk of Rain 2 and most BepInEx-era Unity games — with no credential of any kind. Thunderstore needs no key for anything, so there is nothing to sign in to and `lmm source list` shows its auth as `n/a`.

Map a game to it by community slug, which is the per-source game id:

```yaml
games:
  - id: lethal-company
    sources:
      thunderstore: lethal-company # the slug in a thunderstore.io/c/<slug>/ URL
```

or `lmm game edit --source thunderstore=lethal-company`.

The slug is **required**. lmm will not guess a community from the game's own id — a wrong guess silently downloads and searches the wrong 230 MB — so `lmm game add` and `lmm game edit` both refuse an empty mapping for a source that needs one, and a search against a game whose mapping is already empty says which command fixes it rather than picking a community for you.

**Search runs against a locally cached copy of the community index**, because Thunderstore publishes one unpaginated document per community and no per-query search endpoint at all. The first search for a community downloads that document and turns it into an index under `<data>/cache/_thunderstore/<community>/` — a few seconds and, for the largest community on the site, around 230 MB on disk. `lmm search` says so on stderr while it happens, so `--json` still writes exactly one document on stdout. After that:

- the index is served **without any request at all** for six hours;
- past that, a refresh is a conditional request that upstream usually answers "unchanged" in **zero bytes**;
- if Thunderstore cannot be reached, the copy you have keeps answering searches rather than the source going dark;
- results carry an **exact** total, so paging through them is exact and deep — Thunderstore is the first source that knows how many results it has.

`--category` and `--tag` both filter Thunderstore's own categories (`Mods`, `BepInEx`, `Client-side`, …), case-insensitively and ANDed together. A deprecated package still appears, marked `Deprecated` among its categories, and never above a package that is not.

The index directory is safe to delete at any time; the next search rebuilds it.

> **Installing** from Thunderstore is not wired up yet — this release line adds the source, its index and search over it ([#360](https://github.com/DonovanMods/linux-mod-manager/issues/360)); package, version and dependency reads land with the next unit of the same issue.

### Steam Workshop

lmm can **track** the Steam Workshop items you are already subscribed to, and **download** an item so it manages its own copy. Tracking reads Steam's own bookkeeping (`steamapps/workshop/appworkshop_<appid>.acf`) across every Steam library on the machine, records each installed item, and checks it for updates through Valve's keyless metadata API.

**Tracking, updates and collections need no credentials at all.** Only _searching_ the Workshop needs a Steam Web API key, and it is yours, not lmm's — see [Searching the Workshop](#searching-the-workshop-your-own-api-key) below.

**Tracking what Steam already installed:**

- `lmm game detect` maps a game whose Workshop manifest shows installed items to the `steamworkshop` source automatically (the per-source game id is the Steam **app id**). Suppress it with `--no-workshop`, or add the mapping later with `lmm game edit --source steamworkshop=<appid>`. Such a game is **listed by default** even when lmm has no curated entry for it — the downloaded items are what say it is moddable — with `Steam Workshop: N items` beside the row, and picking it (by row number or by app id) configures it straight from the detection. The same rows appear in the web UI's detect list.
- `lmm import --workshop` records every subscribed item lmm does not already track. `--dry-run` previews it; `--refresh` bypasses the cached Steam metadata. In `lmm serve`, the same flow is **Add mods ▾ → Track Steam Workshop items…** or the **Scan Steam Workshop…** card in **Setup → Adopt** — the same preview and confirm either way — offered once the game maps the `steamworkshop` source.
- `lmm list` marks such mods `EXTERNAL`, `lmm status` counts them separately, and `lmm mod show` prints a "Managed by: Steam Workshop" block naming the directory Steam owns.
- `lmm update` reports an item whose Steam revision has moved on, and says plainly that **Steam** applies that update — the next time you launch the game, or via Steam's _Verify integrity of game files_.
- `lmm verify` checks that the directory Steam owns is still there and non-empty; an item you unsubscribed from is reported as a finding.
- `lmm uninstall` on such a mod removes **lmm's tracking only**. The item stays subscribed in Steam; unsubscribe in the Steam client to remove it.

**What it deliberately does NOT do.** lmm never moves, copies, downloads or deletes a Workshop item's files — the Steam client owns them where they sit, and the game loads them from there. So:

- **Deploying, enabling, disabling, updating, rolling back and re-linking are refused** for a Workshop item, with a message naming what to do in Steam instead. So is `lmm install steamworkshop:<file id>` for an item you are already subscribed to: an lmm-managed second copy alongside the one Steam loads would put the mod in the game twice. A bookkeeping-only "disabled" flag on a mod the game still loads would be a lie.
- **Profile switches do not change what Steam has on disk.** A Workshop item is game-global; lmm profiles are not. `lmm profile switch` / `apply` leave such items exactly as they are and say so once. Managing which items are active is the Steam client's job.
- **Conflict detection cannot see them.** `lmm conflicts` compares files deployed under the game's `mod_path`, and a Workshop item has none there. lmm cannot see inside a game's own Workshop loader.
- **`lmm profile reorder` omits them.** Load order decides deploy precedence, and a tracked-only item deploys nothing, so any position it held would be inert.
- **A snapshot records them; a restore leaves them alone.** lmm never captures a Workshop item into the [originals store](#snapshots) and holds no copy to put back, so `lmm snapshot restore` undeploys nothing for it and downloads nothing for it. An item Steam no longer has on disk is reported as a finding — the same judgement `lmm verify` makes — not a refusal.

**Downloading an item lmm manages itself.** `lmm install steamworkshop:<file id>` (and the same Install action in `lmm serve`) downloads a Workshop item into lmm's own cache and deploys it like any other mod. Nothing about it is special once the bytes are on disk: it appears in `lmm list` without the `EXTERNAL` marker, deploys, disables, updates and uninstalls normally, and takes part in conflict detection and load order.

lmm gets the bytes one of two ways, and neither needs your Steam password:

- A handful of **old UGC-era items** are still published at a plain URL, which lmm downloads directly and checks against the exact byte count Valve reports for it.
- Everything else needs **[steamcmd](https://developer.valvesoftware.com/wiki/SteamCMD)**, which lmm shells out to anonymously. Install it from your distribution's packages; lmm never bundles it, never installs it for you, and tells you so with the install link if it is missing when you try. steamcmd runs pinned to lmm's own staging directory with an isolated `HOME`, so it can neither find nor write to your real Steam library.

**Not every game allows this.** Anonymous Workshop downloads are a per-app opt-in, and there is no way to know before trying — an item whose download is refused still describes itself perfectly. When a publisher has not opted in, lmm says so and points you at the route that does work: subscribe to the item in the Steam client, then run `lmm import --workshop` and lmm will track it in place.

**What downloading deliberately does NOT do.** lmm never signs in to your Steam account, never stores a Steam password or session, never manages your subscriptions, and never rehosts or proxies Workshop content. If anonymous download is refused, tracking a subscription is the answer — there is no fallback that logs in as you.

#### Searching the Workshop (your own API key)

Valve's search endpoint refuses an unauthenticated request, so searching the Workshop needs a **Steam Web API key**. Get one free at <https://steamcommunity.com/dev/apikey>.

**The key is personal and confidential.** It is tied to your own Steam account. Never share it, never publish it, and never paste somebody else's — lmm ships no key of its own and never will, because embedding a shared key in a distributed application violates the Web API Terms of Use, and a leaked key is your account's problem, not the tool's.

```bash
lmm auth login steamworkshop         # store your key (validated live before it is saved)
export STEAM_WEB_API_KEY=...         # or just export it; lmm reads this name
lmm search "cargo ship" --game space-engineers-2 --source steamworkshop
```

Stored keys are encrypted at rest, like every other source's. Without a key, a Workshop search reports that authentication is required rather than silently returning nothing; every other Workshop feature keeps working.

Two Workshop-specific search notes: `--category` and `--tag` are both sent as **required tags** (the Workshop has no category concept distinct from tags), and results are cached for five minutes so pressing the same search twice costs one round trip, not two.

#### Importing a collection

A Workshop **collection** is a mod list, which is what an lmm profile is — so lmm imports one as a profile:

```bash
lmm profile import --workshop-collection 2500900001 --game space-engineers-2
lmm profile import --workshop-collection https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001 --as ships
```

It takes the collection's id or the URL of its page, needs **no API key**, and names the profile after the collection unless you name it with `--as`. The profile **records the collection's list**: every item becomes a `steamworkshop:<file id>` entry in it. Items you are already subscribed to and tracking are marked as such; every other item is listed with what to do about it — subscribe to it in Steam, then run `lmm import --workshop`. Nothing is downloaded: the import records the list.

What such a profile does **not** do is change what the game loads. A Workshop item is game-global — Steam has it on disk and the game loads it whichever lmm profile is active — so switching to a collection profile deploys nothing and undeploys nothing, and `lmm list --profile <name>` for it is empty until the items are installed by something. It is a **record of the list**, useful for seeing what a collection contains and what you are missing from it, not a way to turn a set of Workshop items on and off. To have lmm manage an item's own copy, install it — see [downloading an item lmm manages itself](#steam-workshop) above.

In `lmm serve` the same input is in the **Profiles** modal, and pasting a collection link into the search box offers the import directly.

Steam Workshop metadata is cached under `$XDG_DATA_HOME/lmm/cache/_steamworkshop/meta/` — six hours for an item Valve describes, one hour for one it refuses. The directory is safe to delete at any time; `--refresh` bypasses it for one run.

### Search

`lmm search <query>` queries every source configured for the game concurrently by default — there's no prompt to pick one first, even when several sources are mapped. Results carry a `SOURCE` column so you can tell which source found each mod:

```text
$ lmm search bigger --game baldurs-gate-3
ID                  NAME             AUTHOR   VERSION  SOURCE
--                  ----             ------   -------  ------
BiggerBackpack-2.1  Bigger Backpack  donovan  2.1      donovan-mods
```

If one source fails, its failure is reported as a warning on stderr and the other sources' results are still returned — a flaky manifest URL doesn't hide results from a source that responded:

```text
warning: source my-repo: source "my-repo": reading manifest /opt/mods/my-repo.yaml: open /opt/mods/my-repo.yaml: no such file or directory
```

Only when **every** configured source fails does the command return an error, which names each source's failure:

```text
Error: search failed: all 1 source(s) failed: source my-repo: source "my-repo": reading manifest /opt/mods/my-repo.yaml: open /opt/mods/my-repo.yaml: no such file or directory
```

`--limit N` (default 10) is a target, not just a ceiling: sources are paged until N merged results exist, every source runs out, or a safety bound of 10 pages per source is reached. A source is only paged at a page size it has shown it can honour, which is what keeps consecutive pages consecutive **rows**: most remote APIs cap how many results one page can hold (CurseForge at 50, NexusMods around 30), and asking such a source for "page 2 of 100" fetches rows 100–149 while rows 50–99 were never returned at all. When a source reports the smaller size it actually served, lmm adopts that size for the rest of the search and keeps paging it — CurseForge answers a `--limit 100` in two contiguous pages of 50. A source that caps **silently** (NexusMods reports neither its cap nor a total) has no safe size to page at, so it contributes one capped page and is not paged further: `--limit 100` can legitimately come back with fewer than 100 results, and `has_more` says so, but every result you do get is really among the first ones that source had. Ask for a smaller `--limit` (at or below such a source's cap) to page it further. A source that fails partway through the paging is reported as a warning like any other source failure, and the results its earlier pages returned are kept.

Use `--source <id>` to search a single configured source instead of aggregating:

```bash
lmm search bigger --game baldurs-gate-3 --source donovan-mods
```

A source that doesn't support searching (e.g. an `api` source defined without a `search` endpoint — see [API Sources](#api-sources)) is silently skipped when aggregating, but targeting it directly with `--source` reports a clear notice instead of a generic error:

```text
Error: source "demo-api" does not support searching; install by ID instead: lmm install --source demo-api --id <mod-id>
```

A source that **can** search but that you have never signed in to is skipped the same way, rather than warned about on every query: `lmm init` maps `steamworkshop` from a Steam scan because tracking and updating Workshop items needs no key, while searching the Workshop does. The skip is still reported — a search that comes back empty names what it left out and how to fix it — it just isn't a failure:

```text
steamworkshop was skipped: not signed in (run: lmm auth login steamworkshop).
```

Once a key **is** stored (or supplied through `LMM_<ID>_API_KEY` or a built-in's own variable), an authentication failure means that key is expired or revoked — a real problem — and is reported as a warning like any other source failure. Targeting the source directly with `--source` always reports it.

A game with no configured sources at all fails fast with a diagnostic instead of an empty result:

```text
Error: no mod sources configured for Baldur's Gate 3; add sources with 'lmm game add' or edit games.yaml
```

`--json search` includes the same per-source failures as a `"warnings"` array alongside `"mods"`, each entry `{source_id, error}`, and names any source skipped for want of a credential in `"skipped_unauthenticated"` (present only when there is one).

### Update check behavior

When you run `lmm update`, the tool checks each installed mod against the source (e.g. NexusMods). If some mods cannot be fetched (e.g. deleted, private, or network error), you still see **partial results** (any updates that were found), and a **warning** is printed to stderr describing which mods could not be checked.

### Snapshots

A **snapshot** is a named point you can bring a game back to.

```bash
lmm snapshot create --name before-total-conversion
lmm snapshot list
lmm snapshot restore before-total-conversion --dry-run
lmm snapshot restore before-total-conversion
lmm snapshot delete before-total-conversion
```

It is **metadata, not a copy of your mods**: the profile (with its load
order and locks), the installed versions and settings, and the deployed
files with their checksums. The mod files themselves are already in the
cache, so a snapshot costs kilobytes. Creating one hashes the deployed tree,
which is the same work `lmm verify` does — so on a large install it is not
instant.

Because a snapshot holds no mod bytes, a **restore** needs each mod's
recorded version from the cache, or a download of that exact version from
its source when the cache no longer has it. A version the source can no
longer serve is a refusal in the preview, before anything is touched. The
stock content lmm replaced is the exception: that IS kept, in the originals
store below, which is the only copy of it.

**The originals store** is the part lmm cannot reconstruct any other way.
Whenever a deploy, or a profile override, would replace a file lmm did not
put there — stock game content, or a file another tool left — the original
is copied to `~/.local/share/lmm/snapshots/<game>/_originals/` **first, and
once**: the first original wins, because the second write's "original" is
lmm's own first write. `lmm snapshot restore` puts those back, checksum
-verified. `lmm snapshot delete` never removes them; they are the only copy.
**Removing the mod puts the original back too**, at the moment lmm's own
file goes: an `lmm uninstall`, a `lmm purge`, an `lmm update` that drops a
file the previous version shipped, a rolled-back install and the deploy-time
convergence all restore what they displaced, so undoing what lmm did no
longer leaves a hole where stock content was. The stored copy is dropped
only once the bytes are actually back in place — a put-back that fails keeps
both, so `lmm snapshot restore` can still do it. A restored original comes
back with the **mode it had**, so a stock launcher script or shipped binary
is executable again rather than `rw-r--r--`. A capture that **fails** — a full disk, a permission problem on the store —
never blocks the operation, but it is printed as a warning on stderr and
carried on the operation's result at any `--log-level`, because the moment
lmm cannot preserve an irreplaceable file is not one to discover at the
restore that cannot put it back.

**A restore is five stages**, in this order: the current deployment is
undeployed, every stored original goes back, the recorded profile is
written (and made the active one), the mods it lists are installed at their
recorded versions — downgrades included, re-downloading anything the cache
no longer has — and the ones the snapshot recorded as **enabled** are
deployed. A mod that was disabled when the snapshot was taken comes back
disabled, with none of its files on disk. A version the source can no longer serve is reported as a **refusal, in
the preview, before anything is touched**; it is never a quiet partial
restore. A snapshot of the **current** state is taken first, so a restore is
itself reversible (`--no-safety-snapshot` to skip that).

**A restore also puts back which profile was active.** Snapshots are listed
per game, not per profile — the snapshot you want back is often the one you
took before switching away — so restoring a snapshot taken under another
profile undeploys the currently active profile first and makes the
snapshot's profile the active one again. Both the `--dry-run` preview and
the web UI's confirm dialog say so before anything is touched, and the
safety snapshot records the profile you were on, so the way back is a
restore of that.

**Steam Workshop items are recorded, never restored.** A snapshot lists the
Workshop items the profile tracked, so the preview accounts for the whole
profile — but lmm holds no copy of one and never deploys it, so a restore
undeploys nothing for it, downloads nothing for it, and leaves its files
exactly where Steam put them. If Steam no longer has an item on disk (you
unsubscribed it after the snapshot), the preview and the result both say
so; it is a finding, not a refusal, because lmm never promised to put a
Steam subscription back. See [Steam Workshop](#steam-workshop).

**Automatic snapshots** are opt-in. Set `auto_snapshot: true` in
`config.yaml` and lmm records one before every deploy, profile switch and
update, named `auto-<op>-<timestamp>`. They are pruned: the newest
`auto_snapshot_keep` (default 10, `0` for unlimited) survive, and only the
automatic ones — a snapshot you named is yours until you delete it. Off by default because hashing a
large deployed tree on every deploy is a real cost, and because a user who
wants the safety net can say so once. An automatic snapshot that fails is a
warning, never a refusal — a backup that blocks the operation it is
protecting is worse than no backup.

### Verify output

`lmm verify` reports per file:

- **+ ModName (fileID) - OK** - Cache exists and checksum stored.
- **X ModName (fileID) - MISSING (version X not in cache)** - Cached files for that mod version are missing; use `--fix` to re-download.
- **? ModName (fileID) - NO CHECKSUM** - File was installed without a stored checksum (e.g. before checksum support or with `--skip-verify`).
- **! ModName - FILE COUNT MISMATCH** - The cache directory exists but is empty, when downloads were expected (per-mod, not per-file); not repaired by `--fix`.
- **? Unknown mod ID - SKIPPED** - A stored checksum row references a mod that's no longer installed; not repaired by `--fix`.
- **? path - STALE DEPLOYMENT (reason)** - A game-directory file/symlink that's either no longer provided by any installed mod, or a dangling symlink into the lmm cache with no owning row; `--fix` removes it (see below).

With `--fix`, verify also REMOVES stale lmm-deployed files and dangling lmm-cache symlinks from the game directory: files/links `--fix` created or tracks that no installed mod claims anymore, and cache-rooted symlinks whose target is gone. This is provenance-gated - only content lmm itself is responsible for is ever touched, never a foreign file just sitting in the game directory.

`lmm verify` also contacts each installed mod's source to check its recorded version against what the stored file ID(s) actually are upstream (issue #94: older installs could record the mod's "latest" version instead of the version of the file that was actually downloaded and deployed), reporting per mod:

- **X ModName - VERSION MISMATCH (recorded X, source reports Y)** - The recorded version doesn't match what the installed file ID(s) report upstream; use `--fix` to repair.
- **? ModName - VERSION UNVERIFIABLE** - None of the recorded file ID(s) are listed by the source anymore; not repaired by `--fix` (reinstall the mod instead).

For a game with a `loader:` block (see [BepInEx (Unity
games)](#bepinex-unity-games)), verify adds a loader tier — five checks,
reporting six statuses:

- **LOADER MISSING** — the game declares a loader and its preloader
  (`BepInEx/core/BepInEx.Preloader.dll`) is not in the install directory.
- **LOADER VERSION MISMATCH (declared X, installed Y)** — drift between the
  declaration and what is on disk.
- **LOADER BOOTSTRAP INCOMPLETE** — the files the declared bootstrap needs
  (`run_bepinex.sh` + `libdoorstop.so` for native, `winhttp.dll` +
  `doorstop_config.ini` for Proton) are not all there, which usually means
  the wrong BepInEx pack is installed.
- **LOADER NEVER RAN** — everything is in place and `BepInEx/LogOutput.log`
  does not exist, so the loader has not run; the finding names the launch
  option to paste.
- **LOADER STALE LOG** — the loader ran, but before the plugins currently on
  disk were deployed, so nothing proves the current set ever loaded.
- **LOADER PLUGIN UNLINKED** — an enabled mod's plugin files are not in the
  game directory; `--fix` re-deploys the mod.

Only the last is `--fix`-able: lmm does not install the loader or write
Steam launch options, so the remedy for the others is the setup `lmm game
show` prints.

A locked mod's VERSION MISMATCH is still reported, but `--fix` refuses to
rewrite a locked mod's record (other, unlocked mods in the same run are
still fixed) since the record is the lock's target, not drift to repair —
move the lock instead. A locked mod's MISSING, NO CHECKSUM and NEEDS
REINGEST repairs are refused on the same grounds whenever the source cannot
identify the recorded version's own file: each of them downloads into the
recorded version's cache slot, and a source that does not stamp a version on
its files (Icarus, for one) can never identify it — unlock first to repair
such a mod. Separately, when a locked mod's installed version
hasn't yet converged to the lock (see [Locking mods to a
version](#locking-mods-to-a-version)), `verify` prints an informational
"lock pending convergence" note rather than treating it as an issue.

Mods installed from a local source, mods requiring manual download, and mods with no recorded file IDs are skipped silently. `--fix` repairs a VERSION MISMATCH by re-keying the cache entry to the effective (source-reported) version, correcting the DB row and active profile record, and re-linking symlink deployments; if a cache entry already exists under the effective version the rename is skipped and a note is printed (also included as `note` in `--json` output) while the DB/profile are still corrected.

For a pak-to-exmod-conversion game (Icarus, #221), `lmm verify` also reports two more compile-only states, per mod:

- **? ModName - CONVERSION FAILED (reason)** - The mod's prebuilt `.pak` could not be converted into the merged pak on the last sync and stays raw-deployed instead; fix the mod or run `lmm mod convert <mod-id> off` to silence it (not repaired by `--fix`).
- **? ModName (fileID) - NEEDS REINGEST** - The mod's pak was cached before conversion support existed, so it has no retained source to convert from; `--fix` re-ingests it via the same redownload path `MISSING` uses (a local/imported mod has no source to redownload from and must be re-imported instead).

CONVERSION FAILED is read straight from the merged pak's stored fingerprint — the outcome of the last successful sync — rather than recomputed by `verify` itself, so it stays accurate between syncs. NEEDS REINGEST only fires for a convert-eligible pak (both the game and the mod have conversion enabled); a successful `--fix` re-ingest reports as `fixed_needs_reingest` in `--json`, the same "resolved problem, not an outstanding one" convention a successful redownload or version repair uses elsewhere in this section.

## Architecture

The pipeline above is the design; this is where each stage lives. Everything
that decides anything is in `internal/core`, and the two frontends — the CLI
and `lmm serve` — are thin adapters over it: they parse input, call core, and
render what comes back. Neither reaches past core, and core never calls back
into either (a mutation that needs an answer mid-flight returns a typed error
the caller answers by re-running with a different option).

```text
cmd/lmm/                  # CLI entry point (Cobra); imports exactly app/core/domain/source/serve (enforced by a test)
internal/
├── app/                  # Composition root: app.Open resolves paths (XDG), prepares dirs, opens core, registers sources
├── domain/               # Core types (Mod, InstalledMod, Game, Profile) — no external dependencies
├── source/               # The ModSource interface and its implementations
│   ├── nexusmods/        # NexusMods GraphQL client
│   ├── curseforge/       # CurseForge API client
│   ├── steamworkshop/    # Steam Workshop: track subscribed items, search, collections, anonymous steamcmd download
│   ├── thunderstore/     # Thunderstore: the locally cached community index and the search over it
│   ├── icarus/           # Icarus: its mod catalog, plus the .pak/.exmodz merge compiler
│   ├── custom/           # User-defined sources (directory, manifest, api)
│   ├── steam/            # Steam library scanning (for `lmm game detect`)
│   └── httpclient/       # Shared HTTP client (timeouts, size caps, redirect rules)
├── storage/
│   ├── db/               # SQLite (mod metadata, encrypted auth tokens) — pure Go, no CGO
│   ├── config/           # YAML: config.yaml, games.yaml, profiles
│   └── cache/            # The central mod file cache
├── linker/               # Deployment strategies: symlink, hardlink, copy
├── serve/                # `lmm serve`: the SPA (spa/ + vendor/) + /api/v1 JSON + SSE over core.Service
└── core/                 # Every decision lmm makes (one flat package; frontends never reach past it)
```

Inside `core`, one file per concern:

- **The facade and its contracts** — `service.go` (construction and the
  query/mutation concurrency contract), `ops.go`/`oplock.go` (the single
  mutation slot, in-process and across processes), `plan.go` (the freshness
  precondition every Apply re-checks), `errors.go` (the typed errors a
  frontend branches on), `events.go` (the progress vocabulary both frontends
  render), `jsonwire.go` and `queries.go` (the documents `--json` and
  `/api/v1` return).
- **One file per flow**, named for the command that drives it — most a
  `Plan…`/`Apply…` pair, a few (the toggles and the settings writes) a single
  gated call, because there is nothing to preview: `install.go`, `deploy.go`,
  `uninstall.go`, `update.go`, `rollback.go`, `purge.go`, `switch.go`, `profile_apply.go`,
  `profile_sync.go`, `profile_import.go`, `profile_reorder.go`, `adopt.go`,
  `import_archive.go`, `mod_edit.go`, `mod_toggle.go`, `mod_settings.go`,
  `game_add.go`, `game_detect.go`, `game_edit.go`, `snapshot.go`,
  `snapshot_restore.go`, `workshop_adopt.go`, `workshop_collection.go`,
  `verify.go`.
- **The engines the flows share** — `downloader.go`, `extractor.go`,
  `installer.go`, `importer.go`, `updater.go`, `dependencies.go`,
  `resolve.go`, `selection.go`, `conflicts.go`, `converge.go`,
  `deployable.go`, `overrides.go`, `originals.go`, `staging.go`, `fetch.go`,
  `hooks.go`, and `merged_pak.go` (the Icarus compile step).

`cmd/lmm` may import only `internal/{app,core,domain,source,serve}`, and
`internal/serve` only `internal/{app,core,domain}` — both enforced by tests
that read the packages' real imports, so a new dependency either belongs in
that list or the logic that wanted it moves into core.

## File Locations

lmm follows the XDG Base Directory specification. `--config` and `--data` override the resolved directories; `cache_path` in `config.yaml` overrides the cache.

| Type                    | Path                                                                                                                                                                                                                                                                                  |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Config                  | `$XDG_CONFIG_HOME/lmm/` (default `~/.config/lmm/`)                                                                                                                                                                                                                                    |
| Custom Sources          | `<config>/sources/*.yaml`                                                                                                                                                                                                                                                             |
| Database                | `$XDG_DATA_HOME/lmm/lmm.db` (default `~/.local/share/lmm/lmm.db`)                                                                                                                                                                                                                     |
| Credential key          | `$XDG_DATA_HOME/lmm/key` (default `~/.local/share/lmm/key`) — 0600, created on first `auth login`                                                                                                                                                                                     |
| Mod Cache               | `<data>/cache/` (default; not under `XDG_CACHE_HOME` — cached mods are expensive to re-download)                                                                                                                                                                                      |
| Download Staging        | `<data>/downloads/` (in-flight downloads and archive extraction)                                                                                                                                                                                                                      |
| Steam Workshop metadata | `<data>/cache/_steamworkshop/meta/` (cached item descriptions; safe to delete)                                                                                                                                                                                                        |
| Thunderstore index      | `<data>/cache/_thunderstore/<community>/` (the community index searches run against; safe to delete, rebuilt on the next search)                                                                                                                                                      |
| Snapshots               | `<data>/snapshots/<game-id>/` — one `<name>.json` per snapshot, plus `_originals/` (the files lmm has replaced; the only copy of them). A snapshot name may use letters, digits, `.`, `_` and `-`, and may not start with `.` or `_` — which is what keeps `_originals/` out of reach |
| steamcmd home           | `<data>/cache/_steamworkshop/steamcmd-home/` (steamcmd's isolated `HOME`; safe to delete)                                                                                                                                                                                             |

**Precedence.** `--config`/`--data` win outright. Otherwise an `XDG_CONFIG_HOME`/`XDG_DATA_HOME` set to an **absolute** path decides, whether or not `$XDG_…/lmm` exists yet — setting the variable is an explicit instruction, and lmm never silently writes somewhere else (#297). Only when the variable is **unset** — or set to a relative path, which the XDG spec requires be ignored — does lmm fall back to the legacy `~/.config/lmm` / `~/.local/share/lmm`, which is the situation an install predating XDG support is in. If you set an XDG variable and want your existing data, move the directory to the new location (or point `--data`/`--config` at the old one).

The mod cache location can be customized via `cache_path` in `config.yaml`. Setting a per-game `cache_path` in `games.yaml` changes that game's on-disk layout too: the global cache is `cache/<game-id>/<source-id>-<mod-id>/<version>/`, but a game-scoped `cache_path` drops the `<game-id>` segment since the configured directory is already specific to that game (`<cache_path>/<source-id>-<mod-id>/<version>/`).

## Documentation

- **[Configuration reference](docs/configuration.md)** – All options for `config.yaml` and `games.yaml` (including hooks, link method, sources).
- **[Security](docs/security.md)** – How stored credentials are encrypted at rest, what that protects against (and what it does not), and how to recover from a lost or damaged key file.
- **Man pages** – In [`docs/man/man1/`](docs/man/man1/), one page per command and subcommand, generated from the CLI's own `--help` text (`make man`; a drift test fails CI if the pages fall out of sync). View with `man -l docs/man/man1/lmm.1` or install to your man path.
- **[CHANGELOG.md](CHANGELOG.md)** – Release history and notable changes.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** – How to build, test, and submit changes.

## Roadmap

Everything under the first three headings shipped; the last heading is what
is still open. The full entry for each shipped item — what changed and why — is
in [CHANGELOG.md](CHANGELOG.md); the 2.0 line is under `[Unreleased]` until the
release is cut.

### The model

- [x] Profiles as desired state, converged by `lmm profile apply` (downgrades included)
- [x] Version locking, enforced when converging and carried by `profile export`/`import`
- [x] Update policies (`auto`, `notify`, `pinned`) and `lmm update rollback`
- [x] Mod file verification with provenance-aware `--fix` repair
- [x] Conflict detection (file conflicts, circular dependency warnings)
- [x] Automatic dependency installation (opt out with `--no-deps`)
- [x] Snapshots — `lmm snapshot create|list|restore|delete`, over an originals store ([#350](https://github.com/DonovanMods/linux-mod-manager/issues/350))

### Sources

- [x] NexusMods authentication and downloads
- [x] CurseForge integration
- [x] User-defined `directory`, `manifest` and `api` sources, with capabilities and live validation
- [x] Additional first-party built-in sources beyond NexusMods/CurseForge (Icarus)
- [x] Steam Workshop: tracking, search, collection import, and anonymous download ([#269](https://github.com/DonovanMods/linux-mod-manager/issues/269))

### Interfaces and packaging

- [x] Default game setting (avoid `--game` on every command)
- [x] `lmm serve` — the second frontend: a full local web UI over the same core, with `/api/v1` and SSE
- [x] `lmm init` — a guided first run ([#351](https://github.com/DonovanMods/linux-mod-manager/issues/351))
- [x] Encrypted credential storage ([#79](https://github.com/DonovanMods/linux-mod-manager/issues/79))
- [x] Packaging: AUR, `.deb`, `.rpm`, `.apk` ([#352](https://github.com/DonovanMods/linux-mod-manager/issues/352))

### In progress for 2.0

- [ ] BepInEx support, tiered: plugin archives deploy correctly, the loader
      handled as a per-game prerequisite rather than a mod, and a Thunderstore
      source gated behind a locally cached index
      ([#357](https://github.com/DonovanMods/linux-mod-manager/issues/357),
      [#360](https://github.com/DonovanMods/linux-mod-manager/issues/360))
- [ ] A documented game-adapter seam — archive normalisation, compile, file
      routing and verify — so Unreal, Unity and the rest land without touching
      the generic core; Icarus and BepInEx move behind it
      ([#353](https://github.com/DonovanMods/linux-mod-manager/issues/353))

Game auto-detection beyond Steam (Lutris, Heroic, Flatpak) was considered and
declined ([#89](https://github.com/DonovanMods/linux-mod-manager/issues/89)):
Steam detection works because Steam has one documented on-disk layout, and the
other launchers each need their own parser against an unstable private schema,
to save one step of a command most people run once per machine. `lmm game add
--path` adds any of them by hand today. The issue will be reopened if a
specific launcher draws real demand. FOMOD
([#354](https://github.com/DonovanMods/linux-mod-manager/issues/354)) and
automated LOOT-style plugin sorting
([#355](https://github.com/DonovanMods/linux-mod-manager/issues/355)) are
[non-goals for 2.0](#non-goals-for-20), not backlog — lmm's own load ordering
is shipped and unaffected by either.

## Development

```bash
# Run tests
go test ./...

# Format code
go fmt ./...

# Vet code
go vet ./...

# Build
go build -o lmm ./cmd/lmm
```

`make build` (or `make`) is the preferred way to build a working binary: it
stamps `git describe --tags --dirty` into the binary, so `lmm --version` on
a dev build self-identifies as e.g. `2.0.0 (dev: v2.0.0-2-g140e3c6-dirty)`
instead of silently claiming the last released version. A plain
`go build`/`go test` (no ldflags) behaves exactly like a clean release build.

### `lmm serve`'s front end

There is **no Node, no npm and no bundler** anywhere in this project, and
there never will be: `go build` is the entire build, and a user installs
nothing. The web UI is plain ES modules a browser loads directly.

- `internal/serve/spa/` — the shell (`index.html`), the stylesheet
  (`app.css`, hand-written CSS custom properties, two full token sets for
  dark and light), and the application's own modules under `app/`.
- `internal/serve/vendor/` — Preact and htm, at pinned versions, committed.
  Each file carries a header naming its package, exact version, source URL
  and the SHA-256 of the upstream artifact, plus any local edit made to it.
  **Nothing in this repo ever fetches them**; upgrading one is a deliberate,
  reviewed commit that replaces the file and updates its header.

Both trees are served from the binary via `go:embed`. Several ratchets keep
the front end honest: `no_unsafe_dom_test.go` fails the build on any
raw-markup write (`dangerouslySetInnerHTML`, `.innerHTML`,
`document.write`, …), any `eval`, or any import over the network;
`TestSPAModuleGraphResolvesOverHTTP` walks the module graph from the entry
point and requires every import to resolve — without a bundler, nothing
else would catch a bad path before a browser did;
`no_hardcoded_color_test.go` refuses any literal colour outside the token
blocks, so both themes really do apply everywhere; and
`contrast_test.go` holds every one of those tokens to WCAG 2.1 AA contrast
in both palettes and pins the two dark token blocks to the same values.
The browser E2E (`e2e_test.go`, real headless Chrome via `chromedp`,
skipped when no browser is on PATH) is the only thing that EXECUTES the
SPA, so it is the only thing that can see a CSP refusal, a 404 module, or
a screen where the keyboard loses focus.

## License

MIT License - See [LICENSE](LICENSE) for details.

## Acknowledgments

- [Cobra](https://github.com/spf13/cobra) - CLI framework
- [NexusMods](https://www.nexusmods.com/) - Mod hosting platform
- [CurseForge](https://www.curseforge.com/) - Mod hosting platform
