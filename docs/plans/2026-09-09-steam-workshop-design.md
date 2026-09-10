# Steam Workshop Support — Design (#269)

**Date:** 2026-09-09 · **Status:** For review · **Issue:** #269 (spike #268) · **Target:** v2.0.0

Authority: issue #269 plus the owner's 2026-09-09 ruling comment (Tiers 1+2+3 ship;
Tier 3 = legacy `file_url` GET + `steamcmd +login anonymous` only, **no account-session
fallback**; subscriptions, shared keys, rehosting and password storage stay NO-GO), over
the live-verified facts in `docs/plans/2026-08-27-steam-workshop-spike-findings.md`
(branch `docs/steam-workshop-spike-findings`). Every empirical claim below is the spike's;
nothing here was re-verified against the network.

Each numbered section is a decision, not a menu. Section 9 holds the only genuine forks.

---

## 1. Source identity and game mapping

**Source id `steamworkshop`**, `Name() "Steam Workshop"`, `TypeLabel() "built-in"`, in a new
package `internal/source/steamworkshop`. Registered by one line in
`internal/app/sources.go`'s `builtinSourceFactories`, exactly like `icarus`. Key attachment,
`lmm source list`, the SPA's `SourcesMapEditor` and `lmm auth` all work unchanged as a
result — every one of them enumerates the registry.

**A game maps to it through `games.yaml`'s existing `sources:` block, and the per-source
game id IS the Steam app id** (decimal string): `sources: {steamworkshop: "1133870"}`. This
lands in `domain.Game.SourceIDs`, which `Updater.CheckUpdates`, `Service.SearchMods` and
`Service.GetMod` already translate through. No new config key, no new games.yaml schema.

**Unit 9 detection prefills it.** `steam.DetectGames` already walks every library and knows
each app's id and library root. It gains one check per candidate: if
`<library>/steamapps/workshop/appworkshop_<appid>.acf` exists **and** its
`WorkshopItemsInstalled` block is non-empty, add `steamworkshop: <appid>` to the candidate's
`Sources` map and stamp `DetectedGame.WorkshopItems` (new, `json:"workshop_items,omitzero"`)
with the item count.

- For a curated entry with an explicit `sources:` map, the workshop entry is **added** to it.
- For a curated entry with nil `Sources` (the "derive `{nexusmods: NexusID}`" case), the
  derivation happens first, then the workshop entry is added.
- For an **unknown** game (`Known: false`, empty `ModPath`), the workshop entry is the only
  source it gets — which is exactly right: lmm cannot deploy to it, but it can track it.
- An **empty-stub ACF** (`WorkshopItemsInstalled {}` — the spike's documented shape for a
  workshop-capable game with nothing subscribed) parses cleanly and does **not** prefill.
  Nothing is lost: the user adds it later with `lmm game edit` or the SPA sources map.
- `lmm game detect --no-workshop` suppresses the prefill for a user who does not want it.

`core.GameSpecFromDetected` already copies `Sources` into `GameSpec`, so
`lmm game add --from-detected`, `POST /api/v1/games` with `from_steam_app_id`, and the SPA's
"Pick an installed game…" inherit the prefill with no change of their own.

**Custom-source-editor implications: none.** `steamworkshop` is built-in; it can never be
shadowed by a `sources/*.yaml` definition (built-ins register first and win the collision),
and it is not expressible as a `directory`/`manifest`/`api` definition.

**`data/steam-games.yaml` is not bulk-edited.** Detection is the mechanism; a curated entry
*may* declare `steamworkshop` explicitly, but doing so for dozens of games would encode a
claim (this game uses Workshop) that the ACF on disk answers better.

---

## 2. The domain question — tracking without deploying

A Tier-1 item lives in `steamapps/workshop/content/<appid>/<fileid>/`, is loaded by the game
directly from there, and is owned by the Steam client. lmm tracks it, reports on it and
checks it for updates; lmm never deploys, links, downloads, moves or deletes its files.

### The representation

**Two additive fields on `domain.InstalledMod`. Not a LinkMethod value, not a DeployMode.**

```go
// External marks a mod lmm TRACKS but never deploys: another agent (today,
// the Steam client for a Workshop item) owns its files where they sit, and
// the game loads them from there. lmm never links, copies, downloads,
// moves or removes an external mod's content.
External bool `json:"external,omitzero"`
// ExternalPath is the absolute directory that agent owns. Only meaningful
// when External; recorded at adopt time so no flow needs to re-scan Steam's
// libraries just to report where a mod lives.
ExternalPath string `json:"external_path,omitempty"`
```

Rejected alternatives, and why:

- **A fourth `LinkMethod` (`external`).** `LinkMethod` is also a *user-selectable config
  value* (`ValidLinkMethods`, `ParseLinkMethod`, `game.link_method`, `--link-method`). Adding
  a value makes `link_method: external` syntactically legal in games.yaml and profiles, which
  is meaningless, and forces a rejection branch into every parse site.
