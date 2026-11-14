# The lmm collection format

**Format version 1** · issue [#621](https://github.com/DonovanMods/linux-mod-manager/issues/621) · design: [docs/plans/2026-11-10-collections-design.md](plans/2026-11-10-collections-design.md) §12

An lmm collection is a shareable profile: the list of mods a profile holds,
with what a recipient needs to install the same thing on another machine.
It is what `lmm profile export` writes and `lmm profile import` reads. There
is one format, the profile export, versioned: a document without the
`lmm_collection` key is version 0, the export older versions of lmm wrote,
and it still imports exactly as it always did.

A collection points at each mod on its source. It never carries the mods
themselves: the recipient downloads each one with their own account.

## Files

A collection is either a YAML document or a `.lmmc` container.

- **YAML document.** What `lmm profile export <name>` writes to stdout, and
  `lmm profile export <name> -o <file> --plain` to a file. Good for diffs,
  git and gists.
- **`.lmmc` container.** What `lmm profile export <name> -o <file>` writes,
  and the web UI's **Export** downloads. A zip archive (deflate) holding:

  | Member                | Meaning                                                               |
  | --------------------- | --------------------------------------------------------------------- |
  | `collection.yaml`     | The YAML document. Required, exactly once.                            |
  | `collection.yaml.sig` | Reserved for a detached signature (#624). Ignored, with a warning.    |
  | `files/…`             | Reserved for an author's own bundled files (#623). Ignored, with a warning. |

  Any other member is ignored, with a warning naming it.

An importer tells the two apart by the first bytes: `PK\x03\x04` is a
container; anything else is a YAML document, of version 0 or 1.

## The document

```yaml
lmm_collection: 1            # the format version; absent means version 0
id: 2f8c7a3e-…               # stable across every version of this collection
version: 3                   # the author's number for this publish
name: coop                   # the profile name (version 0's meaning)
title: Welcome to Night City # the display name, when it is not the profile name
author: …
description: |-
  …
install_instructions: …      # shown before importing
game_id: cp2077              # the author's local game id, for older lmm
game:                        # the portable game identity an importer matches
  name: Cyberpunk 2077
  steam_app_id: "1091500"
  sources: { nexusmods: cyberpunk2077 }
  versions: ["2.5.0.0"]      # tested game versions, informational
requires: [external]         # features the content needs (below)
origin:                      # present when someone else curated the collection
  source_id: nexusmods
  slug: iszwwe
  revision: 484
  url: https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe
  author: …
signer: …                    # reserved for signed collections (#624); ignored
mods:                        # load order, first = lowest priority
  - source_id: nexusmods
    mod_id: "107"
    version: 1.8.0
    file_ids: ["1234"]
    locked: true
    disabled: true           # listed, but switched off
    category: UI             # the author's own category
    mod_type: dinput         # where the whole mod deploys (requires: mod_type:dinput)
    fomod:                   # recorded FOMOD installer choices (requires: fomod)
      config_sha256: 3f1c…   # the ModuleConfig.xml they answer
      steps:
        - name: Main
          groups:
            - name: Textures
              choices: [{ name: 4K, idx: 1 }]
      file_states: { skse64_loader.exe: Active }
    name: Much Better Ammo   # display: readable without the source
    files:                   # per-file facts, where the source provides them
      - { id: "1234", md5: 25aa…, sha256: …, size: 1234567 }
  - source_id: nexusmods     # a mod whose files each carry an installer
    mod_id: "43532"
    file_ids: ["100", "101"]
    fomod_files:             # its choices per file, by file id (requires: fomod_multi_file)
      "100": { config_sha256: 9e0a…, steps: [ … ] }
      "101": { config_sha256: 51c7…, steps: [ … ] }
    fomod_order: ["101", "100"]  # optional: the order those files layer in (else by file id)
external:                    # items lmm cannot fetch: listed, never downloaded
  - name: Cyber Engine Tweaks
    url: https://…
    instructions: Install it by hand before the first launch.
hooks:                       # the profile's own hooks; scripts lmm would run
  install:
    after_all: /home/author/bin/after.sh
overrides:                   # config files written into the game directory
  engine/config/user.ini: |
    [Section]
    key=1
plugins:                     # a Bethesda game's plugin list (requires: plugin_order)
  order:
    - name: SkyUI_SE.esp
    - name: Optional Thing.esp
      enabled: false
  rules:
    plugins:
      - { name: Patch.esp, after: [SkyUI_SE.esp] }
```

### Keys

| Key                    | Meaning |
| ---------------------- | ------- |
| `lmm_collection`       | The format version. `1` for this document. An importer that reads a lower version refuses a higher one ("made by a newer lmm"). |
| `id`                   | The collection's stable id. The first export of a profile mints a random UUID and records it in the profile, so every later export keeps it. A profile imported from a collection keeps that collection's id; a duplicated profile (`lmm profile duplicate`) is a new collection and mints its own. |
| `version`              | The author's version number for this publish (`--collection-version`). Defaults to the profile's, else 1. It is never raised automatically. |
| `name`                 | The profile name. It keeps version 0's meaning so an older lmm imports the document under the same name. An importer that cannot use it as a file name derives one from it or from `title`; `--as` chooses another. |
| `title`, `author`, `description`, `install_instructions` | The author's words, shown to the recipient before anything is written. |

What an export publishes - the id and every value its flags set - is saved
in the profile's `collection:` block, so the next export reuses it. The
save happens only when something changed; if it fails, the export fails. A
duplicate keeps `title`, `author`, `description`, `install_instructions` and
`origin` as starting values, and drops `id` and `version`.
| `game_id`              | The author's local id for the game. A version-1 importer ignores it; an older lmm imports by it. |
| `game`                 | The portable game identity. An importer matches a local game by `steam_app_id` first, then by any `sources` entry its own game maps to the same id - unless that game's own Steam app id is known and differs (Fallout 4 and Fallout 4 VR share a Nexus id). None, or more than one, needs `--game`. |
| `requires`             | The features the content needs. Derived by the exporter, never written by hand. |
| `origin`               | Where the collection came from, when someone else curated it. Carried through every re-export, so the curator stays credited. A profile imported from a URL also records `origin.import_url` (below), which no document carries. |
| `signer`               | Reserved (#624). Ignored, with a warning. |
| `mods`                 | The mods, in load order. Each entry is a profile mod reference (`source_id`, `mod_id`, `version`, `file_ids`, `locked`, `disabled`, `category`, and every key a profile reference gains later), plus `name` and `files`. |
| `external`             | Items lmm cannot fetch, with a URL and the curator's instructions. They are recorded on the imported profile, so a re-export keeps them. |
| `hooks`                | The profile's hooks, in the profile file's encoding: a hook that is absent inherits the game's, and one set to `""` disables it. |
| `overrides`            | Config files (path relative to the game directory → content). Deploy keeps them inside the game directory, as it does every override. |
| `plugins`              | The profile's plugin list: `order` (plugin names, first loads first, each with `enabled: false` when it is listed but not active) and `rules` (`groups` and per-plugin `group`, `after` and `req`, in the shape of a LOOT userlist). Requires `plugin_order`. An importer refuses, rather than rewrites, a name that is empty, holds a control or formatting character or a path separator, or is over 1024 bytes. |

A key the importer does not recognise, and that `requires` does not name,
is informational and ignored.

**Never exported:** the profile's link method and its default flag. Both
are this machine's preferences.

**Compatibility with older lmm.** An older lmm reads `name`, `game_id`,
`mods`, `hooks` and `overrides`, and ignores every other key, so it imports
a version-1 YAML document as the profile it describes (on a machine with
the same game id). That is why `external` items are a top-level list and not
entries in `mods`: an older reader would take such an entry for a mod with
no source. (The design's §12 sketch put them in `mods` with
`kind: external`; the format moved them out for this reason.) A `.lmmc`
container needs version 1 to read.

## Importing from a URL

`lmm profile import https://…` (the web UI's **From a URL**) fetches a
collection from an address instead of a file:

- **https only.** An `http://` address is refused, and so is a redirect to
  anything that is not `https` (before it is requested). At most 10
  redirects are followed, and the whole download has a two-minute limit.
- **What it reads.** The first bytes decide, as for a file: `PK\x03\x04` is
  a `.lmmc` container (at most 64 MiB), anything else a YAML document (at
  most 4 MiB), version 1 or version 0. Both caps hold as the body arrives,
  not after it is downloaded. A body that is neither a collection nor a
  profile export (a web page, say) is refused. A version-0 export imports
  into `--game`, else the configured game its `game_id` names, and takes
  `--as` as a collection does.
- **Hooks are never imported from a URL**, even with `--with-hooks`, which
  is ignored rather than refused. The plan lists every hook and says so
  (`hooks_importable: false`), and the result counts them
  (`hooks_withheld`). The overrides gate is the same as for a file.
- **The address is recorded** on the imported profile, as
  `collection.origin.import_url`, beside whatever `origin` the document
  carries:

  ```yaml
  collection:
    id: 2f8c7a3e-…
    version: 3
    origin:
      author: …                                        # the document's own credit, kept
      import_url: https://example.com/coop.lmmc?dl=1   # where this machine fetched it
  ```

  It is the address as given, without its `user:password@` part (used for
  that one download and never stored) or its `#fragment`. It keeps the
  query string, which some hosts need to serve the file. A collection
  update (#613) fetches it again and compares the document's `id` and
  `version`. `import_url` is this machine's record: lmm sets it only at a
  URL import, ignores it in a document, and leaves it out of every export
  (`lmm profile export`, `-o`, `--json`, and the web UI's) and of a
  duplicated profile.
- **What lmm shows.** The address is unredacted only in the profile file
  (and lmm's own snapshots of it). Everywhere lmm shows or logs it - the
  plan, every `--json` document (the profile commands' `profile` result
  included), every `/api/v1` answer (the profile routes and
  `POST /api/v1/profiles/{name}/collection` included), messages, errors and
  logs - it shows it without credentials, and with any query string
  replaced by `?…`. A redirect never carries the address on as a
  `Referer`, and an error never quotes the server's own status text.

## `requires` and the feature gate

`requires` names the features a collection needs an importer to support.
An importer compares it, together with what it can see in the content
itself, against its own supported set. A collection needing any feature
outside that set still shows its plan, saying what is missing, and its
import is refused with nothing written (`CollectionUnsupportedError`;
`--json` names each missing feature). There is no override: importing what
lmm cannot replay is the result that looks like it worked and doesn't.

A newer lmm adds a feature by adding its name to the supported set, which
is what lets the format grow without breaking older importers.

| Feature    | Raised by                         | Supported from |
| ---------- | --------------------------------- | -------------- |
| `external` | any `external` item               | format v1 (lmm with #621) |
| `fomod`    | any mod with a `fomod` block (its recorded FOMOD installer choices) | format v1 (lmm with #614) |
| `fomod_multi_file` | any mod with a `fomod_files` block: its FOMOD choices recorded per file, for a mod two or more of whose files each carry an installer (also raises `fomod`) | format v1 (lmm with #634). An older lmm, which installs one installer per mod, refuses it |
| `plugin_order` | a `plugins` block (the profile's plugin load order and rules) | format v1 (lmm with #616) |
| `mod_type:<type>` | any mod with that `mod_type` (a stated mod type, as a NexusMods collection records it: `dinput` is a script extender, deployed to the game folder) | lmm with #607, for a game whose adapter routes the type (the `bethesda` adapter: `dinput`, `enb`). Elsewhere it is refused, because the mod would deploy where the game never reads it |

## Importing safely

A collection from someone else is untrusted input.

- **Hooks are code.** Every hook is listed in the plan. From a local file,
  they are imported only with `--with-hooks` (in the web UI, the **Import
  these hooks** box); without it the profile is imported without them, and
  the result says so (`hooks_withheld`). A collection fetched from a URL
  never has its hooks imported, even with `--with-hooks` (see
  [Importing from a URL](#importing-from-a-url)). A hook command holding a control, escape,
  or bidi/format character is refused, never cleaned: the plan must show
  exactly what would run.
- **Overrides** are listed in the plan. One that targets a text config
  file (`.ini`, `.cfg`, `.conf`, `.txt`, `.json`, `.xml`, `.toml`, `.yaml`,
  `.yml`, `.properties`, `.config`, `.ltx`, `.lsx`, `.cvars`, `.ron`, in any
  case) is imported, unless it is a loader configuration that can make the
  game load a DLL (`doorstop_config.ini`, `enblocal.ini`, by file name in
  any case - see the known gaps below); any other is skipped unless
  `--allow-any-overrides` (the web UI's **Import these overrides too**),
  and the plan (`restricted_overrides`) and the result
  (`overrides_skipped`) name each. Whatever its type, a path that is
  absolute, escapes the game directory, or holds a control or formatting
  character is refused before anything is written, and deploy writes
  overrides only inside the game directory, never through a link. An
  override of a deployed mod's own file (a symlink into lmm's mod cache, or
  a hardlink) is skipped with a warning for now (#630); each override is
  written or skipped on its own.
- **Version 0 too.** The hooks and overrides rules apply to every profile
  import from a file or upload, a version-0 document included, so the
  older format is no way around them. They do not apply to profiles you
  already have, or to overrides you set yourself.
- **Links.** Only an absolute `http`/`https` address stays a link (an
  external item's `url`, `origin.url`). Any other - `javascript:`, `data:`,
  `file:`, a relative one - is dropped from the item, and the plan names it
  in a warning, as text.
- **Sources you do not have.** A mod from a source this machine lacks (a
  custom source, say) is listed with what to do. A collection never carries
  a custom source's definition, since those can make requests with your
  credentials; ask the collection's author for it.
- **Text** is sanitised before it is shown: control characters and
  direction overrides are removed. Names that are identifiers rather than
  text (`game.sources` keys) are refused instead. So are the names in a
  `fomod` block - step, group and option names, and `file_states` paths -
  which are matched against the installer exactly as written; a block
  whose `config_sha256` is not 64 lower-case hex digits, whose file state
  is not `Missing`, `Inactive` or `Active`, or that holds more than 10,000
  recorded entries (steps, groups and picks together) or file states is
  refused too. A `fomod_files` block is held to the same rules for each
  file, and its keys must be file ids the mod pins (`file_ids`), free of
  control and formatting characters; so must a `fomod_order` list's
  entries, which may not repeat, and which need a `fomod_files` block to
  order.
- **A version-0 document** never carries a `collection` or `external`
  block, so the version-0 import ignores both: only a checked version-1
  document sets them.
- **Caps.** A container is at most 64 MiB with at most 4096 members, and
  `collection.yaml` at most 4 MiB, inflated through a limit; a member
  compressed more than 100:1 past 1 MiB is refused unread. A document lists
  at most 10,000 mods, 1,000 external items and 256 overrides; a one-line
  value is at most 1 KiB, a description or instructions at most 64 KiB.
  Member names go through the same zip-slip and reserved-name checks as
  mod archives. The web UI's import posts the file inside its request,
  which holds about 700 KiB of container; a larger one imports with
  `lmm profile import`.

Failures are counted per mod, as an ordinary import's are: a mod that
cannot be downloaded fails on its own and the rest are imported. A mod that
needs something set up first (a mod loader) stops the import before
anything is written.

## Known gaps

- **Loader configurations not yet on the list.** The list of loader
  configurations held back by name is short on purpose: each entry is a
  file verified to make a game load a DLL. Two more can likely do it too,
  but are not verified yet, so they import like any other text config:
  - `ReShade.ini`, whose `[ADDON]` `AddonPath=` names where ReShade loads
    add-on DLLs from;
  - the Ultimate ASI Loader's `.ini` files, which choose the folders it
    loads `.asi` plugins (DLLs) from.
- **Overriding a mod's own file.** An override of a file a mod deployed
  (a symlink into lmm's mod cache, or a hardlink) is skipped with a
  warning, because writing it would change the cached mod for every
  profile. #630 will replace the deployed file with a regular copy that
  holds the override.
