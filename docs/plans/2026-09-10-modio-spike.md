# mod.io as a Mod Source — Spike Findings (#407)

**Date:** 2026-09-10 · **Status:** Complete · **Issue:** #407 · **Type:** Research only, no code

Scope: which of the owner's installed games (#406) actually use mod.io, mod.io's public API
and auth model, its package/version/download model, and how the Steam Workshop source's
tiered shape (#269: track / search / download) maps onto it.

Method: mod.io's own public documentation (`docs.mod.io`), mod.io's API Access Terms
(`mod.io/legal/api`), developer and publisher statements, plus a read of the existing
`internal/source/steamworkshop` package and the `source.Fetcher` seam in
`internal/source/source.go`. **No live API call was made, with or without a key**, and
nothing on the owner's machine was read. Every claim below is either sourced to a URL or
explicitly marked unverified.

---

## 0. Headline

**The premise the spike was opened on does not hold.** Space Engineers 2 does **not**
distribute mods through mod.io: Keen announced mod.io first, reversed the decision after
community feedback, and shipped Steam Workshop instead. Of the 21 games `lmm game detect`
finds on the owner's machine, **exactly one — Space Engineers (1) — uses mod.io**, and it
uses it as the *cross-platform alternative* to the Steam Workshop, for crossplay and console
parity, not as its primary PC channel.

Per tier, on the facts:

| Tier | Verdict | One-line reason |
|---|---|---|
| **T1 — track locally-installed items** | **CONDITIONAL GO** | The tracking machinery already exists (`External`/`ExternalPath`, #269), but nobody has publicly documented where Space Engineers puts a mod.io mod on disk. Gated on a 10-minute owner probe (Q1). |
| **T2 — search + metadata** | **GO** | A clean bring-your-own-key fit: personal API key from `mod.io/me/access`, read-only GET, `api_key=` query parameter, 60 req/min. Every seam it needs already exists. |
| **T3 — download** | **GO** | A plain HTTPS GET of an expiring pre-signed `binary_url`. No external tool, no `steamcmd`, no `Fetcher` needed — *simpler* than Workshop Tier 3. |
| **Deploy the downloaded bytes** | **NO-GO for Space Engineers** | SE loads mods through its own in-game mod list and per-world config, not from a directory lmm can converge. See §4.4. |

**Recommendation to the owner (§6, Q0):** the tiers are individually feasible and the work is
well-shaped, but on today's game list a `modio` source buys *tracking and update notices for
one game that already has a Steam Workshop source pointed at it*. That is the weakest
cost/benefit of anything currently on the 2.0 bar. Recommend the coordinator put it to the
owner as a defer-to-post-2.0 candidate. **This is a recommendation, not a decision, and no
label has been applied.**

---

## 1. Which of the owner's games use mod.io

### 1.1 Space Engineers 2 — **no**

Keen's original plan was mod.io ("the goal of providing a unified backend for both PC and
console"). They reversed it before Early Access. Keen's own support topic:

> "Space Engineers 2 will use Steam Workshop as its primary platform for user-generated
> content."
> — [Modding in Space Engineers 2 — Steam Workshop, Keen Support topic 45156](https://support.keenswh.com/spaceengineers2/pc/topic/45156-modding-in-space-engineers-2)

Announced the same way on the studio dev blog
([Space Engineers 2 — Steam Workshop Support, Marek Rosa, 2025-01](https://blog.marekrosa.org/2025/01/space-engineers-2-steam-workshop-support/)),
with Workshop support landing in the first major update after the 2025-01-27 EA launch.
Console UGC was left open at the time; nothing public says it landed on mod.io.

**Consequence:** SE2 is a Steam Workshop game and is already covered by the shipped
`steamworkshop` source (#269 W1–W3). It belongs to #406's S1 story, not here.

### 1.2 Space Engineers (1) — **yes**

SE1 carries mod.io *alongside* the Steam Workshop. mod.io is the channel that makes mods work
across the Steam/Xbox EOS crossplay boundary:

- Keen's crossplay announcement (Update 197.1, 2021-02) added mod.io integration to every
  in-game screen that reaches the Workshop, and enabled mods on Xbox and on dedicated
  servers — [Space Engineers: Community Crossplay](https://www.spaceengineersgame.com/space-engineers-community-crossplay/).
- The game has a live mod.io profile: [mod.io/g/spaceengineers](https://mod.io/g/spaceengineers),
  and mod.io's own support site carries a
  [Space Engineers section](https://support.mod.io/hc/en-us/sections/9171643544591-Space-Engineers).
- Server config distinguishes the two channels explicitly, per host documentation:
  `<ModItem FriendlyName="…"><Name>ID.sbm</Name><PublishedFileId>ID</PublishedFileId><PublishedServiceName>mod.io</PublishedServiceName></ModItem>`
  — [BisectHosting: How to Install Mods on a Space Engineers Server](https://www.bisecthosting.com/clients/index.php?rp=%2Fknowledgebase%2F565%2FHow-to-install-mods-on-a-Space-Engineers-server.html).
  A Workshop item uses the same shape with `PublishedServiceName` set to Steam.

So an SE1 mod has *two* possible identities — a Steam published-file id and a mod.io mod id —
and they are different numbers for the same mod.

### 1.3 Every other detected game — **no**

Checked against the #406 list. None has an announced mod.io integration; the "primary
channel" column is what the public record says the game's ecosystem actually uses.

| Game | mod.io | Primary channel (public record) |
|---|---|---|
| Space Engineers | **yes** | Steam Workshop **+ mod.io** (crossplay/console) |
| Space Engineers 2 | no | Steam Workshop (Keen, §1.1) |
| Human Host | no | [Steam Workshop](https://steamcommunity.com/app/2393970/workshop/) (Unity/Mono + BepInEx) |
| Cyberpunk 2077 | no | Nexus Mods |
| Valheim | no | Thunderstore / Nexus (BepInEx) |
| 7 Days to Die | no | Nexus Mods / community modlets |
| No Man's Sky | no | Nexus Mods (`GAMEDATA/MODS` paks) |
| Satisfactory | no | ficsit.app (Satisfactory Mod Repository) |
| Subnautica 2 | no | **No official mod support in EA** — [Subnautica 2 Help Center](https://support.subnautica.com/hc/en-us/articles/57020140859929-Will-there-be-any-mod-support); community mods on Nexus |
| The Planet Crafter | no | Nexus (BepInEx) |
| Tainted Grail: The Fall of Avalon | no | [Nexus Mods](https://www.nexusmods.com/taintedgrailthefallofavalon/mods/top) |
| StarRupture | no | [Nexus Mods](https://www.nexusmods.com/starrupture/mods/89) (AlienX loader); no Workshop |
| Grim Dawn | no | Official mod tools; ModDB / Nexus |
| The Elder Scrolls Online | no | ESOUI add-ons directory |
| Halo: Campaign Evolved | no | No official tools/Workshop; Nexus + MJOLNIR Core |
| Cubic Odyssey | no | None yet — [devs "looking into it"](https://steamcommunity.com/app/3400000/discussions/0/567001570938457027/) |
| Windrose | no | None announced (EA, 2026-04) |
| The Blood of Dawnwalker | no | None announced |
| For The King | no | None announced |
| LEGO Batman: Legacy of the Dark Knight | no | None |
| Satisfactory Modeler | no | A tool, not a game (#406 S3 already calls this detect-only) |

**Absence of evidence caveat.** mod.io's own game directory is a client-rendered SPA and
could not be enumerated from a fetch; the negatives above rest on each game's own developer
communications and modding ecosystem, which is the strongest public signal available but is
not the same as reading mod.io's catalogue. A game that quietly added mod.io without
announcing it would not show up here. The confident finding is the positive one: SE1 uses
mod.io, SE2 does not.

---

## 2. API and auth model

Base URL `https://api.mod.io/v1`; HTTPS/TLS required on every request.
Reference: [mod.io API v1](https://docs.mod.io/restapiref/).

### 2.1 Two credential types, and what they can do

| Credential | How obtained | Capability | Rate limit |
|---|---|---|---|
| **Game-linked API key** | Issued to a studio that registers *its own game* on mod.io | Read-only GET | Unlimited |
| **User (personal) API key** | Any account, at [mod.io/me/access](https://mod.io/me/access) | Read-only GET | **60 requests/minute** |
| **OAuth 2 access token** | Email code exchange, or platform SSO (Steam/Xbox/PSN/Switch/Epic/GOG/Apple/Google/OpenID), or manual creation in the dashboard | Read **and write**, scoped `read` / `write` / `read+write`; default expiry ~1 year, settable shorter via `date_expires` | 120 req/min (60 for writes) |
| — | (any) | — | **IP limit 1000 req/min**, 60 for writes |

The API key rides as a query parameter (`?api_key=…`); an OAuth token rides as
`Authorization: Bearer …`. mod.io is explicit that keys are read-only "due to the limited
security it offers", and that tokens must never be embedded in client code.

**429 handling.** A rate-limited response is `429 Too Many Requests` with a `retry-after`
header in seconds. Per-endpoint limits exist on top of the global ones and surface as
`error_ref` **11009**. The old `X-RateLimit-*` headers were retired after 2022-11-20 — a
client must key off `retry-after` only. mod.io warns that clients which keep hammering after
a 429 "could potentially have their credentials revoked". The docs' own guidance:
*"You should always plan to minimize requests and cache API responses."*

### 2.2 What the ToS lets a distributed tool bundle: **nothing**

From [mod.io's API Access Terms](https://mod.io/legal/api) — these are the clauses that
decide lmm's model:

> **1.1** "You may register to use the mod.io API by requesting an API Key through your
> mod.io User Account at <https://mod.io/me/access>"
>
> **3.2** "You agree to only use the API Key that is linked to your User Account."
>
> **3.3** "You agree to include mod.io's branding at all times you are using the mod.io API.
> You agree to not whitewash the mod.io API or represent the mod.io API as your own."
>
> **3.4** "Your use of the mod.io API may be subject to usage limits … mod.io is entitled to
> enforce these usage limits, which may result in mod.io blocking access to the mod.io API to
> You."
>
> **3.5** "You will not use the mod.io API to bypass or create a competing or replacement
> product for mod.io … for spamming or scraping data, or undertake any activity that is
> intended to or has the likely effect of disrupting the mod.io API."

Three consequences, all binding on any design:

1. **Bring-your-own-key is not a preference, it is the only lawful model.** §3.2 makes a key
   personal to an account; a key shipped inside lmm's binary and used by thousands of people
   is exactly what it forbids. This is the same conclusion #268 reached for the Steam Web API
   key, from a near-identical clause, so the house pattern already matches
   (`EnvKeyProvider` + `KeyValidator` + `AuthInstructionsProvider`).
2. **§3.3 is a new obligation no other lmm source carries.** NexusMods and CurseForge impose
   no contractual UI requirement; mod.io does. A `modio` source means lmm must visibly
   attribute mod.io wherever mod.io data is shown — a line in `lmm source list`, a "via
   mod.io" label on CLI mod output, and a badge/label plus a link on the SPA's mod rows and
   mod page. Cheap, but it must be *designed in*, not discovered at review.
3. **§3.5 needs a straight answer before code is written.** A mod manager that fetches, for
   the user's own account and rate budget, the mods that user asks for is ordinary API use.
   A tool that mirrors mod.io's catalogue, or that presents itself as a place to browse mods
   instead of mod.io, is the "competing or replacement product" the clause names. lmm's
   search-and-install shape sits on the safe side of that line, and staying there is a design
   constraint: no bulk catalogue sync, no local mirror of mod listings beyond a short-TTL
   response cache, and mod.io attribution on every surface (see Q3).

### 2.3 The OAuth path, if it is ever needed

Two POSTs, both of which **require an `api_key`** — there is no keyless user login:

1. `POST /oauth/emailrequest` with `api_key` + `email` → mod.io emails the user a security
   code.
2. `POST /oauth/emailexchange` with `api_key` + `security_code`, plus optional `date_expires`
   and `terms_agreed` → an Access Token object.

Before either, the integrating tool must show mod.io's consent text — the Terms endpoint —
with clickable links to `mod.io/legal/terms` and `mod.io/legal/privacy`, and pass
`terms_agreed=true` only after the user accepts. A `403` with `error_ref` **11074** means the
terms changed and must be re-accepted; a `401` means the token expired and the user must
re-authenticate ([Terms & User Consent](https://docs.mod.io/terms)).

lmm needs this **only** if Tier 1 has to fall back to reading the user's subscriptions from
the API (`GET /me/subscribed`, OAuth-only) instead of reading the game's own on-disk state.
That is a materially bigger unit than a key field — see §4.1 and Q1.

---

## 3. Package, version, download and dependency model

### 3.1 The object model

A **Mod** (`GET /games/{game-id}/mods/{mod-id}`) carries `id`, `name_id` (URL slug), `name`,
`summary`, `description`, `profile_url`, `tags`, `stats`, `date_updated`, `maturity_option`,
and — critically — `modfile`: the **single latest live file**, embedded in the mod object.

A **Modfile** (`GET …/mods/{mod-id}/files`, `…/files/{file-id}`) carries:

| Field | Maps to |
|---|---|
| `id` | `domain.DownloadableFile.ID` |
| `mod_id` | (parent) |
| `version` | `DownloadableFile.Version` — a free-text string set by the author, e.g. `"1.3"` |
| `changelog` | `source.ChangelogProvider` — no second round trip needed |
| `filename` | `DownloadableFile.FileName` |
| `filesize` | `DownloadableFile.Size` — **exact**, so `source.ExactFileSizer` applies |
| `filehash` | **md5 only** — see §4.5 |
| `date_added` | file recency |
| `platforms` | per-platform availability — see §4.3 |
| `download` | `{ binary_url, date_expires }` |

**Version identity is genuinely better than the Workshop's.** The Workshop has no version
string at all, which is why #269 had to use a 19-digit content-manifest id as the identity and
then rule that no human-facing surface prints it. mod.io gives a real author-set `version`
string *and* a monotonically increasing numeric `modfile.id`. Recommended mapping:
`InstalledMod.Version` ← `modfile.version` when non-empty, else `modfile.id`;
update detection compares `modfile.id`, never the version string (authors reuse and
regress version strings; the file id is authoritative).

### 3.2 Download mechanism

`download.binary_url` is a **pre-signed, expiring** URL (`download.date_expires`, Unix
seconds). The documented example points back at
`https://api.mod.io/v1/games/{g}/mods/{m}/files/{f}/download`, i.e. mod.io serves the redirect
itself. Per the reference, the URL is usable as a plain GET; whether it also accepts/requires
the `api_key` on the hop is not stated (Q4).

This fits lmm's existing download path with **no new seam**:

- `Service.downloadModToCache` calls `GetDownloadURL` *immediately* before fetching
  (`internal/core/service.go:1012`) — a plan never carries a URL, so an expiring URL cannot go
  stale between plan and apply. Verified in the tree.
- `Downloader.redirectSafeClient` already follows redirects with a 10-hop cap and strips
  `Authorization`/`Cookie` on a cross-host hop (`internal/core/downloader.go:149-181`), which
  is exactly right for an api.mod.io → CDN redirect.
- `source.Fetcher` (the seam #269 W3 added for `steamcmd`) is **not needed**. mod.io is a URL.

### 3.3 Dependency metadata — real, and better than the Workshop's

`GET /games/{game-id}/mods/{mod-id}/dependencies` returns Mod Dependencies objects with
`mod_id`, `name`, `name_id`, `modfile`, `date_added` and **`dependency_depth`**. The presence
of a depth field says mod.io returns a flattened tree rather than one level, though the
reference page did not state a `recursive` filter explicitly (Q5). Either way this maps
straight onto `ModSource.GetDependencies` → `[]domain.ModReference`, and
`core.DependencyResolver` (which already does ordering and cycle detection for NexusMods and
CurseForge) needs no change. `Capabilities().Dependencies` would be **true** — the Workshop
source reports false.

### 3.4 Update detection

Three usable signals, in preference order:

1. `mod.modfile.id != installed modfile id` — one `GET /games/{g}/mods?id-in=…` batches many
   mods into one request (mod.io's list filters accept `id-in`), so an update check is
   `ceil(N/100)` requests, not N.
2. `mod.date_updated` — coarser, catches metadata-only edits too (noisier).
3. `GET /games/{game-id}/mods/events` — an event feed for change detection at scale. Overkill
   for a single-user tool; noted for completeness.

Pagination on every list endpoint is `_limit` (default **100**) / `_offset`, with
`result_count` / `result_limit` / `result_offset` / `result_total` in the envelope — a direct
fit for `source.SearchResult{TotalCount, Page, PageSize}`.

### 3.5 Collections

mod.io has a first-class **Collections** feature in the API (Get Mod Collections, Get
Collection Mods, follow/subscribe, plus write endpoints). Reading one is a GET, so an API key
suffices. This is the same shape as the Workshop collections #269 W2 already handles, and it
maps onto the existing `source.CollectionResolver` seam and
`lmm profile import --workshop-collection`'s pattern with no new core machinery — only a
generalised flag name (Q6).

---

## 4. Tier mapping, GO/NO-GO, and what lmm would need beyond the Workshop source

### 4.1 Tier 1 — track locally-installed items · **CONDITIONAL GO**

The Workshop's Tier 1 works because Steam writes a machine-readable manifest
(`appworkshop_<appid>.acf`) that names every installed item and its content id. mod.io's
equivalent depends on which integration the game uses:

- **Games using mod.io's own SDK** (C++/Unity/Unreal) write a configurable local store: a
  `globalsettings.json` under `%LOCALAPPDATA%\mod.io\` with a `RootLocalStoragePath`, and,
  under it, one directory per mod id containing a `state.json` plus a modfile-id
  subdirectory. Reported by mod.io support and by mod.io's own account
  ([support article](https://support.mod.io/hc/en-us/articles/9809372059407-How-do-I-change-the-install-location-for-mods-from-mod-io),
  [@modiohq](https://x.com/modiohq/status/1588332358469836801)); the pages themselves return
  403 to a fetch, so this is **reported, not verified**. If SE1 follows this layout, Tier 1 is
  a direct analogue of the ACF scan: read `state.json`, get mod id + installed modfile id, and
  adopt as `External`.
- **Space Engineers is a custom engine (VRAGE) with a custom integration**, and no public
  document says where it puts a mod.io mod. Community documentation covers the Workshop path
  (`steamapps/workshop/content/244850/`) and the legacy local `Mods/<id>.sbm` convention, but
  the mod.io channel's on-disk footprint is undocumented. Under Proton it would sit inside the
  prefix, under `…/compatdata/244850/pfx/drive_c/users/steamuser/AppData/Roaming/SpaceEngineers/`.

**Verdict: CONDITIONAL GO**, gated entirely on Q1 — a two-command, read-only probe of the
owner's machine that a worker must not run. If a readable local manifest exists, Tier 1 is
*cheap*: `domain.InstalledMod.External` / `ExternalPath`, `source.WorkshopScanner`,
`DeployModClass "external"`, `ErrExternalMod` and every flow rule already shipped with #269
W1, and a `modio` scanner drops into them. If it does not exist, the only alternative is
OAuth + `GET /me/subscribed`, which trades a local read for a user login flow and a token
store change — a materially larger unit, and one whose answer ("what you are subscribed to")
is not the same question as "what is on this disk".

`source.WorkshopScanner` is named for the Workshop but its contract is generic ("sources whose
content is installed and owned by ANOTHER agent on the user's machine"); it needs a doc-comment
widening, not a redesign.

### 4.2 Tier 2 — search and metadata · **GO**

Everything needed exists:

- `httpclient.Options.AuthQueryParam` (added by #269 W1 for Steam's `key=`) carries mod.io's
  `api_key=` unchanged.
- `EnvKeyProvider` → `MODIO_API_KEY`; `AuthInstructionsProvider` → "Get a personal read-only
  key at <https://mod.io/me/access>. It is personal to your account — never share it, and
  never paste someone else's." `KeyValidator.ValidateKey` → any cheap authenticated GET
  (`GET /games?_limit=1`), which returns 401 on a bad key.
- `SearchQuery` → mod.io list filters: `Query` → `_q`, `Page`/`PageSize` → `_offset`/`_limit`,
  `Tags` → `tags-in`, `Category` → one more required tag (same ruling as #269 W2), sort via
  `_sort`. `SearchResult.TotalCount` ← `result_total`.
- `Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}`
  — strictly richer than the Workshop's.
- `ChangelogProvider` is free (`modfile.changelog` is already in the object).

Cache policy copies the Workshop's, which the docs independently ask for: short-TTL mod
metadata under `<CacheDir>/_modio/meta/`, negative caching for deleted/hidden mods, backoff
honouring `retry-after`, and a circuit breaker. The 60 req/min personal-key ceiling is tighter
than Steam's 100k/day, so batching (`id-in`, `_limit=100`) matters more here than it did there.

### 4.3 Tier 3 — download · **GO**, and easier than the Workshop's

A plain GET of `binary_url` through the existing `Downloader`, with retries, progress ticks
and checksum recording all for free. No `steamcmd`, no `Fetcher`, no isolated `HOME`, no
`+force_install_dir` trap, no "publisher forbids anonymous download" refusal class. `#269`'s
whole Tier-3 error taxonomy simply does not arise.

**The one genuinely new requirement: platform targeting.** mod.io honours an
`X-Modio-Platform` request header (`windows`, `mac`, `linux`, `android`, `ios`, `xboxone`,
`xboxseriesx`, `ps4`, `ps5`, `switch`, `oculus`) which filters *which mods and which modfiles
are visible*; **omitting it defaults to `windows`**. A companion `X-Modio-Portal` header
(`steam`, `gog`, `epicgames`, …) tunes display names. Games opt into per-platform files in
their dashboard; where they have not, all files are visible on all platforms.

This collides with lmm's core situation: lmm runs on Linux, but most of the games it manages
are **Windows builds running under Proton**, whose mods are Windows mods. Asking the host OS
is the wrong answer. lmm has no concept today of "the platform this game actually runs as".
The default (`windows`) is the right answer for a Proton game and the wrong one for a
Linux-native game with Linux-only modfiles.

*Recommendation:* default to `windows` and let a game override it — a per-source option
alongside `sources: {modio: "<game-id>"}` in `games.yaml`, or a `platform:` key on the game.
Deciding that shape is Q2. `X-Modio-Portal: steam` is a safe constant for a Steam-installed
game and a nice-to-have otherwise.

### 4.4 Deploying what was downloaded · **NO-GO for Space Engineers specifically**

This is separate from "can lmm fetch the bytes", and it is where the value case falls over for
the one game that qualifies. Space Engineers does not load mods from a directory that lmm can
converge: mods are selected **per world**, in the world's own config (the `<ModItem>` list
quoted in §1.2), by id *and* by service name. Dropping an extracted mod into a folder does not
enable it; SE's own mod list and world settings do. lmm's whole deploy model — converge a
directory to a desired state — has nothing to grip.

That is not a mod.io limitation (the same is true of SE's Workshop mods, which is precisely
why #269 built the `External` track-but-never-deploy representation). It means a `modio` source for
SE1 is a **Tier-1+2 tracker**, and Tier 3's bytes have no useful destination for that game.
Tier 3 keeps its GO because it is cheap and correct for the *next* mod.io game — one that
loads mods from a directory — but it earns nothing today.

### 4.5 What a `modio` source needs from lmm that the Workshop source did not

| # | Need | Size | Notes |
|---|---|---|---|
| 1 | **Platform targeting** (`X-Modio-Platform`) | Small–medium | New concept: the platform a game *runs as*, not the host OS. §4.3, Q2. |
| 2 | **md5 checksum verification** | Small | mod.io publishes `filehash.md5`; `domain.DownloadableFile` carries only `SHA256`. Core already computes both md5 and sha256 during a download (`internal/core/downloader.go:251-278`) and stores the md5 as `Checksum`, so this is one additive `MD5` field plus one comparison — or skip it and lean on `source.ExactFileSizer` (mod.io's `filesize` is exact), which is what the Workshop does. Recommend the additive field: a real checksum beats a length check. |
| 3 | **mod.io branding/attribution** (ToS §3.3) | Small | A contractual UI obligation no other source has. Text + link only: the SPA's `no_hardcoded_color`/`contrast` ratchets forbid new colour literals, so no logo colours. |
| 4 | **`WorkshopScanner` doc widening** | Trivial | The interface is already generic; the name and comment are Workshop-specific. Rename or re-document — renaming touches #269's shipped code, so prefer re-documenting. |
| 5 | **OAuth token storage** *(only if Q1 forces the `/me/subscribed` path)* | Medium–large | The token store holds an opaque key string with no expiry, refresh or scope. mod.io tokens expire (~1 year), and the email-code flow does not fit `ModSource.AuthURL()` + `ExchangeToken(code)` (that pair assumes a browser redirect). Plus the mandatory terms-consent UI in both frontends. **This is the single biggest swing factor in the whole estimate.** |
| 6 | Nothing else | — | `Fetcher`, `ExactFileSizer`, `CollectionResolver`, `ChangelogProvider`, `BatchModDescriber`, `RefreshingUpdateChecker`, `External`/`ExternalPath`, `ErrExternalMod`, the `external` deploy class, `AuthQueryParam`, redirect-safe downloads — all shipped, all reusable as-is. |

---

## 5. Story breakdown (sizes; **not filed** — the coordinator files)

Ordering assumes the owner says GO. **M0 is not a code story**; it is the probe that decides
whether M3 exists at all, and it must be run by the owner or the coordinator, never a worker.

| ID | Story | Depends on | Size (with tests) |
|---|---|---|---|
| **M0** | *Probe (not code):* confirm where SE1 stores mod.io mods on disk, and whether that store names the mod id and modfile id. Answers Q1. | — | ~10 min, owner/coordinator |
| **M1** | `modio` built-in source: package skeleton, registry entry, `httpclient` wiring on `AuthQueryParam`, BYO key (`EnvKeyProvider`/`KeyValidator`/`AuthInstructionsProvider`), `GetMod`, `Capabilities`, metadata cache + backoff + circuit breaker, `X-Modio-Platform` plumbing (#1 above), mod.io attribution (#3), `lmm auth login modio`. | — | **1200–1600** |
| **M2** | Search + `lmm search --source modio` + SPA search rows; `ChangelogProvider`; `GetDependencies`; `BatchModDescriber` via `id-in`. | M1 | **700–1000** |
| **M3** | Tier 1 tracking: local scan → `External` adopt, `lmm import --modio`, update-check by `modfile.id`, serve job kind, SPA badge/panel. **Exists only if M0 finds a readable local manifest.** | M1, M0 | **900–1300** |
| **M3′** | *Alternative to M3 if M0 comes back empty:* OAuth email-code auth, terms-consent UI in both frontends, token expiry in the store, `/me/subscribed` as the tracking source. | M1 | **1400–1900** |
| **M4** | Tier 3 download: `GetModFiles`/`GetDownloadURL` over `binary_url`, `ExactFileSizer`, md5 verification (#2 above). | M1 | **500–800** |
| **M5** | Collections → `lmm profile import --modio-collection` (or a generalised `--collection <source>:<ref>`, Q6) over the existing `CollectionResolver`. | M1, M2 | **400–600** |
| **M6** | Docs + curation: README source table and the mod.io attribution/ToS note, `docs/security.md` key handling, man pages (`make man`), and an SE1 known-games entry carrying `modio` alongside `steamworkshop` (folds into #406 S1). | M1–M5 | **250–400** |

Parallelism: **M1 alone first** (it owns the package, the key model and the platform header).
Then **M2 ∥ M4** — they share only `Capabilities()` and the package doc. **M3/M3′** after M1
and M0. M5 after M2. M6 last.

Realistic total if M3 lands: **≈ 4000–5700 lines** across six units, in the same shape and
about the same size as #269's W1–W3. If M3′ is needed instead: **≈ 4500–6300**.

Gates for every unit, per house rules: `make test` / `make test-race`, `go vet`,
`trunk check`, every ratchet in the CLAUDE.md testing table (JSON goldens ×4 packages, doc
comments ×4, `details_coverage_test.go`, the SPA colour/contrast ratchets, module-graph
resolution, both boundary tests), `make man` in any unit that touches command help, and
**no test may reach `api.mod.io`** — the same `TestNoTestReachesTheProductionAPI` guard the
`steamworkshop` package already carries.

---

## 6. Open questions, each with a recommendation

**Q0 — Does a `modio` source belong on the 2.0 bar at all?**
*Recommend: put it to the owner as a defer candidate.* The facts: SE2 (the game that opened
this spike) is a Steam Workshop game already covered by the shipped source; SE1 is the only
mod.io game on the machine and cannot be *deployed to* by lmm through any channel (§4.4), so
the deliverable is tracking and update notices for one game whose mods are mostly reachable
through `steamworkshop` anyway. Against that: ~4000–5700 lines and a new contractual
obligation (§2.2). **Scope is the owner's call — nothing here has been labelled or deferred.**
If the owner wants mod.io for reach beyond today's library (it is the default UGC backend for
a large slice of console-and-PC titles), that is a perfectly good reason and the plan above
stands as written.

**Q1 — Where does Space Engineers put a mod.io mod on disk, and does that store name the mod
id and the installed modfile id?** *Recommend: probe before M3 is written.* A read-only look
under the SE Proton prefix — `…/steamapps/compatdata/244850/pfx/drive_c/users/steamuser/AppData/Roaming/SpaceEngineers/`
(and `…/AppData/Local/mod.io/` for a `globalsettings.json`) — after subscribing to one mod.io
mod in-game. Three outcomes: (a) an SDK-style `state.json` tree → M3 as scoped; (b) a
custom-but-readable layout → M3, slightly larger; (c) nothing readable → M3′ (OAuth) or drop
Tier 1. A worker must not run this; it reads the owner's machine.

**Q2 — How does a game declare which mod.io platform it targets?**
*Recommend: default `windows`, override per game.* Windows-under-Proton is the common case and
matches mod.io's own default, so the zero-config answer is right most of the time. The override
belongs on the game (it is a property of how the game runs), not on the source. Concretely: an
optional `platform:` key on the games.yaml game entry, read only by sources that care. Also
send `X-Modio-Portal: steam` for a Steam-installed game.

**Q3 — How does lmm satisfy ToS §3.3 (branding) and stay clear of §3.5 (no competing
product)?** *Recommend: text attribution everywhere mod.io data appears, and a hard no on
catalogue mirroring.* "via mod.io" on CLI mod output, a `mod.io` label plus a link to
`profile_url` on SPA rows and the mod page, mod.io named in `lmm source list`. And a written
constraint carried into M1's package doc: response caching is short-TTL only, no bulk
catalogue sync, no local mirror, no bypassing mod.io's own pages. If the coordinator wants
certainty rather than a good-faith reading, mod.io publishes `developers@mod.io` for exactly
this question — one email, before M1 starts.

**Q4 — Does `binary_url` need the `api_key` on the download hop?** *Recommend: send nothing,
handle both.* The reference describes it as pre-signed and time-limited. If a live test shows
otherwise, `source.DownloadHeaderProvider` already exists for exactly this (it scopes
credentials to the right origin) — no design change either way. Confirmed only by a real
download, i.e. during M4's smoke.

**Q5 — Is `GET …/dependencies` recursive?** *Recommend: assume flattened, verify at M2.* The
object carries `dependency_depth`, which strongly implies a flattened tree; the reference page
did not confirm a `recursive` filter. `core.DependencyResolver` already handles both (it does
its own ordering and cycle detection), so the risk is a redundant round trip, not a wrong
answer.

**Q6 — One collection flag per source, or one generalised flag?**
*Recommend: generalise.* `lmm profile import --workshop-collection` was correct when there was
one `CollectionResolver`; a second one makes the pattern `--collection <source>:<ref>`, with
the old flag kept as an alias. Cheap now, and it stops a third source adding a third flag.
This touches shipped W2 surface, so it is the owner's call whether it happens in M5 or
separately.

---

## Appendix — constraints any implementation inherits

- **Never bundle, share or embed a mod.io API key** (ToS §3.2). Every user brings their own
  from <https://mod.io/me/access>, exactly as the Steam Web API key works today.
- **Never store a mod.io password.** The only credentials lmm touches are a personal API key
  and (if M3′ happens) an OAuth token the user consented to.
- **Never mirror, rehost or bulk-sync mod.io's catalogue or its files** (ToS §3.5).
- **Always attribute mod.io** wherever its data is displayed (ToS §3.3).
- **Never exceed the rate budget:** honour `retry-after`, back off on 429/5xx, batch with
  `id-in`/`_limit=100`, and cache. 60 req/min is the personal-key ceiling.
- **Never reach `api.mod.io` from a test.** Fixtures + `httptest`, with a package test proving
  no test names the production base URL — the `steamworkshop` precedent.
- **Never read the owner's machine or run a keyed call from a worker.** The probes in Q1/Q4
  are owner or coordinator work.

---

## Sources

- [mod.io API v1 reference](https://docs.mod.io/restapiref/) — base URL, authentication, rate
  limits, pagination, Mod/Modfile/Dependency/Collection objects, platform headers, email auth.
- [mod.io Terms & User Consent](https://docs.mod.io/terms) — consent requirements,
  `terms_agreed`, `error_ref` 11074.
- [mod.io API Access Terms](https://mod.io/legal/api) — §1.1, §3.2, §3.3, §3.4, §3.5.
- [Modding in Space Engineers 2 — Keen Support](https://support.keenswh.com/spaceengineers2/pc/topic/45156-modding-in-space-engineers-2)
  and [Marek Rosa dev blog, 2025-01](https://blog.marekrosa.org/2025/01/space-engineers-2-steam-workshop-support/)
  — SE2 uses Steam Workshop.
- [Space Engineers: Community Crossplay](https://www.spaceengineersgame.com/space-engineers-community-crossplay/)
  — SE1 mod.io integration (Update 197.1).
- [mod.io/g/spaceengineers](https://mod.io/g/spaceengineers) ·
  [mod.io Support: Space Engineers](https://support.mod.io/hc/en-us/sections/9171643544591-Space-Engineers).
- [BisectHosting: mods on a Space Engineers server](https://www.bisecthosting.com/clients/index.php?rp=%2Fknowledgebase%2F565%2FHow-to-install-mods-on-a-Space-Engineers-server.html)
  — `PublishedServiceName` distinguishes Steam from mod.io.
- [mod.io Support: changing the mod install location](https://support.mod.io/hc/en-us/articles/9809372059407-How-do-I-change-the-install-location-for-mods-from-mod-io)
  and [@modiohq](https://x.com/modiohq/status/1588332358469836801) — `globalsettings.json`,
  `RootLocalStoragePath`, per-mod `state.json` (**reported, not verified**).
- [Subnautica 2 Help Center: mod support](https://support.subnautica.com/hc/en-us/articles/57020140859929-Will-there-be-any-mod-support)
  · [Cubic Odyssey mod support thread](https://steamcommunity.com/app/3400000/discussions/0/567001570938457027/)
  · [Human Host Workshop](https://steamcommunity.com/app/2393970/workshop/)
  · [StarRupture mod loader on Nexus](https://www.nexusmods.com/starrupture/mods/89) — §1.3.
- In-tree: `internal/source/source.go` (`Fetcher`, `WorkshopScanner`, `CollectionResolver`,
  `ExactFileSizer`, `DownloadHeaderProvider`, `EnvKeyProvider`, `KeyValidator`),
  `internal/core/service.go:1012`, `internal/core/downloader.go:149-278`,
  `internal/app/sources.go`, `docs/plans/2026-09-09-steam-workshop-design.md`.