- **A fourth `DeployMode` (`tracked`).** `DeployMode` is per-GAME ("how downloaded archives
  are handled"). External-ness is per-MOD: a Skyrim install has Nexus mods and Workshop items
  side by side in one profile.
- **A single enum `ModManagement{managed,external}`.** Strictly more vocabulary than the one
  bit in play, and no second value is in sight. The `ManualDownload bool` precedent on this
  same struct is the house style for exactly this.

`ExternalPath` is a second field rather than derived because deriving it means re-running the
Steam library scan inside `lmm mod show`, `verify` and every SPA mod-page render.

`Enabled` keeps its meaning (user intent). **`Deployed` is set `true` at adopt and no flow
ever mutates it on an external mod** — the field means "its files are where the game reads
them", which for a Workshop item is true the moment Steam finished downloading it. Any other
choice makes `lmm status` report a permanent, un-actionable "not deployed" backlog.

### Every flow, decided

| Flow | Behaviour on an external mod |
|---|---|
| `lmm status` / `GameStatus` | Counted in `installed_mod_count` / `enabled_mod_count`. New `GameStatus.ExternalCount int json:"external_count,omitzero"` so a readout can say "30 installed (30 tracked from Steam)". |
| `lmm list` / `ModList` | Listed, with an `EXTERNAL` marker in the CLI table. `ModListing` embeds `InstalledMod`, so the two new keys ride along free. |
| `deploy` (`PlanDeploy`/`ApplyDeploy`/`DeployProfile`) | Listed in `DeployPlan.Mods` with a **new third `DeployModClass` value `external`**; `ApplyDeploy` emits one `StepEvent` per external mod and does nothing. `deployedPathsFor` returns nil for them, so they contribute no deployed paths. A targeted `lmm deploy --mod <id>` on one returns `ErrExternalMod`. `classifyCompileDeployMods` gains an unconditional external pass ahead of its compile-only classification. |
| `deploy --purge` / `purge` / `PurgeProfile` | Excluded from `purgeSpec`'s mod set. `PurgePlan` gains `External []string json:"external,omitempty"` (display names) so the preview says what it will not touch. |
| `convergeDeployedFiles` | Unaffected: converge reconciles files under `mod_path`, and an external mod owns none. |
| `verify` | Local tier: `ExternalPath` must exist and be a non-empty directory; absent → a finding worded "Steam no longer has this item on disk — it may have been unsubscribed". Checksum tier: skipped, by adding `mod.External` to `verify.go`'s existing `SourceLocal \|\| ManualDownload \|\| len(FileIDs)==0` skip. `verify --fix` offers **no** repair (redownload and checksum-backfill both presuppose an lmm-owned cache entry) and reports the finding. |
| `uninstall` | Removes lmm's tracking only — DB row and profile ref — never files, never a cache entry (there is none). `UninstallPlan` gains `External bool json:"external,omitzero"` so the confirmation reads "this only stops lmm tracking it; the item stays subscribed in Steam — unsubscribe in the Steam client to remove it". `--keep-cache` is a no-op. |
| `profile switch` / `apply` / `sync` | Both halves (purge, deploy) skip external mods, so switching profiles **does not change what Steam has on disk**. When the outgoing and incoming profiles differ in external mods, one advisory note: "N Steam Workshop items stay active regardless of profile — manage subscriptions in the Steam client." A Workshop item is game-global; lmm profiles are not, and pretending otherwise would require unsubscribing on the user's behalf (NO-GO). Documented limitation. |
| `conflicts` | External mods never appear: conflict detection compares deployed paths under `mod_path`, and they have none. lmm cannot see inside a game's own Workshop loader. Documented limitation. |
| `mod enable` / `mod disable` | **Refused** with `ErrExternalMod`: "lmm cannot disable a Steam Workshop item — unsubscribe it in Steam, or use the game's own mod menu." A bookkeeping-only "disabled" flag on a mod the game still loads is a lie. |
| `update` (check) | Checked — that is the point of Tier 1. See §3. |
| `update` (apply) / `update rollback` / `mod edit` (relink) | Refused with `ErrExternalMod`. |
| `install` | An item downloaded by Tier 3 is an ordinary managed mod (`External: false`) from that moment. External and managed are mutually exclusive per install; see Q2. |
| `profile reorder` | External mods are omitted from the reorder list (no deployable files ⇒ no load order). See Q1. |
| SPA library rows (`modrows.js`) | A `Steam` badge (existing tokens only — the colour ratchets forbid a new literal). |
| SPA mod panel / full mod page | A "Managed by Steam" block naming `ExternalPath`; deploy / disable / rollback / relink actions hidden; uninstall reworded. |
| SPA Mission Control | The deploy card's "N to deploy" excludes external mods; a separate "N tracked by Steam" line. |

### Golden blast radius

Both new fields are `omitzero`/`omitempty`, so **no existing golden changes a byte** — every
current fixture leaves them unset. The re-record check must still be run against the goldens
that embed `InstalledMod`, to prove that:

`internal/core/testdata/json/`: `install_plan`, `local_scan`, `mod_files_report`,
`mod_listing`, `mod_listing_not_applicable`, `mod_setting_result`, `profile_apply_install`,
`profile_apply_plan`, `purge_plan`, `relink_plan`, `relink_result`, `rollback_plan`,
`switch_plan`, `uninstall_plan`, `update_batch_plan`, `update_plan`.
`internal/domain/testdata/json/`: `installed_mod`, `update`.
`cmd/lmm/testdata/json_golden/`: `list_populated`, `mod_convert_result`, `mod_edit_result`,
`mod_files_report`, `mod_lock_result`, `mod_set_update_result`, `mod_unlock_result`,
`profile_apply_dry_run`, `profile_switch_dry_run`, `purge_dry_run{,_compile}`,
`uninstall_dry_run{,_compile}`, `update_bulk`.

**New** goldens (added, not changed): `installed_mod_external`, `mod_listing_external`,
`game_status_external`, `deploy_plan_external`, `uninstall_plan_external`,
`purge_plan_external`, `update_plan_external`, `workshop_scan`, `workshop_adopt_plan`,
`workshop_adopt_entry`, `workshop_adopt_result`, `external_mod_error`,
`workshop_fetch_error`; CLI-side `mod_show_external`, `import_workshop_dry_run`,
`import_workshop_result`, `list_external`.

---

## 3. Tier 1 — adopt, metadata, update-check

### Adopt: a sibling plan/apply, not the existing `ScanLocal`

**Decision: new `internal/core/workshop_adopt.go` with `PlanWorkshopAdopt` /
`ApplyWorkshopAdopt`**, alongside (not inside) `ScanLocal`/`PlanAdopt`/`ApplyAdopt`.

Why a sibling: the existing adopt flow is filename-based end to end. It scans `game.ModPath`,
matches by **name search** across every searchable source (`matchScannedMod`), copies into
the cache for copy-mode games, and stamps `ManualDownload: true`. A workshop adopt scans a
different tree, matches by **exact published-file id** (no search at all), must never write a
cache entry, and produces `External` rows. Threading four conditionals through
`scanLocal` → `matchUntracked` → `adoptScannedMod` would fork the flow's shape inside itself.

What it reuses, so this is a sibling and not a second engine: `beginOp`, `installedSnapshot`
+ `checkPlanFresh` (Ruling 5 staleness), `saveInstalledMod`, the profile-ref upsert, the
`Event`/`Scope`/`StepEvent` vocabulary, and the `Op`/`DeployPhase` enums (new phases only).

Types (all new, all additive to the wire): `WorkshopScan{GameID, AppID, Libraries []string,
Items []WorkshopItem, Tracked, Untracked}`, `WorkshopAdoptPlan{GameID, Profile, Scan,
Entries []WorkshopAdoptEntry, snapshot}`, `WorkshopAdoptEntry{FileID, Path, SizeOnDisk,
Manifest, TimeUpdated, Mod *domain.Mod, Unavailable bool, Note string}`,
`WorkshopAdoptResult{Adopted, Skipped, Failed int, Warnings []string}`.

CLI: **`lmm import --workshop`** — a third mode on the existing command, mutually exclusive
with an archive argument and with `--skip-match`. Same user intent ("bring what is already on
disk under lmm"), no new top-level command.
Serve: one new job kind `workshop_adopt` (`internal/serve/kind_workshop_adopt.go`, registered
in `plankinds.go`), driven by `POST /api/v1/plans/workshop_adopt` then `POST /api/v1/jobs` —
the same entry point every other mutation uses. No new read endpoints.

### Keyless metadata

`ISteamRemoteStorage/GetPublishedFileDetails/v1`, form-POST, no key, batched
(`itemcount` + `publishedfileids[i]`, **capped at 100 ids per request**).

Transport: the package builds on `internal/source/httpclient` — the shared timeouts, size
caps and redirect policy are the reason it exists. `httpclient` gains **two additive things**
and no behaviour change for existing sources:

- `Options.AuthQueryParam string`, an alternative to `AuthHeader` (Steam takes `key=` as a
  query parameter, not a header). `New`'s required-field panic relaxes to "AuthHeader **or**
  AuthQueryParam".
- `DoForm(ctx, path string, form url.Values, result any) error` — POST,
  `application/x-www-form-urlencoded`, otherwise identical to `DoJSON` (same 401 →
  `domain.ErrAuthRequired` mapping, same `errorBodyLimit`, same decode).

Without these the new package would re-implement precisely what `httpclient` centralises.

**Cache — mandatory, per the spike.** Location `<Paths.CacheDir>/_steamworkshop/meta/<fileid>.json`.
The `_` prefix is unreachable as a game slug (`DeriveGameID` never emits one), so it cannot
collide with the game-scoped mod cache that shares this root.
TTL **6 hours** for a successful item. A **`result != 1`** item (the spike's `result: 9`
dead/delisted case) is cached as a negative for **1 hour** so a delisted item cannot hammer
the API on every check. `lmm update --refresh` and the SPA's refresh action bypass both.
**Backoff:** on 429 (honouring `Retry-After`) or 5xx, exponential with jitter, 3 attempts;
after 3 consecutive failures a process-lifetime circuit breaker suspends further calls for 5
minutes and every affected item reports "Steam metadata unavailable" rather than "up to date".
Per-item `result != 1` is handled **per item, never per response**: that item is marked
`Unavailable`, gets a note, and is *never* reported as having an update.

### Update-check

`steamworkshop` implements `ModSource.CheckUpdates` (and
`source.UpdateProgressReporter`, so the existing per-mod progress line works).

- **Version identity for a workshop item is the ACF `manifest` value** (the content id) — the
  only stable, comparable identity such an item has. `InstalledMod.Version` holds it;
  `Update.NewVersion` holds the API's `hcontent_file`.
- **Primary signal:** API `hcontent_file != ACF manifest` ⇒ update available.
- **Secondary:** when `hcontent_file` is absent or zero, API `time_updated > ACF timeupdated`.
- `domain.CompareVersions` is never consulted for this source (a 19-digit content id is not a
  dotted version); the source computes the answer itself, as `icarus` already does for its own
  identity scheme.

**What "update available" means when Steam applies it itself.** For an external mod the
update is a *notification*, never an action lmm can take:

- `UpdatePolicy`: `notify` (the default, forced at adopt) and `pinned` are allowed;
  `lmm mod set-update auto` is **refused** with `ErrExternalMod` — lmm cannot apply it.
- Bulk `lmm update`: the item appears in the check output, and `UpdateCheckReport` gains
  `External int json:"external,omitzero"` so the summary line can read "N Steam Workshop
  items have updates — Steam applies these itself the next time you launch the game (or use
  Steam's *Verify integrity of game files*)."
- Targeted `lmm update <mod>`: `PlanUpdate` gains `External bool json:"external,omitzero"` and
  fills the **existing** `UpdatePlan.Refusal` field with that same sentence; `ApplyUpdate`
  returns `ErrExternalMod`. No new refusal-rendering machinery.

### `lmm mod show` / the SPA mod page

`ModDetail.Mod` comes from `steamworkshop.GetMod` (one `GetPublishedFileDetails` call,
cached): `Name` ← `title`, `Summary`/`Description` ← `description` (raw source markup, per
the accepted #86 precedent — the SPA must keep treating `description` as untrusted text),
`UpdatedAt` ← `time_updated`, `PictureURL` ← `preview_url`, `Category` ← the first tag,
`SourceURL` ← `https://steamcommunity.com/sharedfiles/filedetails/?id=<fileid>`.

**`Author` is the raw `creator` steamid64.** Resolving it to a display name needs
`ISteamUser/GetPlayerSummaries`, i.e. a key — so Tier 1 would show a number and Tier 2 a name
for the same mod. The `SourceURL` is one click from the real author name; that is enough, and
Tier 2 deliberately does **not** upgrade it.

`core.InstalledDetail` gains `External bool json:"external,omitzero"` and
`ExternalPath string json:"external_path,omitempty"`, mirroring the row, so `lmm mod show`
and the mod page render "Managed by: Steam Workshop (`/…/workshop/content/1133870/3617086610`)".

---

## 4. Tier 2 — search and collections, bring-your-own key

**`lmm auth login steamworkshop`** uses the existing store verbatim
(`core.SaveSourceToken` → `db.SaveToken`, `POST /api/v1/auth/{source}` in serve). The source
implements:

- `EnvKeyProvider.EnvKey() → "STEAM_WEB_API_KEY"` (the name every other Steam tool uses and
  the one users already have exported), rather than the derived
  `LMM_STEAMWORKSHOP_API_KEY`. `app.ResolveAPIKey` handles either without change.
- `AuthInstructionsProvider`: "Get a free key at https://steamcommunity.com/dev/apikey. The
  key is personal and confidential — never share it, and never paste someone else's."
- **`KeyValidator.ValidateKey`**: the *keyed* call that proves a key works —
  `IPublishedFileService/QueryFiles/v1?key=<k>&query_type=1&numperpage=1&appid=480`
  (Spacewar, present for every account). A valid key returns 200 even with zero results; an
  invalid one returns the spike's live-verified **403 "Please verify your key= parameter"**,
  mapped to "invalid Steam Web API key". A keyless call would validate nothing.
- `Capabilities()` reports `Search: true` unconditionally — the capability exists; an unkeyed
  `Search` returns `domain.ErrAuthRequired` via `httpclient`'s existing mapping, and
  `lmm source list`'s auth column carries the rest of the story.
  Full set: `{Search: true, Dependencies: false, Updates: true, Auth: true, Versions: false}`.

**Search** maps `QueryFiles` onto `SearchQuery`/`SearchResult`:

| `SearchQuery` | Steam |
|---|---|
| `GameID` (already the app id via `SourceIDs`) | `appid` |
| `Query` | `search_text`; `query_type=12` (RankedByTextSearch) when non-empty, `query_type=3` (RankedByTrend) when empty |
| `Page` (0-based) | `page` (1-based) — `page = Page + 1` |
| `PageSize` | `numperpage`, capped at 100, default 20 |
| `Tags` | `requiredtags[i]` |
| `Category` | **one additional required tag.** Workshop has no category concept distinct from tags; `Category`'s contract already says "source-specific: ID or name". |

`SearchResult.TotalCount` ← `response.total`; `Page`/`PageSize` echo the request. Results map
to `domain.Mod` exactly as `GetMod` does.

**Collections → a `lmm profile import` input.** `GetCollectionDetails` is keyless and returns
a collection's child published-file ids. A collection **is** a mod list, which is what a
profile is; surfacing it as a search facet would produce a row the user cannot install as a
unit. So:

- `lmm profile import --workshop-collection <id|url>` builds a `domain.ExportedProfile` whose
  `Mods` are `{source_id: steamworkshop, mod_id: <fileid>}` refs and hands it to the existing
  `PlanImport`/`ApplyImport`. Items already subscribed adopt via Tier 1; the rest download via
  Tier 3; anything neither is reported per-item as "subscribe in Steam and re-run
  `lmm import --workshop`".
- SPA: the profiles modal's import gains a "Steam Workshop collection" input; a collection URL
  pasted into the search box is recognised and offered as an import.
- Keyless, so it *works* without a key — but it ships in the Tier-2 unit, where the collection
  client code lives.

**Rate/quota.** 100k calls/day, per the Web API ToU. The same cache + backoff + circuit
breaker as Tier 1, plus search responses cached **5 minutes** keyed on the full normalised
query. No local quota counter: lmm cannot see the user's other tools' usage, so a counter
would be a confident wrong number.

---

## 5. Tier 3 — download

Once the bytes are in lmm's staging directory, **a downloaded item is an ordinary mod**: the
existing `Installer` takes it through cache → linker → `mod_path` with no Workshop-specific
code, and its row has `External: false`. Tier 3 needs no new job kind, no new CLI command
(`lmm install steamworkshop:<fileid>` and the SPA install flow already exist) and no new plan
type. It needs exactly one seam.

### The seam: `source.Fetcher`

`GetDownloadURL` returns a URL and `core.Downloader` GETs it. steamcmd is not a URL. New
optional interface in `internal/source/source.go`, in the house optional-capability style:

```go
// Fetcher is implemented by sources whose files cannot be retrieved by an
// HTTP GET of a URL (Steam Workshop: a steamcmd shell-out). Core prefers
// GetDownloadURL and falls back to Fetch only when the source returns
// ErrNotSupported from it. The returned path MUST be inside destDir.
type Fetcher interface {
    Fetch(ctx context.Context, mod *domain.Mod, fileID, destDir string, progress FetchProgressFunc) (string, error)
}
type FetchProgressFunc func(phase, detail string, bytes int64)
```

`core.Downloader` calls `GetDownloadURL` first; on `source.ErrNotSupported` it creates a
staging dir (`Service.NewStagingDir`) and calls `Fetch`, then verifies the returned path is
inside it. The `file://` guard (`source.LocalFileServer`, #300) is untouched — `Fetch` returns
a local path by contract, into a directory core itself chose, so no source gains the ability
to name an arbitrary path.

### Path A — legacy `file_url`

Populated only on old UGC-era items (spike: Skyrim item 68336872 works; modern items have it
empty). Unauthenticated **GET** — the CDN 404s on HEAD, so lmm must never probe with one.
Written straight into staging and **size-verified against the API's `file_size`** (the spike
matched 572,330 B exactly); a mismatch is a hard failure and the partial file is removed.
This path is a plain `GetDownloadURL` return — no `Fetch`, no steamcmd, no external tool.

### Path B — steamcmd, anonymous only

Gated on a **runtime probe**, copying `internal/core/extractor.go`'s `extract7z`
(lines 249–283): `exec.LookPath("steamcmd")` at call time, `exec.CommandContext` with a
timeout, combined output captured for the error message. Not vendored, never auto-installed;
the ~200 MB self-bootstrap is the user's to install and the user's to remove.

```
steamcmd +force_install_dir <staging> +login anonymous \
         +workshop_download_item <appid> <fileid> +quit
```

- **`+force_install_dir` is ALWAYS pinned, and it comes before `+login`.** The spike verified
  the trap: without it, steamcmd found the user's real Steam library and wrote into it. The
  item lands at `<staging>/steamapps/workshop/content/<appid>/<fileid>/`.
- **`HOME` is isolated** to `<Paths.CacheDir>/_steamworkshop/steamcmd-home/` (plus `XDG_*`
  pointed inside it) so steamcmd can never discover the real install. This home is
  **persistent, not per-run**: a per-run home re-pays the ~200 MB bootstrap on every download.
  It is created by lmm, listed in the README's file-locations section, and safe for the user
  to delete at any time.
- Timeout **30 minutes** (items reach several GB), cancellable via ctx.
- No account-session fallback, per the ruling. No password ever touches lmm.

### Error taxonomy (the spike's observed strings)

All three are `domain` sentinels wrapped by one `Details()`-bearing type,
`core.WorkshopFetchError` in `internal/core/errors.go` (so it lands inside
`cmd/lmm/details_coverage_test.go`'s walk, which covers core/cmd/serve but not domain):

| Observed | Sentinel | User-facing meaning |
|---|---|---|
| `ERROR! Download item … failed (Failure).` | `domain.ErrWorkshopAnonymousRefused` | "This game's publisher does not allow anonymous Workshop downloads (verified for Wallpaper Engine; Space Engineers 1/2 and RimWorld do allow it). **Subscribe to the item in the Steam client, then run `lmm import --workshop`** — lmm will track it in place." **This is the Tier-1 fallback the ruling requires.** |
| `ERROR! Download item … failed (Access Denied).` | `domain.ErrWorkshopItemUnavailable` | "Item is invalid, delisted, or not visible." Same sentinel as an API `result: 9`. |
| `exec.LookPath` miss | `domain.ErrExternalToolMissing` | "steamcmd is not installed. Install it from your distribution's packages or https://developer.valvesoftware.com/wiki/SteamCMD, then retry." |
| non-zero exit, no marker | wrapped generic | Last 4 KiB of combined output (`errorBodyLimit` convention). |

`WorkshopFetchError.Details()` → `{app_id, published_file_id, reason, tool, output_tail}`.

Anonymous availability is **not detectable from metadata** (the spike: a refused item still
returns full metadata), so this is probe-and-report by design — never a pre-flight promise.

### Progress for the shell-out

steamcmd's output is not a dependable progress stream, and a silent multi-GB download is the
worst possible UX. Three new `DeployPhase` values — `WorkshopFetchStarted`,
`WorkshopFetchProgress`, `WorkshopFetchDone` — carried on `StepEvent`, driven by:

1. any `Update state … progress: N.NN` line steamcmd *does* print, when it prints one; and
2. a **15-second heartbeat** carrying elapsed time and bytes-on-disk (a `du` of the staging
   subtree). Bytes on disk is a real signal that needs no output parsing and works even when
   steamcmd says nothing at all.

These flow through the existing `EventSink` → SSE → SPA `jobprogress` path unchanged.

---

## 6. Frontends

### CLI parity ledger

| Command | Change | Tier |
|---|---|---|
| `lmm import --workshop` | **New mode** — workshop adopt | 1 |
| `lmm game detect` / `game add --from-detected` | Prefills `steamworkshop`; `--no-workshop` opts out | 1 |
| `lmm game edit --source steamworkshop=<appid>` | None — `UpdateGameSources` is already generic | 1 |
| `lmm list` | `EXTERNAL` marker column | 1 |
| `lmm status` | `external_count` in the readout | 1 |
| `lmm mod show` | "Managed by: Steam Workshop (path)" block | 1 |
| `lmm update` | External items reported, never applied; `--refresh` bypasses the metadata cache | 1 |
| `lmm update <mod>` / `update rollback` / `mod edit` / `mod enable\|disable` / `deploy --mod` | Refused with `ErrExternalMod` | 1 |
| `lmm deploy` / `purge` / `profile switch\|apply\|sync` | Skip external mods; plans list them | 1 |
| `lmm uninstall` | Tracking-only removal, reworded confirmation | 1 |
| `lmm verify` | Presence-only tier for external mods | 1 |
| `lmm mod set-update auto` | Refused for external mods | 1 |
| `lmm auth login\|logout\|status steamworkshop` | None — existing flow | 2 |
| `lmm search --source steamworkshop` | None — existing flow | 2 |
| `lmm profile import --workshop-collection <id\|url>` | **New flag** | 2 |
| `lmm install steamworkshop:<fileid>` | None — existing flow, via the `Fetcher` seam | 3 |

**No new top-level command in any tier.**

### SPA surfaces

`setupsources.js` / `setupauth.js` (Steam Workshop row, BYO-key field, T2) ·
`sourcesmap.js` (no code change; an app-id placeholder hint, T1) ·
`searchpage.js` / `searchresults.js` (generic source-scoped search; collection-URL
affordance, T2) · `modrows.js` (`Steam` badge, existing colour tokens only — the
`no_hardcoded_color`/`contrast` ratchets forbid a new literal, T1) ·
`modpanel.js` / `fullmodpage.js` ("Managed by Steam" block, hidden actions, the steamcmd
explainer, T1/T3) · `missioncontrol.js` (deploy-card counts exclude external, T1) ·
`setupadopt.js` (a workshop-adopt card — see Q3) · `profilesmodal.js` /
`plan_profile_import.js` (collection input, T2) · new `plan_workshop_adopt.js` + a
`plankinds.go` entry + `kind_workshop_adopt.go` (T1).

**steamcmd availability is surfaced by the typed error, not by a new field.** The SPA shows
the install action unconditionally; attempting it on a machine without steamcmd returns
`ErrExternalToolMissing` with `Details()` naming the binary and the install hint, rendered as
a dismissible explainer on the mod page. The alternative — a `SourceInfo.Notes`/`Health()`
field so the button can be pre-disabled — adds shared-type machinery for one caller and a
second place for the same fact to go stale. One mechanism, and it is required anyway.

### Events and typed errors

- **`core.ExternalModError`** (`internal/core/errors.go`): one type for every refusal —
  `{Op string, Mod domain.ModReference, ModName, Reason string}`, `Unwrap() → ErrExternalMod`,
  `Details() any`. One type rather than five keeps `details_coverage_test.go` honest with one
  entry and gives the product one wording for one kind of refusal (the `LockedRef…` precedent).
- **`core.WorkshopFetchError`** (§5) — `Details() any`, one entry.
- New `Op` value `OpWorkshopAdopt`; new `DeployPhase` values `WorkshopScanned`,
  `WorkshopAdopted`, `WorkshopSkipped`, `WorkshopUnavailable`, `WorkshopFetchStarted`,
  `WorkshopFetchProgress`, `WorkshopFetchDone`; new `DeployModClass` value `external`.

### JSON documents

**Extended, all additive (`omitzero`/`omitempty` ⇒ every existing document byte-identical):**
`domain.InstalledMod` (+`external`, +`external_path`) · `domain.DetectedGame`
(+`workshop_items`) · `core.InstalledDetail` (same two) · `core.GameStatus`
(+`external_count`) · `core.UpdatePlan` (+`external`; reuses `refusal`) ·
`core.UpdateCheckReport` (+`external`) · `core.UninstallPlan` (+`external`) ·
`core.PurgePlan` (+`external`) · `core.DeployPlanMod.class` gains the *value* `"external"`
(no new key).

**Added:** `core.WorkshopScan`, `WorkshopAdoptPlan`, `WorkshopAdoptEntry`,
`WorkshopAdoptResult`, `WorkshopCollection`, plus the two error `Details()` shapes.

---

## 7. Testing

**Fixtures — nothing is ever fetched live in a test.**

- **ACF corpus** (`internal/source/steamworkshop/testdata/acf/`): a populated
  `appworkshop_1133870.acf` (3 items with `WorkshopItemsInstalled` + `WorkshopItemDetails`,
  in the spike's documented structure) · an **empty-stub** `appworkshop_294100.acf`
  (`WorkshopItemsInstalled {}` — the shape that must parse cleanly) · a truncated/malformed
  one · one whose items carry `timeupdated` but no `manifest`.
- **Recorded API responses** (`testdata/api/`): `getpublishedfiledetails_ok.json` (2 items),
  `getpublishedfiledetails_mixed_result9.json` (one good, one `result: 9`),
  `queryfiles_page1.json`, `queryfiles_403.json`, `getcollectiondetails_ok.json` — all
  hand-authored from the spike's documented field lists, served by `httptest`. The client
  takes an `*http.Client` and a base URL, so a test that reached the real network would have
  to pass the production URL explicitly; a package test asserts none does.
- **Fake steamcmd** (`testdata/fakesteamcmd/steamcmd`, a bash script prepended to `PATH`):
  switches on the file id to print the spike's exact `success` / `(Failure)` /
  `(Access Denied)` outputs, and on success creates
  `<force_install_dir>/steamapps/workshop/content/<appid>/<fileid>/mod.txt`. It **exits 3 if
  `+force_install_dir` was not passed**, turning the spike's most dangerous trap into a test
  that fails loudly. It also asserts `HOME` is not the real one. Tests skip when `bash` is
  unavailable — the 7z/rar optional-tool pattern.

**Layers.** Source: table-driven against the fixtures above (ACF parse, metadata mapping,
update-signal precedence, `result != 1`, cache TTL + negative caching, backoff/breaker,
QueryFiles mapping, the three steamcmd outcomes). Core:
`internal/core/workshop_adopt_test.go` with a fake `ModSource` and a `t.TempDir()` Steam root;
per-flow tests that an external mod is skipped/refused by deploy, purge, switch, enable,
disable, update-apply, rollback and relink, and reported by list/status/verify/mod-show.
Serve: `api_flow_workshop_adopt_internal_test.go`, asserting **end state** — DB rows, profile
refs, and that **nothing was written under `mod_path`**. E2E (`e2e_test.go`, real headless
Chrome, skipped when absent): the library badge renders, the mod page hides deploy/disable for
an external mod, the setup sources card shows the Steam Workshop key field.

**Ratchets that must stay green** (each one can fail on this work): `json_golden_test.go` ×4
packages + `json_contract_coverage_test.go` (serve) · `doc_comment_test.go` ×4 ·
`details_coverage_test.go` (the two new `Details()` types) · `no_hardcoded_color_test.go` /
`contrast_test.go` (the `Steam` badge uses existing tokens) ·
`TestSPAModuleGraphResolvesOverHTTP` (the new `plan_workshop_adopt.js` import path) ·
`cmd/lmm/boundary_test.go` and `internal/serve/boundary_test.go` — **`internal/core` must
not import `internal/source/steamworkshop`**; only `internal/app` may.

---

## 8. Implementation plan

Three story units, one per tier, each merged `--no-ff` into `v2` per the v2 branch model.

### W1 — "Steam Workshop Tier 1: adopt and track already-subscribed items (#269)"

Files: **new** `internal/source/steamworkshop/{steamworkshop,client,acf,metacache,updates}.go`
· `internal/core/workshop_adopt.go` · `internal/serve/kind_workshop_adopt.go` ·
`internal/serve/spa/app/components/plan_workshop_adopt.js`.
**Touched** `internal/domain/{mod,game}.go` · `internal/source/{source.go(no change),
httpclient/client.go}` · `internal/source/steam/steam.go` (workshop prefill) ·
`internal/core/{errors,deploy,purge,uninstall,update,updater,verify,queries,moddetail,
game_detect,events,phases}.go` · `internal/app/sources.go` ·
`cmd/lmm/{import,list,status,mod,update,uninstall,deploy,verify}.go` ·
`internal/serve/plankinds.go` + `modrows/modpanel/fullmodpage/missioncontrol/setupadopt`.
Goldens: the 13 new + the `_external` variants (§2). Tests: §7 minus steamcmd and QueryFiles.
**Size ≈ 2000–2500 lines with tests** (the largest unit — it defines the package, the two
domain fields and every flow rule).
Gate: `go test ./... -race`, every ratchet in §7, `go vet`, `trunk check`, `make man` **not**
run (no version bump), plus a read-only live smoke on the owner's SE2/Human Host libraries.

### W2 — "Steam Workshop Tier 2: search and collections with a bring-your-own API key (#269)"

Files: **new** `internal/source/steamworkshop/{search,collection,auth}.go`.
**Touched** `internal/core/profile_import.go` · `cmd/lmm/{auth,search,profile}.go` ·
`internal/serve/{api_profiles.go, spa setupsources/setupauth/searchpage/profilesmodal}`.
Goldens: `workshop_collection`, `profile_import_workshop_*`.
**Size ≈ 900–1300 lines.** Depends on W1 (package, client, cache).
Gate: as W1, plus a keyed live smoke **the owner runs** — an agent never handles the key.

**Shipped 2026-09-09** on `dyoung522/v2-workshop-w2`, as designed, with three
additions the build surfaced and one deviation:

- `source.CollectionResolver` / `source.Collection` (the seam core consumes, so
  `internal/core` still imports no concrete source package) and
  `source.ErrInvalidReference` (so `internal/serve`, which may not import
  `internal/source`, can be told by `core.IsBadCollectionRef` that a 400 is
  right).
- `core.SearchHit` gains an additive `external`: a CATALOG document has no
  installed row to read the version-display rule from, so without it `lmm
  search --source steamworkshop` and the SPA's result rows would have printed
  the 19-digit content id — the very rule the approval note added.
- `GET /api/v1/search` answers **401**, not 500, when the only source needs a
  key. That is the ordinary state of a Workshop-only game before `lmm auth
  login steamworkshop`.
- **Deviation:** a collection import forces `NoInstall` (in core, so both
  frontends inherit it) rather than letting the ordinary install loop try. Tier
  3 owns the download path; until it lands, attempting it would spend a round
  trip per item to produce a stack of `ErrNotSupported` failures the plan
  already answers per item ("subscribe in Steam and re-run `lmm import
  --workshop`"). W3 removes the force.

#365's two Tier-1 follow-ups landed here: `domain.ModReference` gained additive
`external`/`updated_at` (`yaml:"-"`, so no profile file changes), which core
stamps on `ImportPlan`'s and `ProfileSyncPlan`'s buckets; and the library row
menu stopped offering "Re-link…" for an external row.

### W3 — "Steam Workshop Tier 3: download items via file_url or anonymous steamcmd (#269)"

Files: **new** `internal/source/steamworkshop/{download,steamcmd}.go` +
`testdata/fakesteamcmd/`. **Touched** `internal/source/source.go` (`Fetcher`) ·
`internal/core/{downloader,errors,phases}.go` · `cmd/lmm/install.go` (error rendering) ·
SPA `fullmodpage/modpanel` (steamcmd explainer).
Goldens: `workshop_fetch_error`.
**Size ≈ 900–1300 lines.** Depends on W1; **independent of W2**.
Gate: as W1, plus fake-steamcmd tests, plus a live smoke on SE2 (allows anonymous) and, if
the owner has it, Wallpaper Engine (refuses → the Tier-1 fallback message).

### Parallelism

**W1 alone first** — it owns the domain fields, the package skeleton and every flow rule, so
neither sibling can land coherently before it. **W2 ∥ W3 after W1 merges.** Their only shared
surface is `steamworkshop`'s `Capabilities()` and package doc comment; whichever merges second
resolves a two-line conflict.

---

## 9. Open questions

Each carries a recommendation; each is decided-unless-overruled, so none blocks W1 starting.

**Q1 — Do external mods appear in `lmm profile reorder` / the SPA reorder modal?**
*Recommend **no*** — load order decides deploy precedence, and an external mod deploys
nothing, so any position it held would be inert. Counter-argument: users may expect one list
containing everything they see in `lmm list`. Mitigation if overruled: show them pinned at the
end, unmovable, labelled "ordered by Steam".

**Q2 — `lmm install steamworkshop:<fileid>` for an item the user is already subscribed to
(i.e. already tracked as external).**
*Recommend **refuse with a note***: "already tracked from your Steam subscription — uninstall
it first if you want lmm to manage its own copy." Silently creating a second, lmm-deployed
copy alongside the one Steam loads is how you get a mod that appears twice in-game and a
conflict report that cannot explain itself.

**Q3 — Does first-run setup offer a workshop adopt?**
*Recommend **yes**, as a second card in `setupadopt.js`* after the `mod_path` scan: it is the
highest-value zero-cost discovery a Workshop-native game has, and the scan is a pure local
read. It is extra SPA scope inside W1, so it is called out here for the coordinator to defer
to a follow-up issue if W1 is already the biggest unit.

---

## Appendix — what lmm must never do (from #268, restated as build constraints)

Embed or share a Steam Web API key · rehost, redistribute or proxy Workshop content · store
Steam account passwords (and, per the 2026-09-09 ruling, no account-session fallback at all in
v2.0.0) · attempt subscription management · vendor or auto-install steamcmd · run steamcmd
without `+force_install_dir` pinned to lmm's own staging · probe a download CDN with HEAD.

---

## Approved with notes (coordinator, 2026-09-09)

Approved as written, with these rulings and corrections — each is binding on W1–W3:

- **Q1 — no.** External mods are omitted from `lmm profile reorder` and the SPA reorder modal.
- **Q2 — refuse with the note.** `lmm install steamworkshop:<fileid>` on an item already tracked
  as external refuses: "already tracked from your Steam subscription — uninstall it first if you
  want lmm to manage its own copy."
- **Q3 — deferred to its own issue** (the first-run workshop-adopt card in `setupadopt.js`),
  filed against v2.0.0 so it stays on the bar; W1 does not build it.
- **Version display.** The ACF `manifest` content id is the item's version *identity* (as
  designed: `InstalledMod.Version`, `Update.NewVersion`, `--json`), but no human-facing surface
  prints a 19-digit number as a version: `lmm list`, `lmm status`, the update summary and the
  SPA rows show the item's `time_updated` as a date (`2026-08-02`); `lmm mod show` and the mod
  page show the date and, beneath it, the manifest labelled as such.
- **`make man` is required, not skipped.** The genman test enforces `docs/man` in sync with the
  command help, and W1 adds `lmm import --workshop` and `game detect --no-workshop`; W2 adds
  `profile import --workshop-collection`. Run `make man` in every unit that touches help text.
- **Live smokes.** No worker ever reads or writes the owner's Steam library, calls the Web API,
  or runs steamcmd. The read-only smoke of W1 (`lmm game detect`, `lmm import --workshop
  --dry-run` against the real SE2/Human Host libraries) is the coordinator's; the keyed (W2) and
  steamcmd (W3) smokes are the owner's hand-test checkpoints.
- **`httpclient` additions** (`AuthQueryParam`, `DoForm`) and the optional `source.Fetcher`
  seam are accepted as designed; both must leave every existing client byte-for-byte unchanged in
  behaviour (a test per addition proves it).
- **Sub-issues.** W1, W2, W3 are filed as their own issues under #269 and close at their
  respective merges; #269 closes when W3 merges.
