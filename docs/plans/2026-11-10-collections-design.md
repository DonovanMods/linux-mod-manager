# NexusMods Collections, FOMOD and plugin order — design (#597, #354, #355)

**Date:** 2026-11-10 · **Status:** For review · **Issues:** [#597](https://github.com/DonovanMods/linux-mod-manager/issues/597) (Collections), [#354](https://github.com/DonovanMods/linux-mod-manager/issues/354) (FOMOD), [#355](https://github.com/DonovanMods/linux-mod-manager/issues/355) (plugin order)
**Inputs:** the three spikes in [2026-11-10-collections-spike-findings.md](2026-11-10-collections-spike-findings.md) (every external fact below is theirs, with its VERIFIED/INFERRED/UNVERIFIED tag there), the game-adapter seam ([2026-09-10-game-adapter-design.md](2026-09-10-game-adapter-design.md)), the Steam Workshop collection path ([2026-09-09-steam-workshop-design.md](2026-09-09-steam-workshop-design.md) §4), the manual-download flow (#543), and the v2 rules (Plan/Apply, typed errors, thin frontends, additive JSON) in [2026-08-27-v2-core-refactor-design.md](2026-08-27-v2-core-refactor-design.md).

No code in this unit. Each numbered section is a decision, not a menu; §11 holds the genuine forks.

---

## TL;DR

A NexusMods collection is two things: a **list of pinned files** (cheap — the Nexus GraphQL API returns it, keylessly) and a **recipe for installing them** (`collection.json` inside the revision's archive: FOMOD choices, plugin order, mod order rules, bundled files, binary patches). #597 is blocked because shipping the list without the recipe installs a Bethesda setup that looks right and plays wrong.

This design keeps that promise without waiting for everything:

1. **The gate is the collection's own manifest, and it fails closed.** Core reads `collection.json`, derives the set of *features* the collection needs (`fomod`, `plugin_order`, `patches`, `bundled`, `file_list`, …), and refuses to apply an import whose features are not all in lmm's supported set. Nothing is special-cased by game: a Stardew collection with no installer choices imports on day one; a Skyrim collection waits for the features it actually uses. Each later story *adds a feature to the supported set*, and that is the whole release mechanism. The manifest is not the only witness: an archive that turns out to contain a FOMOD the manifest recorded no choices for raises `fomod` too, at acquire time, before anything is written (§1, §3).
2. **FOMOD is a core install step, not a Bethesda-adapter capability.** FOMOD is an archive format detected from the archive (`fomod/ModuleConfig.xml`), used by Cyberpunk, Witcher 3, BG3 and Mount & Blade mods as much as Skyrim's. #354's text puts it in a `bethesda` adapter; this design moves it to core, with adapters supplying only what is game-specific (plugin state for `fileDependency`). See §4.
3. **Plugin order is a `bethesda` adapter capability plus a per-profile plugin list.** The adapter is a pure rule table (where the list file lives, its format, implicit plugins, header flag bits, limits); core reads plugin headers, sorts, and writes `Plugins.txt` into the game's Proton prefix — a location lmm does not model today and gains here. Tier 1 is the text-file games (Skyrim SE/AE/VR, Fallout 4/VR, Starfield, Skyrim LE). The timestamp-ordered games are deferred. See §5.
4. **No new dependencies.** FOMOD is `encoding/xml` + `unicode/utf16` + a 32-entry Windows-1252 table; plugin headers are `encoding/binary`; BSDIFF40 patches are `compress/bzip2`. Nothing GPL is linked (lmm is MIT; Vortex, libloot, libloadorder and esplugin are GPL — read for behaviour, never ported).

---

## 1. What a collection is, and what lmm makes of it

A collection is a mod list, which is what an lmm profile is. The Workshop precedent holds as far as the *plan*: **a collection import plans as a profile import**. It builds an exported-profile document and hands it to `PlanImport`, inheriting the staleness snapshot, the buckets and the plan/apply round trip.

The *apply* cannot be plain `ApplyImport`, for two reasons. A Nexus import **installs** (Workshop forces `NoInstall` because acquisition is Steam's), and `ApplyImport` treats each item independently: it counts a failed item and moves on (`profile_import.go`, `result.Failed++`), saving items 1…N−1 before item N fails. That is right for an ordinary import. For a collection it is wrong, because some refusals are only discoverable after a download (an archive that turns out to hold a FOMOD), and a half-saved collection is the "looks like it worked" outcome. A re-run would also fail `checkPlanFresh` with `ErrStalePlan`, because the earlier items changed the snapshot.

### Two-phase apply: acquire, then commit

`ApplyCollectionImport` (and, from F2 on, every install-shaped Apply that can meet a FOMOD: install with dependencies, import, the update batch, `profile fill`) runs in two phases:

1. **Acquire.** For every item, in phase order: download (or find in the cache), verify, extract into staging, and resolve installer choices. Nothing is written to the cache, the DB or the profile. Downloaded archives are kept through `retained_download.go`, which exists for exactly this (an ingest refused for a reason the user can fix without re-fetching), gaining a second refusal reason next to the BepInEx loader precondition.
2. **Decide.** Every refusal from the acquire phase is collected. If any exist, Apply returns **one** typed error covering all of them, with nothing written: `*core.CollectionUnsupportedError` for features lmm cannot replay, `*core.FomodChoicesRequiredError` (keyed by mod) for questions the user can answer. The plan is still fresh, because nothing changed.
3. **Commit.** Only when acquire is clean: cache commit, DB rows, profile save and deploy, through the existing installer, under the Ruling 16 pairing.

The re-run is the existing #225 round trip (conflict Overwrite): the CLI re-runs Apply with the answers in its Options, and the SPA re-plans and re-posts, since serve's plans are single-use (`plans.go` `Take`). The retained archives make the re-run cheap.

The profile has to carry more per mod than an id; §4–§6 add those fields.

### The two halves, and where each comes from

| Half | Source | Auth | Carries |
|---|---|---|---|
| **The list** | GraphQL `collectionRevision(slug, revision, domainName, viewAdultContent)` → `modFiles[]`, `externalResources[]` | none (verified live, anonymously); lmm sends `apikey` anyway | per file: `fileId`, `optional`, `updatePolicy`, `version`, mod id/name, file name, size |
| **The recipe** | the revision's `downloadLink` (an API path) → a `.7z` with `collection.json`, `bundled/`, `patches/` | API key required (anonymous → 401) | per mod: `source` (type nexus/direct/browse/manual/bundle, md5, size), `choices` (FOMOD), `hashes` (exact file list), `patches`, `phase`, `instructions`, `fileOverrides`; top level: `modRules`, `plugins`, `pluginRules`, `collectionConfig`, `info.installInstructions` |

**Decision: core always fetches the recipe.** The list alone cannot answer "does this collection need FOMOD?", and that question is the gate. If the recipe cannot be fetched (no key; a free account turns out to be refused — UNVERIFIED, see §11 Q1), the plan still renders the list, but its verdict is `manifest_unavailable` and Apply refuses. Fail closed.

### Reference forms

`lmm profile import --nexus-collection <ref>` where `<ref>` is a slug (`rqhcxy`), a collection URL (`https://www.nexusmods.com/games/<domain>/collections/<slug>`), either optionally suffixed with a revision (`<slug>@<n>`; a `…/revisions/<n>` URL form is UNVERIFIED and is accepted only once C2's fixtures confirm it exists). No revision means the latest published one (`collection.latestPublishedRevision.revisionNumber`), and the plan records the number it resolved, so Apply imports exactly what was previewed. A URL whose `<domain>` does not match the target game's `nexusmods` source id is refused (`ErrInvalidReference`), never silently imported into the wrong game.

The flag is a sibling of `--workshop-collection`, not a generic `--collection`: a bare slug and a bare Workshop id are both plausible strings, and guessing the source is the kind of cleverness that imports the wrong thing.

### Per-file pinning and locks

`updatePolicy` maps onto lmm's existing machinery with no new concepts:

| Collection `updatePolicy` | lmm `ModReference` |
|---|---|
| `exact` (and every `bundle`) | `file_ids` pinned, `version` set, `locked: true` |
| `prefer` | `file_ids` pinned, `version` set, not locked (`lmm update` may offer newer; the user decides) |
| `latest` | no `version` pin; the file is the *newest file of the same logical file* the curator picked, resolved by the manifest's `logicalFilename`/`fileExpression`, never "the mod's primary file" (the curator's file may be an optional or alternate one). The semantics of those two fields are INFERRED from Vortex; when neither resolves, the item falls back to the pinned `fileId` with a plan warning |

`locked` already refuses version changes at install, update, rollback, auto-update and converge (#294/#311), so an `exact` collection stays exact with no new enforcement code.

A lock the *collection* set differs from one the *user* set: a revision update (C8) may move the first and must not move the second. Each imported entry therefore records `from_collection: true` (a `ModReference` field, the same three-places work as `fomod:`), and only C8's sync, for such entries, may change a locked pin. A user who locks or unlocks an entry by hand clears the marker, and the entry is theirs from then on.

### Provenance on the profile

The imported profile gains one profile-level field:

```yaml
collection:
  source_id: nexusmods
  slug: rqhcxy
  revision: 3
```

It is what makes a later "update this profile to the collection's new revision" (§7, C8) possible, and what the SPA shows as the profile's origin. Additive, `omitempty`; absent on every existing profile.

---

## 2. The source seam

`source.Collection{ID, Name, URL, ItemIDs []string}` is too thin: a Nexus item needs a file id, an update policy, an optional flag and a kind. The interface grows **additively**:

```go
// internal/source/source.go
type Collection struct {
    ID, Name, URL string
    ItemIDs       []string         // Workshop: unchanged
    Revision      int              // 0 = the source has no revisions
    Items         []CollectionItem // structured; Workshop leaves it nil
    // Handle is opaque to core: whatever the source needs to fetch this
    // revision's manifest again. Nexus's download_link path is keyed by
    // the collection's and revision's NUMERIC ids (…/collections/335/
    // revisions/467/…), not by slug and revision number.
    Handle string
}

// GameCollectionResolver resolves a collection FOR A GAME: Nexus's
// collectionRevision query takes the game's domainName, and a URL naming a
// different game must be refused. CollectionResolver (Workshop) is unchanged.
type GameCollectionResolver interface {
    ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (Collection, error)
}

type CollectionItem struct {
    Kind         CollectionItemKind // mod_file | external | bundled
    ModID        string
    FileID       string
    Version      string
    Name         string
    Optional     bool
    UpdatePolicy string // exact | prefer | latest
    URL          string // external: where to get it; mod_file: the file's page
    Instructions string
    SizeBytes    int64
}

// CollectionManifestFetcher is the recipe half. Optional, type-asserted,
// like CollectionResolver: core never imports a concrete source.
type CollectionManifestFetcher interface {
    // ManifestArchiveURL returns a short-lived URL for the revision's archive.
    ManifestArchiveURL(ctx context.Context, c Collection) (string, error)
    // ParseManifest reads an EXTRACTED archive tree and returns the
    // source-neutral recipe. Pure: no network.
    ParseManifest(root fs.FS) (CollectionManifest, error)
}
```

`CollectionManifest` is source-neutral: per-item installer choices (a neutral `InstallerChoices` type, §4), exact file lists with hashes, patches, phase, `fileOverrides`, instructions; collection-level mod rules, plugin list and plugin rules; and `Unknown` — every manifest key the parser did not recognise, with the item it was found on (or none, for a top-level key). The parser holds an allowlist of keys known to be informational at every nesting level (`author`, `details`, `source.tag`, `source.adultContent`, …), so a harmless key does not block anything. An unrecognised key is a feature lmm cannot replay, so it joins the gate (§3). Vortex's `collection.json` is a moving format (the spike found the rule generator and parser disagreeing on key names); fail-closed on the unknown is what makes that safe.

**Core owns the fetch and the extraction.** The source supplies the URL and the parse; core downloads with the existing downloader and extracts with the existing extractor (the archive is 7z), into a cache entry of its own: `<cache>/<game>/collections/nexusmods-<slug>/<revision>/`. It is kept, because `bundled/` and `patches/` are needed at install time, and a re-plan of the same revision is then free. lmm has no cache-prune command, so removal is explicit: the entry goes when the last profile whose `collection` provenance names that revision is deleted or synced to another revision (C8), and purge leaves it alone (it is not deployed content).

The `collectionRevision` query gets the same `schema_contract_test.go` treatment the `ModsFilter` query has: a recorded introspection of `Collection`, `CollectionRevision`, `CollectionRevisionMod` and `ExternalResource`, so a Nexus schema change fails a test rather than a user's import.

### Account tier

Nexus serves API download links only to Premium accounts. lmm has no Premium handling today: `ValidateAPIKey` discards `is_premium`, and a free account's refusal surfaces as a generic HTTP error. Two fixes, both generic source-level classification, neither collection-specific:

1. **The refusal becomes a new `*source.AccountRestrictedError`** (`errors.Is` `source.ErrAccountRestricted`), **not** the #543 `ManualDownloadError`. The difference matters: `noteDownloadOutcome` (`manual_only.go`) records every `ErrManualDownload` into `manual_only_mods`, which is per mod and for everyone. Reusing it would mark every Nexus mod a free user touches as manual-only, and the update batch would then skip them all, for every account, until each one happened to download again. The new error renders the same remedy (the file's page, "update from file"), and nothing records it.
2. **A new optional capability `source.AccountTierReporter`** (`AccountTier(ctx) (Tier, error)`, from `is_premium`) lets a *plan* say up front "every item is a manual download" instead of discovering it 200 times at Apply. It is account-level, cached for the session, and never recorded per mod.

C1 also checks the v3 download-link API Vortex now uses (`getModFileVersionDownloadLink`, taking a `collectionId`); its REST shape is UNVERIFIED. If it is the supported path for collection-originated downloads, C2 uses it.

---

## 3. The gate

The plan derives a **feature set** from the manifest, and the acquire phase (§1) adds what only the archives can reveal. Both are compared with core's supported set:

| Feature | Raised by | Supported from |
|---|---|---|
| `list` | always | C2 |
| `external` | a `direct`/`browse`/`manual` source or an `externalResources` entry | C2 — listed with its URL and instructions, never fetched (Nexus's own collection installer asks the user to fetch these too) |
| `mod_rules` | `modRules` with `before`/`after` whose references all resolve to items | C2 — a topological sort onto the profile's mod order, stable on the collection's own order |
| `mod_rule_unresolved` | a `before`/`after` rule whose reference matches no item (rules reference mods by `IModReference`: `fileExpression`, `logicalFileName`, `md5Hint`, … — that field list is UNVERIFIED) | never, until C2's fixtures show what real rules carry |
| `optional` | any `optional: true` item | C2 — opt-in (§11 Q4) |
| `phase` | any `phase > 0` | C2 — honoured as install order (cheap); its effect on FOMOD evaluation arrives with C4 |
| `fomod` | an item with `choices.type == "fomod"` (recorded choices, replayable) | C4 |
| `fomod_unanswered` | **found at acquire:** an archive holds `fomod/ModuleConfig.xml` and the manifest recorded no choices for it (Vortex's "fresh" install mode: the curator expects each user to run the installer) | C4, answered through F3's question round trip; until then refused. **This is the witness the manifest alone misses**, and the reason acquire runs before anything is written |
| `fomod_scripted` | **found at acquire:** a `fomod/script.cs` archive | never (refused) |
| `fomod_unknown_installer` | `choices.type` other than `fomod` | never (refused) |
| `file_list` | an item with `hashes` (Vortex's "replicate" install mode) | C5 |
| `bundled` | a `bundle` source | C5 |
| `patches` | an item with `patches` | C5 (BSDIFF40 only; any other patch format stays unsupported) |
| `plugin_order` | top-level `plugins` or `pluginRules` | C6 |
| `manifest:<key>` | a manifest key the parser did not recognise and does not allowlist | never, until a story names it |
| `manifest_unavailable` | the recipe could not be fetched | never |

`modRules` of type `requires`/`recommends`/`conflicts`/`provides` are informational for lmm's purposes (the list already *is* the requirement set) and raise no feature; they are shown in the plan.

**Optional items do not block.** An optional item that needs an unsupported feature becomes `unsupported` and is skipped, with a note. Only a required item, or a collection-level feature (`plugin_order`, `mod_rules`, a top-level unknown key), blocks the whole import. Otherwise one optional texture pack with a patch would hold back an entire collection.

**The plan always renders.** A preview of an unsupported collection is useful: it says what lmm would need and which items cause it. **Apply refuses** with `*core.CollectionUnsupportedError` (`Details()`: the missing features, each with the items raising it), which `cmd/lmm` exits non-zero on and the SPA renders as the plan's blocking banner. Features found at acquire arrive the same way, from Apply rather than the plan, with nothing written. There is no `--force`. "Looks like it worked" is the failure #597 exists to prevent, and a flag that permits it would be the first thing a frustrated user reaches for.

**Whether C2 alone is useful is an empirical question.** The table says a collection with no FOMODs, no patches and no plugin rules imports from C2 on. How many real collections that covers is unknown until C2's fixture survey (§11 Q2). If bundled files or patches turn out to be common, C5 is on the critical path, and the order in §7 should change.

Each item in the plan document carries a **state**:

| State | Meaning |
|---|---|
| `installed` | already in this profile at the pinned file |
| `will_install` | lmm will download and install it |
| `manual_download` | free account: the plan links the file's page (`…/mods/<id>?tab=files&file_id=<fid>`) |
| `external` | not a Nexus file: URL + the curator's instructions |
| `bundled` | ships inside the collection archive |
| `optional_skipped` | optional and not opted in |
| `unsupported` | carries a feature not in the supported set (named) |

### The wire shape

`ImportPlan.WorkshopCollection` and `WorkshopCollection` are golden-frozen and stay **untouched**. A new neutral document rides beside them: `ImportPlan.Collection *CollectionImport` (`json:"collection,omitempty"`), carrying `source_id`, `slug`, `revision`, `name`, `url`, `features`, `unsupported`, `items[]` (state, note, url, file id, optional, installer flag), and a counts map. Neutral rather than `NexusCollection`, because Thunderstore and mod.io both have collection-shaped concepts in their design docs (`docs/plans/2026-09-10-{thunderstore-design,modio-spike}.md`), and a third bespoke type means a third copy of the CLI and SPA list rendering. New goldens; no existing golden changes.

---

## 4. FOMOD (#354)

### Placement — core, with one adapter hook

FOMOD is detected from the archive, not chosen by the game: an archive whose tree contains `fomod/ModuleConfig.xml` (case-insensitively, under at most one wrapper directory) is a FOMOD. The evaluator is a pure package, **`internal/fomod`**: parse, evaluate visibility and flags, resolve a choice set into install operations, match recorded choices. Its only game-specific input is the answer to `fileDependency` ("is `Foo.esp` active / present / missing?"), which arrives as an interface:

```go
// internal/fomod
type FileStates interface {
    State(path string) FileState // Missing | Inactive | Active
}
```

Core builds it from the profile: a path is **present** if a mod deployed in the profile provides it or the game install has it; it is **active** if present and, for a game whose adapter implements the plugin capability (§5), enabled in the profile's plugin list. Without that capability, active ≡ present. This is a documented policy, and it beats both reference managers' defaults: with no host answer, Vortex evaluates every `fileDependency` as false (even `Missing`), and MO2 considers only `.esp/.esm/.esl` files.

`gameDependency` and the script-extender dependencies (`skseDependency`, …) evaluate as **satisfied, with a plan warning**: lmm cannot read a Windows executable's version resource without significant work, and refusing the install would be worse than the warning. Recorded as an open item, not silently true.

### Semantics: Vortex's, deliberately

Collections are authored in Vortex, so a replay must reproduce Vortex's result. Where Vortex and MO2 differ, lmm follows Vortex:

- Install layering: required files at priority − 10⁹, selected options at their priority, conditional installs at + 10⁹; the higher priority wins a destination; a tie goes to the lexicographically larger source path. **Exception:** Vortex skips the offset for `<folder>` entries, which reads as a bug. lmm applies the offset to folders too and records the divergence in `internal/fomod`'s package doc. Bug-compatibility costs a quirk forever to match an edge case nobody depends on knowingly.
- Empty `destination` on a `<file>` means its base name at the root (Vortex), not "same as source" (MO2).
- Paths are case-insensitive and backslash-separated in the XML; matched against archive members after normalising both.
- Encoding is sniffed from the bytes (UTF-8 with or without a BOM, UTF-16 LE/BE with or without a BOM, Windows-1252) and the XML declaration is ignored. Both reference managers ignore it because it is usually wrong.
- Parsing is tolerant (no XSD validation, `Decoder.Strict = false`), size-capped, nesting-capped, and expands no custom entities.
- A `source` matching nothing is found during acquire (the plan cannot see the archive). On a fresh install it is a **warning** carried on the Apply result (Vortex's behaviour). On a replay it is an acquire-phase **refusal**: a collection that names a missing file is not the collection the curator tested.
- **Scripted FOMODs** (`fomod/script.cs`) and OMODs are refused (`fomod.ErrScripted`), never executed. Executing them is arbitrary code.

### Choices are profile state

What the user chose belongs on the profile entry, beside `disabled` and `category`, so export, import, duplicate, copy and collections all carry it:

```yaml
mods:
  - source_id: nexusmods
    mod_id: "12604"
    file_ids: ["37471"]
    fomod:
      config_sha256: 9f2c…        # the ModuleConfig.xml the choices were made against
      steps:
        - step: Main Files
          groups:
            - group: Texture Resolution
              choices: [2K]
```

Matching is by name, as Vortex does. Names are not unique in real FOMODs, though (Vortex's own source complains about it), so a recorded entry may carry an `index`: **lmm's own** ordinal among same-named siblings in document order, used only to disambiguate. It is *not* Vortex's `idx` (an option's position in its group, on an INFERRED sort basis, which Vortex itself never compares). A collection replay discards `idx` and matches by name alone, exactly as Vortex does.

Unlike Vortex, which silently skips a name that no longer matches, **lmm reports unmatched recorded choices**. A stale choice is an acquire-phase refusal naming the step and group, answered through the same question round trip with only the unresolved groups re-asked.

**Updates.** A mod update learns that its `ModuleConfig.xml` changed only once the new archive has been downloaded and extracted, which happens in the update Apply's acquire phase, not in its plan. So the *plan* cannot say so, and does not pretend to. If the recorded choices still match the new config, the update proceeds and the new `config_sha256` is recorded. If they do not, the update batch skips that mod with the reason "installer choices need re-answering", the same shape as the manual-only skip, and keeps the download. **Auto-update (#586) never asks a question:** a FOMOD mod whose choices still match updates unattended; one whose choices do not is skipped and announced.

The field must be added in all three places a profile field lives (`domain.ModReference`, `config.ModReferenceConfig`, the `profiles.go` mapping), carried by `internal/core/{row_copy,mod_copy,profile_duplicate}.go` and `internal/storage/config/profile_ref_rename.go`, and must not break the text-preserving `profile_edit.go` (which rewrites `disabled:` in place and refuses layouts it does not understand: a nested `fomod:` block is a layout it must skip over, with a fuzz case).

**Older lmm binaries drop unknown profile keys on rewrite** (`safeyaml` is non-strict, and there is no schema version). A user who downgrades loses their recorded choices on the first profile save. This design accepts that: self-update spreads releases quickly, and a schema-versioning scheme is a bigger change than this feature. Noted in the CHANGELOG entry. (§11 Q5.)

### The cache variant

The cache is keyed `(game, source, mod, version)`. Two profiles installing the same file with different choices produce different trees, so **an entry whose tree depends on more than the file gets a variant**. Decisions:

- **The variant is never part of the version string.** Update checks, lock comparisons and `EffectiveInstalledVersion` keep comparing source versions, untouched. The variant is a separate key component, stored in its own `installed_mods.variant` column (migration V24), with `previous_variant` beside `previous_version` so rollback restores the right tree.
- **One variant covers every input that changes the tree:** `sha256` over a canonical encoding of the FOMOD choices, the `config_sha256`, an exact file list (C5) and the set of applied patches (C5). Truncated to 12 hex digits. No inputs means no variant, and the path is byte-identical to today's.
- **The on-disk spelling is a sibling directory, `<version>@<variant>`, not `+`.** `+` is semver build metadata, which version comparison ignores, and a stray parse of the directory name must not mistake a variant for a version. F0 checks that no version string a source returns can contain the separator, and the cache refuses a version that does.
- **The key becomes a type.** `cache.ModPath(game, source, mod, version)` and its roughly 97 call sites in core take a version today. **F0** replaces the four strings with a `cache.Key` struct (variant empty everywhere) as a pure refactor, before any behaviour changes, so F2 is a feature and not a 97-site diff.
- Every consumer that lists, verifies, converges, purges or rolls back entries gets a test with a variant entry.

### Where it runs

Download → extract to staging → **FOMOD resolve** → adapter `NormalizeArchive` → cache commit (the first three in acquire, the last in commit). The FOMOD step runs in core, before the adapter, because it decides what the mod *is*; adapter layout (BepInEx's `BepInEx/plugins/` routing, d2r's) then applies to the result as it does today. It executes FOMOD's operations itself, copying from staging into a fresh tree. It does **not** go through `adapter.Layout`, whose one-destination-per-member map cannot express a FOMOD copying one source to two destinations, and whose validator correctly refuses N-to-1 mappings. Same for archive import (`Importer.layoutExtracted`), update-from-file, and C3's `profile fill`. Verify's re-layout learns the variant.

**Destinations outside the mod path.** FOMOD destinations are relative to the mod root, which lmm deploys under the game's `mod_path` (`Data/` for a Bethesda game). Content meant for the game *root*, such as SKSE's loader or an ENB, is a gap today, independent of FOMOD: the adapter design left room for it (`FileRoute`'s `Root`, additively) but has no adapter that needs it yet. The `bethesda` adapter is that adapter: P2 adds `FileRoute.Root` and the rule that routes a root-level member set (`skse64_loader.exe`, `d3d11.dll` + `enbseries/`, …, table-driven) to `install_path`. FOMOD output then flows through it like any other archive.

### Plan/Apply without callbacks

A plan cannot see an archive that has not been downloaded, and FOMOD steps depend on earlier answers (a step's visibility is a function of flags set by earlier choices). The flow uses the existing typed-error pattern (v2 Ruling 1):

1. Any install-shaped Apply runs the two phases of §1. In its acquire phase it downloads, extracts and finds a FOMOD with no choices (or stale ones). It **keeps the download** through `retained_download.go`, keyed as that file keys retained archives today (source, mod, file id; Nexus's `ModFile` exposes no md5 before the download), with that file's own TTL, which the CLI shares (serve's upload TTL is serve-only and is not the precedent). Once every item is acquired it returns **one** `*core.FomodChoicesRequiredError` with nothing written. Its `Details()` is the question document, keyed by mod: module name, visible steps, groups (name, rule: exactly-one / at-least-one / …), options (name, description, resolved type: required / recommended / optional / not-usable / could-be-usable, image member path), and the current preselection.
2. The frontend answers and re-runs Apply with `Options.Fomod[modKey] = choices`. Because visibility is stateful, the error is re-raised with the *next* visible steps until every choice set is complete. Each round trip is cheap: the archives are retained, and nothing was written, so the plan is still fresh.
3. Complete choices are written to the profile entry in the commit phase of the Apply that installs.

Frontends:

- **CLI, interactive:** walks the question document with prompts (a frontend concern, not core's), with `?` for an option's description.
- **CLI, non-interactive:** `--fomod-choices <file.json>` (the profile's `fomod:` shape, keyed by mod) or `--fomod-defaults` (Vortex's preselection: required, recommended, and the first option of an exactly-one group). Neither one supplied in a non-TTY returns the error as JSON and exits non-zero. lmm never picks defaults silently.
- **SPA:** a wizard modal driven by the error document, one step per page. It re-posts the job with the accumulated choices, and serves option images from the kept staging tree through a read-only, contained, size-capped route.
- `lmm mod fomod <mod>` (and the SPA mod page's "Reconfigure installer…") re-runs the questions for an installed mod, preselected from its recorded choices. This is how a user changes an option later; it installs into a new variant and redeploys.

---

## 5. Plugin order (#355)

### Framing

lmm already has **mod order**: a profile's `mods` list, first = lowest priority, which decides file precedence at deploy. **Plugin order** is a second, independent axis: which `.esp/.esm/.esl` files the engine loads, in what order, and which are active. One mod can ship several plugins, so plugin order cannot be derived from mod order. The profile gains its own plugin list.

### The `bethesda` adapter

A new `internal/adapter/bethesda`, selected by `adapter: bethesda` in games.yaml, and prefilled by detection for the curated Bethesda entries in `internal/source/steam/data/steam-games.yaml` (by their Steam app ids, through the existing detection prefill — the curated table already says which game a Steam app is; that is data, not a code special case). That table has no Skyrim VR or Fallout 4 VR entries today; P2 adds them. Games *already* in a user's games.yaml are not rewritten: `lmm game edit --adapter bethesda` sets it, and `lmm verify` and the SPA game page suggest it when a game's detected app id is a curated Bethesda one. It implements a new optional capability:

```go
// internal/adapter
type PluginOrderer interface {
    PluginRules(g *domain.Game) PluginSpec
}

type PluginSpec struct {
    ListKind        ListKind // Asterisk | Textfile (Tier 1); Timestamp, MorrowindINI (deferred)
    AppDataFolders  []string // "Skyrim Special Edition", then " GOG"/" EPIC" variants, in probe order
    ListFile        string   // "Plugins.txt"
    OrderFile       string   // Textfile games: "loadorder.txt"
    Extensions      []string // .esm .esp .esl
    HeaderKind      HeaderKind // TES4 24-byte header (Tier 1 games)
    MasterFlag, LightFlag, MediumFlag, BlueprintFlag uint32
    ExtImpliesMaster []string // .esm .esl
    Implicit        []string // Skyrim.esm, Update.esm, …: never written, always first
    // CCC files listing more implicit plugins, in precedence order, each
    // relative to a named root: Starfield's "My Games" copy overrides the
    // game-dir copy, even when empty.
    CCCFiles        []RootedPath // {Root: InstallPath, Path: "Skyrim.ccc"}
    ExtraPluginDirs []RootedPath // Starfield: My Games/Starfield/Data
    MaxFull, MaxLight int    // 255, 4096 (Starfield's medium-plugin limit is UNVERIFIED; P2 confirms or omits it)
}
```

Pure data, table-tested, no filesystem access. That is the adapter design's load-bearing rule, and it holds. Core does every side effect: discovers plugin files in the profile's deployed set, reads their headers with a small stdlib TES4 reader (`internal/core/plugins_header.go`, around 150 lines, fuzzed), sorts, renders (Windows-1252, `*` prefix for active), and writes.

**Tier 1:** Skyrim SE/AE, Skyrim VR, Fallout 4, Fallout 4 VR, Starfield (excluding blueprint plugins, which need mtime control: lmm never lists them and leaves them to the game), Skyrim LE. **Deferred:** Morrowind, Oblivion, Fallout 3, New Vegas. Those order plugins by file mtime, and lmm's deployed plugins are links into a cache shared between profiles, so setting an mtime means touching shared state. They need a design of their own, and a collection for one of them stays gated on `plugin_order`.

### The prefix

`Plugins.txt` lives at `<prefix>/drive_c/users/<user>/AppData/Local/<AppDataFolder>/Plugins.txt`, outside the game install, in a Wine/Proton prefix lmm does not model today (its only prefix concept is the Battle.net launch block). `domain.Game` gains an optional `prefix:` (games.yaml), and core resolves it:

1. an explicit `prefix:`, else
2. for a game with a `steam_app_id`: `<library>/steamapps/compatdata/<appid>/pfx`, probing **every** library in `libraryfolders.vdf`, not only the one holding the install (compatdata can stay behind when a game moves between libraries); if more than one exists, the most recently modified wins and the choice is reported, else
3. unresolved: no plugin list is written, and the deploy result carries a `prefix_unresolved` **finding** ("run the game once through Steam, or set `prefix:`"). The mods are deployed. It is a finding rather than a returned error, because the deploy itself succeeded.

**The user directory** is `steamuser` under Proton and umu. A plain Wine or Lutris prefix uses the Unix user name. Core lists `drive_c/users/`, ignores `Public`, and uses the one directory left; zero or several is the same unresolved finding, with `prefix_user:` as the override.

**File names.** On a case-sensitive filesystem, `Plugins.txt` and `plugins.txt` are two files to Linux but one to Wine. Core matches an existing file case-insensitively and writes to that file's name; a new file is `Plugins.txt`, which is what the games read. The same goes for the AppData folder. **Line endings:** core preserves the existing file's, and writes CRLF for a new file, because that is what the games' own launchers write. libloadorder writes LF, which is evidence that LF works too; P3 confirms it on one game by hand either way (INFERRED until then).

`lmm game detect` prefills `prefix:` when it finds the compatdata directory. Heroic, Lutris and GOG/Epic installs set it by hand (`lmm game edit --prefix`).

### The profile's plugin list

```yaml
plugins:
  order:                     # load order, first loads first; implicit plugins never listed
    - name: Unofficial Skyrim Special Edition Patch.esp
    - name: SkyUI_SE.esp
    - name: Optional Thing.esp
      enabled: false
  rules:                     # optional; LOOT-userlist-shaped, so a collection's pluginRules land verbatim
    groups:
      - { name: Late Loaders, after: [default] }
    plugins:
      - { name: Patch.esp, group: Late Loaders, after: [SkyUI_SE.esp] }
```

### The sort

Core computes the effective order whenever the deployed plugin set or the plugin list can have changed. That is a longer list than `syncMergedPak`'s hook sites: update, rollback, `mod edit`, `mod copy`, profile sync, archive import and verify `--fix` also change which plugins are deployed. P3 first looks for the single choke point, the step that every deploying flow passes through after linking, and hooks there. If no such step exists, it enumerates the flows and adds a ratchet test that fails when a flow that changes `deployed_files` lacks the hook, so a future flow cannot forget it.

1. Discover the plugins the profile deploys, and read their headers (master/light flags, `MAST` list).
2. Build a graph from hard edges (masters before non-masters; a plugin after each of its masters; implicit plugins first), from the profile's `rules` (group edges, `after`, `req`), and from collection rules once imported.
3. Topologically sort, with ties broken by the profile's existing `order`. Ties left after that go by mod order, then case-insensitive name. The result is deterministic, and a plugin the user placed stays where they put it unless a hard edge forbids it.
4. A deployed plugin absent from `order` (newly installed) goes in enabled, at the earliest position its edges allow; the placement is deterministic, so every deploy places it the same way. An `order` entry whose plugin is not deployed is left out of the file but stays in `order`, so disabling and re-enabling a mod does not lose its plugin's position.
5. A cycle is `*core.PluginOrderCycleError`, naming the edge chain. More than 255 full or 4096 light active plugins, a missing master, or a name Windows-1252 cannot encode are typed findings: errors when they would crash the game (missing master, over the limit), warnings otherwise.

The result is written to `Plugins.txt` (plus `loadorder.txt` for Skyrim LE). **It is not written back to the profile on deploy.** The effective order is a deterministic function of the profile's `order`, its `rules` and the deployed set, so the profile needs no copy of it. Writing one would add a profile write to every deploy, with the Ruling 16 pairing and the text-preserving save (#441) that comes with it. The profile's `order` changes only through explicit plugin commands and through flows that already write the profile (import, sync).

**Ownership.** For a game with plugin management on, lmm writes the whole list file, but it **reads the current file before every write**, not just the first time:

- A plugin listed in the current file that lmm does not deploy, and that exists in a plugin directory, is **external**: installed by hand, or by the game's own in-game Creations menu (Skyrim AE and Starfield rewrite `Plugins.txt` themselves). It keeps its active state and its position relative to its neighbours. lmm never deactivates a plugin it did not deploy.
- `lmm verify` reports drift only for lmm's own plugins (one moved or deactivated since lmm wrote the file). It does not report the game's own edits.

**Backup and purge.** The file the user had before lmm's first write is backed up, and purge restores it. It cannot use #350's originals store as it is: `OriginalRoot` has two roots (`mod_path`, `install_path`), and its `UnmarshalText` rejects an unknown one, so adding a `prefix` root would make an *older* binary fail to read the whole manifest, which breaks purge after a downgrade. Prefix files get their own manifest beside it (`originals-prefix.json`), which older binaries never open. (§11 Q3.)

**No LOOT masterlist in the first cut.** The hard edges plus user and collection rules are deterministic and explainable. Masterlist-driven suggestions are a separate, optional story (P6): the masterlists are CC0, so lmm can fetch and parse them freely, but the condition mini-language is roughly 1,000 lines on its own. Collections do not need it: they carry their own rules.

### Frontends

- `lmm plugins list|enable|disable|move|sort [--dry-run]` and `lmm plugins rules …`, all Plan/Apply-shaped where they mutate, with `--json` and goldens.
- SPA: a **Plugins** tab in the reorder modal, the second axis next to mod order. It shows flags (master / light), the masters each plugin needs, findings, and the effective order with rule-driven positions marked.

---

## 6. Collections, fully (#597)

With §4 and §5 in place, replaying a collection's recipe is mostly mapping:

| Manifest | lmm |
|---|---|
| `mods[].choices` (`type: fomod`) | the item's `ModReference.fomod`, matched by name. The archive is verified against `source.md5` first, because choices made against a different archive are meaningless |
| `phase` | install in phase order. `fileDependency` evaluation during phase N sees phases < N as present |
| `mods[].hashes` (replicate mode) | a `file_list` install: extract only the listed paths, then verify each md5. The list is recorded on the profile entry (`files:`, a new `ModReference` field: the same three-places, carriers and `profile_edit.go` fuzz work as `fomod:`), so it replays on another machine, and it is a variant input (§4) |
| `bundled/` + `bundle` source | a synthetic per-collection source (`lmm-collection`, the `lmm-merged` precedent): the bundled tree is ingested from the collection's cache entry like an archive import, and the profile entry references it |
| `patches` | applied during acquire, after the mod's tree is built and before cache commit: BSDIFF40 via `compress/bzip2`, about 150 lines in core, each applied only if the target's CRC32 matches the recorded baseline. A mismatch is a refusal, **a deliberate divergence from Vortex**, which only warns and installs the unpatched file: a collection whose patch did not apply is the "looks right, plays wrong" outcome again. The patched tree is a cache variant (§4) |
| `modRules` `before`/`after` | edges in a stable topological sort of the imported profile's mod order |
| `fileOverrides` | the listed paths must be won by that mod: an ordering constraint fed into the same sort. If it cannot be satisfied, that is a plan warning naming the two mods |
| `plugins` + `pluginRules` | the profile's `plugins.order` (the curator's enabled list, in order) and `plugins.rules` (verbatim). The rule parser accepts both key spellings the spike found (`after` / `req` / `inc` and `requires` / `incompatible`), with a golden for each |
| `info.installInstructions`, per-mod `instructions` | shown in the plan (CLI: printed before confirmation; SPA: a callout) |
| `collectionConfig.recommendNewProfile` | ignored. An import always creates a profile |

### Free accounts

A free account cannot get API download links, and automating the website's "slow download" is not something lmm will do, whatever the ToS says (§11 Q1 notes the ToS wording is unverified). The import still works:

1. The plan marks every Nexus item `manual_download`, with the file page URL.
2. Apply saves the profile (pins, choices, plugin rules), with every item pending.
3. **`lmm profile fill <profile> <dir>`** matches the archives in a directory, typically `~/Downloads`, against the pending pins by **md5** (the manifest records each file's md5 and size), then installs the matches with their recorded choices, as if downloaded. The SPA equivalent is a drop zone on the collection's import result. This makes a 200-mod collection a matter of clicking "slow download" 200 times and then running one command, with no automation of Nexus's site.

### Revision updates

`lmm profile sync --collection` (the existing `profile sync` flow, with a collection-provenance branch) resolves the latest revision, diffs it against the profile, and plans the result: added items, removed items, changed pins, changed choices and changed rules. A mod the old revision brought in and the new one does not is offered for removal, not removed silently (Vortex's "obsolete members" review). Entries marked `from_collection` follow the new revision's pins, locked or not, through an explicit sync-only bypass of the lock check (the lock is the collection's, §1). An entry the user locked or edited by hand is no longer `from_collection`; if the new revision changes it, that is a sync *conflict* shown in the plan, and the user's lock wins unless they choose otherwise. Items the user added by hand are untouched.

---

## 7. Implementation plan

Story issues, each a `feat/…` branch off `develop`, each with its CHANGELOG entry under `[Unreleased]` and its guide update. Dependency arrows are hard: a story does not start before its parents merge.

### Foundations (no parents; can start now, in parallel)

- **C1 — Nexus account tier.** `AccountTierReporter` from `is_premium`; a free-account download refusal becomes `*source.AccountRestrictedError` (never recorded as manual-only, §2); check the v3 download-link API. Generic: it fixes today's opaque error for every free user, collections or not. *Needs a free-account response recorded for the test (§11 Q1).*
- **A1 — Two-phase install-shaped Apply.** The acquire/decide/commit engine of §1, as a core refactor: `retained_download.go` gains its second refusal reason; acquire refusals are collected before any write. Ordinary import, install and the update batch keep today's per-item failure counting for *unanswerable* failures (a download error); only *answerable* refusals (questions, from F2 on) are collected up front. Collection apply (C2) treats every acquire refusal as blocking. No user-visible change on its own, and that is the test: every existing flow's goldens and end-state tests pass unchanged.
- **F0 — `cache.Key`.** Replace `(game, source, mod, version)` strings with a key type at every call site (variant always empty). Pure refactor, zero behaviour change; the separator check of §4.
- **F1 — `internal/fomod`.** Parse (encodings, tolerance, caps), visibility/flags, type resolution, Vortex-semantics resolution (with the folder-offset divergence), choice matching with unmatched reporting, scripted refusal. Pure, table-tested against a fixture corpus of real ModuleConfig.xml files (license-checked) and hand-written edge cases; fuzzed. Joins the doc-comment ratchet.
- **P1 — Game prefix.** `prefix:` / `prefix_user:` on games.yaml and `domain.Game`, Steam compatdata derivation across all libraries, the user-directory rule, detect prefill, `game edit --prefix`, the SPA game editor field.
- **P2 — TES4 header reader + `bethesda` adapter.** The `PluginOrderer` capability and its rule tables for the Tier 1 games; `FileRoute.Root` and the game-root routing table (SKSE, ENB), once P2 confirms no existing route covers it; Skyrim VR / Fallout 4 VR detection entries; the detection prefill and the "suggest `adapter: bethesda`" finding; the header reader (fuzzed, fixtures from `Ortham/testing-plugins` if its license allows, else synthesised).

### Collections

- **C2 — Collection import, list + gate (Level 1).** `GameCollectionResolver`, `CollectionItem`, `Handle` and `CollectionManifestFetcher` on the source seam; the `collectionRevision` query with its schema-contract test; fetching, caching and extracting the recipe archive; `ParseManifest` with its allowlist and `Unknown`; feature derivation and the gate (manifest and acquire-time, including `fomod_unanswered` and `fomod_scripted` detection, which needs only a file-presence check and not F1); `ApplyCollectionImport` on A1; the `CollectionImport` document and goldens; the `collection:` provenance field and `from_collection`; the mod-rule sort; phase as install order; optional opt-in; `CollectionUnsupportedError`; `--nexus-collection` and the SPA profile-import input. Supported set: `list`, `external`, `mod_rules`, `optional`, `phase`. **Starts with the fixture survey of §11 Q2.** ← C1, A1.
- **C3 — `lmm profile fill`.** md5 matching of downloaded archives against pending pins (CLI + SPA drop zone), installing through A1. ← C2.
- **C4 — Collection FOMOD replay.** `choices` to `ModReference.fomod`, the md5 pre-check, phase-aware `FileStates`; adds `fomod`. `fomod_unanswered` becomes supported once both C4 and F3 have merged. ← C2, F2.
- **C5 — Exact file lists, bundled files, binary patches.** The `files:` field; the `lmm-collection` synthetic source; the BSDIFF40 applier; adds `file_list`, `bundled`, `patches`. ← C2, F2 (cache variants).
- **C6 — Collection plugin order.** `plugins`/`pluginRules` to the profile's plugin list, both rule-key spellings; adds `plugin_order`. ← C2, P3.
- **C7 — README/CLAUDE.md: Collections is no longer a non-goal.** Docs only, once C4, C6, F3 and P4 have merged (the point at which a typical Skyrim collection imports, if §11 Q2's survey agrees that it is).
- **C8 — Revision updates** (`profile sync` for collection-provenance profiles, the `from_collection` lock bypass, obsolete-member review, collection cache-entry removal). ← C2.

### FOMOD

- **F2 — Core FOMOD install step.** `ModReference.fomod` (three places + carriers + `profile_edit.go` fuzz); the `installed_mods.variant`/`previous_variant` migration and every cache consumer; the resolve step in download / archive import / update-from-file / `profile fill` / verify; `FileStates` from the profile; `FomodChoicesRequiredError` through A1; the update-batch skip and auto-update policy; goldens. ← F0, F1, A1.
- **F3 — FOMOD frontends.** CLI interactive walk, `--fomod-choices`, `--fomod-defaults`; the SPA wizard and its image route; `lmm mod fomod` and "Reconfigure installer…"; guide page `docs/guide/fomod.md`; README's FOMOD line. ← F2.

### Plugin order

- **P3 — Profile plugin list + sort + write.** `plugins:` on the profile (three places + carriers), the sort, the hook (choke point or ratchet), `Plugins.txt`/`loadorder.txt` writing with case and line-ending rules, external-plugin preservation, `originals-prefix.json` and purge restore, verify drift, typed findings. ← P1, P2.
- **P4 — Plugin frontends.** `lmm plugins …`, the SPA Plugins tab; guide page `docs/guide/plugins.md`; README's #355 lines. ← P3.
- **P5 — FOMOD `fileDependency` sees plugin state.** Wire the profile's plugin list into `FileStates` for adapters with `PluginOrderer`. ← F2, P3.
- **P6 — LOOT masterlist suggestions** (optional, demand-gated). Fetch and cache CC0 masterlists pinned to a syntax branch; `group`/`after`/`req` edges only, with conditions evaluated fail-closed (an unknown condition means not applicable, with a warning). Never automatic: `lmm plugins sort --suggest`. ← P4.

### Order and parallelism

```text
C1 ─────┐
A1 ─────┼──► C2 ──┬──► C3
        │         ├──► C8
        │         ├──────────────► C4 ◄── F2
        │         ├──────────────► C5 ◄── F2
        │         └──────────────► C6 ◄── P3
F0 ─┐   │
F1 ─┼───┴──► F2 ──► F3
A1 ─┘         └──────► P5 ◄── P3
P1 ─┐
P2 ─┴──► P3 ──► P4 ──► P6
C7 ◄── C4, C6, F3, P4
```

C1, A1, F0, F1, P1 and P2 have no parents and can run as six parallel worktrees. A1 and F0 are the riskiest, because they touch every install path, and they are worth landing early and alone. C2 is the first user-visible milestone (collections that need no installer), F3 the second (FOMOD for every game, collections or not), and C4 + C6 the third (typical Bethesda collections).

### Testing notes that bind every story

- Fixtures: no network in tests. The `collectionRevision` response, the manifest archive, and `collection.json` samples are recorded (with the curator's content reduced to names and ids) under `testdata/`. Real `collection.json` samples are a prerequisite for C2 (§11 Q2).
- Every new `--json` type gets a golden; `ImportPlan`'s existing goldens must not change byte-for-byte. A golden diff in C2 means the additive field was not additive.
- Every new `Details()` implementer (`CollectionUnsupportedError`, `FomodChoicesRequiredError`, `PluginOrderCycleError`, and `AccountRestrictedError` if it carries one) needs its entry in `details_coverage_test.go`. `prefix_unresolved` is a deploy finding, not an error.
- E2E (`TestE2E*`): the collection import plan view, the FOMOD wizard, the Plugins tab.
- Cancellation (Ruling 16): a FOMOD install and a plugin-list write each pair a DB write with a profile write; both must finish or neither.

---

## 8. Boundary rules

- `internal/fomod` imports only the standard library. `internal/core` imports it; adapters do not (they supply `FileStates`' plugin half through core, not by importing the evaluator).
- `internal/adapter/bethesda` imports `internal/adapter` and `internal/domain` only; the adapter boundary test covers it.
- `internal/core` never imports `internal/source/nexusmods`; everything goes through `GameCollectionResolver`, `CollectionManifestFetcher` and `AccountTierReporter`.
- `internal/serve` keeps importing only `app`/`core`/`domain`; it needs `core.IsBadCollectionRef`-style classifiers for the new errors.

---

## 9. What this does not do

- No automation of Nexus's website download flow for free accounts (C3 is the alternative).
- No C# script FOMODs, no OMODs, no BAIN installers.
- No timestamp-ordered games' plugin order (Morrowind, Oblivion, FO3, NV) in this design.
- No automatic LOOT sort. P6 offers suggestions only.
- No collection *authoring* or publishing. Import only.
- No execution of anything from a collection: `tools` entries, if a manifest has them, raise `manifest:tools` and are unsupported.

---

## 10. Docs that change, and when

- `README.md` "What lmm does not have", "When to pick something else", "Non-goals for 2.0": FOMOD with F3, plugins with P4, Collections with C7. Each story edits only its own line, so the README is never ahead of `develop`.
- `CLAUDE.md` "Project Overview" non-goals sentence, with C7.
- `docs/guide/profiles.md` (collection import), new `docs/guide/fomod.md` and `docs/guide/plugins.md`, `docs/guide/manual-mods.md` (`profile fill`), `docs/guide/add-a-game.md` (`prefix:`, `adapter: bethesda`), `docs/adapters.md` (`PluginOrderer`), `docs/configuration.md` (profile `fomod:` / `plugins:` / `collection:`, games.yaml `prefix:`).

---

## 11. Open questions

These are the genuine forks; everything above is decided unless a reviewer reopens it.

1. **Free accounts and the recipe — needs a real key.** Can a free account fetch the collection archive (`/v2/collections/{id}/revisions/{id}/download_link`)? Vortex's comments suggest yes; nobody has tested it. If no, free users get list-only imports (`manifest_unavailable`, Apply refused) and C3's value drops sharply. **Ask:** one `curl` with a free-account key and one with a Premium key, recorded into C1's and C2's fixtures. The Nexus ToS wording on automating free downloads was not found either; lmm's no-automation stance stands regardless.
2. **Real `collection.json` samples.** The spike read Vortex's types but no real manifests, and found the plugin-rule generator and parser disagreeing on key names. C2 should start by downloading three or four real ones (one Skyrim SE with FOMOD and plugin rules, one non-Bethesda, one with bundled files or patches) as fixtures. That needs a key, so it goes with Q1.
3. **Plugins.txt ownership.** Decided above: lmm writes the whole file from the profile, re-reads it before each write, and keeps plugins it did not deploy (hand-installed, or added by the game's own Creations menu) in place. The alternative is a stricter "lmm's file is the whole truth" mode that deactivates anything the profile does not name. That fits lmm's reproducibility promise better, but it would silently switch off a Creation bought in-game. Reviewer's call.
4. **Optional collection mods default.** Decided: off, opt-in (`--with-optional`, or per item in the SPA plan). Vortex asks the user. The alternative is on by default, which matches what the curator shipped as the "full" experience.
5. **Old binaries dropping new profile keys.** Accepted above (self-update makes downgrades rare). The alternative is a `requires_lmm: ">=2.N"` profile key that older binaries refuse to rewrite. That only protects binaries released *after* it, so it is cheap insurance for the next feature rather than this one.
6. **FOMOD placement in core** reverses #354's text ("lives in a bethesda adapter"). Decided above with reasons (§4); recorded here because it changes an issue's stated plan.
7. **`gameDependency` and script-extender versions** evaluate as satisfied, with a warning (§4). Reading a Windows executable's version resource would answer them properly (`debug/pe` parses the headers; walking the resource section is the extra work). Deferred, not rejected: it becomes worth doing if real FOMODs turn out to gate on it often.
