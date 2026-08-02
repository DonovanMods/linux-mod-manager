# Icarus `.exmod`/`.exmodz` → PAK Compilation: Research Spike & Design

**Status:** Design (pre-implementation). Originates from [`IDEAS.md`](../../IDEAS.md) "Specialized Game Mod Support" section; no GitHub issue filed yet — file one before starting implementation per repo workflow.

**Date:** 2026-07-29

## Background

Icarus is an Unreal Engine game with two mod distribution formats:

- **`.pak`** — a finished, ready-to-deploy Unreal PAK override file. Deploying these is a solved problem in LMM already (a straight file copy, same shape as the existing `DeployCopy` mode).
- **`.exmodz`** — a zip archive containing an `.EXMOD` JSON manifest (a _diff_ against the base game's data tables) plus, optionally, pre-built `.uasset`/`.uexp` files the mod author already compiled in the Unreal Editor. Turning this into something the game can load requires **building a new `.pak` file on the fly**, which is the actual project: nothing in LMM's pipeline can produce a `.pak` file today, and doing so on Linux (no Unreal Editor, no `UnrealPak.exe` without Wine) is the open question this spec addresses.

The mod catalog itself (metadata + download URLs) lives in a Firestore database the user maintains, currently consumed only by a server-rendered Rails site ([`project_daedalus`](https://github.com/DonovanMods/project_daedalus)) and a Ruby CLI (`icarus-mod-tools`) via an authenticated service-account keyfile. There is no existing public JSON API.

## Scope

**In scope:** end-to-end support for installing an Icarus mod distributed as `.exmodz` — from browsing the catalog through producing and deploying a working `_P.pak`.

**Explicitly out of scope for this spec:**

- Any other Unreal-Engine game (Satisfactory, etc.) — see [Future Potential](#future-potential-not-designed-now).
- Compression or encryption support in the PAK writer beyond the minimum needed for Icarus (uncompressed, unencrypted).
- Redistributing Epic's `UnrealPak.exe` (unlike IMM) or requiring Wine.

## Research Findings

Grounded in a real sample (`Bear_Mount.EXMODZ`), the `icarus-mod-tools`/`project_daedalus` repos, and public modding-community sources — not speculation:

- **Engine/format**: Icarus runs UE 4.26.2/4.27, classic `.pak` format (not UE5 IoStore). This is the best-case format to target: well-documented, well-tooled elsewhere.
- **No encryption**: community guides routinely unpack `data.pak` with plain `UnrealPak.exe`; no AES-key-extraction step appears anywhere in the modding community's process.
- **Confirmed prior art**: Jimk72's [`Icarus_Software`](https://github.com/Jimk72/Icarus_Software) repo (the actual IMM source) ships a redistributed `UnrealPak.zip` alongside `DUMP_Week_140.zip` — a pre-extracted, per-week-build cache of the base game's data files. This confirms the `compatibility: "w57"`-style field used throughout the mod catalog is Icarus's own weekly build numbering, and confirms IMM's actual pipeline: unpack base data (from a cached dump) → apply diff → repack with `UnrealPak.exe`.
- **`.EXMOD` diff shape** (from the real sample): a JSON document with mod metadata (`name`, `author`, `version`, `description`, ...) and a `Rows` array of `{"CurrentFile": "<data-table json filename>", "File_Items": [{"Name": "<row key>", <field overrides>}]}`. This patches **plain JSON data tables already shipped inside the game's PAK** — not compiled binary assets, and not anything requiring an Unreal "cook" step.
- **`.EXMODZ` bundled assets**: the same sample also ships finished `.uasset`/`.uexp` pairs (new skeletal meshes, animations, blueprints, icons) that the mod author already compiled externally. These need placing into the output PAK at the correct path, not compiling.
- **Linux-native tooling precedent**: [`repak`](https://github.com/trumank/repak) (Rust) reads/writes this exact PAK version range natively on Linux with no Wine or Epic binary — proof the underlying problem is tractable on Linux, even though LMM won't shell out to it (see [Package Layout](#package-layout)).
- **No mature Go PAK library**: [`pakr`](https://github.com/recogni/pakr) exists but is WIP/limited — LMM needs a small purpose-built reader/writer, not an off-the-shelf dependency.
- **Unresolved even by experienced modders**: a public GitHub issue ([masterj1337/IcarusMods#1](https://github.com/masterj1337/IcarusMods/issues/1)) shows a modder packing a `.pak` correctly with uncertainty over whether `-Compress` is required, and the result not taking effect in-game. Exact packing parameters are not fully settled community knowledge — this needs direct empirical validation, not assumption.

## Proposed Architecture

### Icarus ModSource (catalog access)

A new built-in `ModSource` at `internal/source/icarus/`, structurally parallel to `internal/source/nexusmods/` and `internal/source/curseforge/` (hand-written Go client, registered in `Service.RegisterSource()` like any other built-in) — **not** a YAML `custom` source, since Firestore's typed-value REST document format (`{fields: {name: {stringValue: "..."}}}`) doesn't fit `custom.API`'s flat-JSON dot-path field mapping.

- Reads Firestore's public REST API directly (`https://firestore.googleapis.com/v1/projects/{project}/databases/(default)/documents/...`) with **no credentials** — confirmed the `mods` collection will have public read rules.
- No `cloud.google.com/go/firestore` SDK dependency (gRPC/protobuf, heavy) — plain `net/http` plus a small typed-value decoder, same dependency weight as the existing NexusMods/CurseForge clients.
- `Search` mirrors `project_daedalus`'s own approach: Firestore's simple REST surface doesn't support server-side text queries the way NexusMods/CurseForge do, so the source fetches the `mods` collection (paginated) and filters client-side by name/author/description — exactly what `ModsController#find_mods` already does today.
- Field mapping, per `modinfo.json.template.md` v2: `name`, `author`, `version`, `compatibility` (Icarus week-build string), `description`, `files.pak` / `files.exmodz` (direct download URLs — GitHub raw links in practice), `imageURL`, `readmeURL`. `GameID` is fixed to `"icarus"` (single-game database).
- `GetDownloadURL` returns the stored URL directly — no signing/redirect dance needed, closer in shape to `custom.Manifest`/`custom.Directory` than to NexusMods' OAuth flow.

### Compile pipeline (the core of this project)

Triggered when the selected downloadable file is an `.exmodz`, positioned as a new pre-deploy stage between "cache has final source files" and the existing `Linker.Deploy`:

1. **Base data comes from the local install, not a hosted dump.** LMM reads whatever `data.pak` is actually present in the user's installed game right now, rather than replicating IMM's per-week `DUMP_Week_N.zip` cache (which depends on someone continuing to host those dumps indefinitely). This always diffs against ground truth and is more robust than IMM's own approach.
2. **Targeted extraction** — parse just the PAK index (not the full multi-GB archive: `repak`'s own docs note it "only parses index initially, reads file data upon request," and the Go reader should follow the same shape), then extract only the specific files each exmod's `Rows[].CurrentFile` entries reference.
3. **Apply the diff** — pure JSON manipulation: for each referenced base file, merge in `File_Items[].Name`-keyed field overrides. No Unreal-specific tooling needed for this step.
4. **Assemble the mod layer** — patched JSON files at their PAK-internal paths, plus any `.uasset`/`.uexp` files already bundled in the `.EXMODZ`, placed as-is.
5. **Write a new override `_P.pak`** via the Go PAK writer (uncompressed/unencrypted to start — see [Open Risks](#open-risks)), following the community's established `_P.pak` → `Content/Paks/mods` convention.
6. **Deploy** via the existing `Linker` unchanged — at this point it's a single finished file, same shape as `DeployCopy`.

### Package layout

- `internal/unrealpak/` — game-agnostic PAK index reader + writer for the UE 4.25–4.27 unencrypted format range. Zero Icarus-specific knowledge. This is the piece a future Satisfactory (or other UE game) effort could potentially reuse.
- `internal/source/icarus/` — the Firestore `ModSource`, the `.EXMOD` diff-application logic (JSON row merging), and `.EXMODZ` → PAK-path mapping. Uses `internal/unrealpak/` but owns all Icarus-specific knowledge.
- Deploy pipeline: the compile step needs a new hook point that doesn't exist today (`Linker.Deploy` is strictly 1:1 file placement, and `Game.DeployMode` is a closed `Extract`/`Copy` enum). Proposed shape: an optional `Compiler` capability a source/game can provide (mirroring the existing `CapabilityReporter`/`TypeLabeler` type-assertion pattern already used for optional `ModSource` behavior), invoked between cache population and `Linker.Deploy` when present. Exact interface and wiring is an implementation-plan decision, not finalized here.

### Error handling

No silent fallbacks. If the installed `data.pak`'s version/compression/encryption doesn't match what the reader expects, or a `Rows[].CurrentFile` target isn't found in the base data, this fails loudly with a clear, actionable error — consistent with this repo's existing fail-fast precedent (#95).

### Testing

Real Icarus game files cannot ship as test fixtures (not ours to redistribute). The reader/writer gets round-trip tests in CI (write a synthetic PAK with our own writer, read it back, assert index/content correctness byte-for-byte) plus diff-application unit tests against a synthetic base JSON + a real `.EXMOD` sample's `Rows` shape. Validation against a genuine `data.pak` happens manually, locally, outside the automated suite — and is also where the format assumptions below get confirmed or falsified.

## Open Risks

- **Everything above is inferred from community discussion, not verified against real bytes.** Before writing any Go PAK code, the concrete next step is validating the actual format against a real local `data.pak`: confirm the PAK version/footer magic, confirm zero encryption in practice, and confirm the `.EXMODZ` internal folder structure (`ASS/`, `BP/`, ...) maps directly to Icarus's in-PAK mount paths with no translation layer.
- The unresolved `-Compress` community issue suggests packing parameters matter in ways not fully settled even by experienced modders — plan to empirically test both compressed and uncompressed output against a real local install before committing to one.
- This is a multi-week effort with no existing library to lean on for the hard part (no mature Go PAK library) — implementation-plan sizing should reflect that, not treat it as a quick add-on.

## Future Potential (not designed now)

The user has flagged that this may lay groundwork for supporting other Unreal-Engine-based games (Satisfactory is explicitly mentioned in `IDEAS.md`, requiring its own separate tooling — "Satisfactory Mod Loader," ficsit.app API). Keeping `internal/unrealpak/` free of Icarus-specific assumptions is a deliberate, low-cost choice that keeps that door open. This is _not_ a commitment to build Satisfactory support, and no Satisfactory-specific design work has been done — a future effort there would need its own research spike (different UE version, different mod-loader mechanism, entirely unverified).

## Out of Scope Recap

- Encrypted or IoStore PAK support.
- Any non-Icarus game.
- Redistributing Epic's `UnrealPak.exe` or requiring Wine/Proton.
- Auth/write access to Firestore (read-only, public).
