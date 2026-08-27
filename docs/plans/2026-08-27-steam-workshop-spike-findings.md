# Steam Workshop Spike Findings (#268)

**Date:** 2026-08-27 · **Status:** Complete · **Verdict: PARTIAL GO, tiered**

Spike question: which Steam Workshop capabilities could lmm deliver as a `ModSource`,
through what mechanism, without violating Steam's ToS? All API and steamcmd behavior
below was **live-tested 2026-08-27** on this machine unless marked otherwise; policy
claims come from sourced web research (agent report archived in issue #268).

## Capability verdicts

| # | Capability | Verdict | Mechanism |
|---|------------|---------|-----------|
| 1 | Adopt already-subscribed items | **GO** | Parse `steamapps/workshop/appworkshop_<appid>.acf` (plain VDF) + `workshop/content/<appid>/<fileid>/` |
| 2 | Per-item metadata | **GO** | Keyless POST `ISteamRemoteStorage/GetPublishedFileDetails/v1` (also `GetCollectionDetails` for collections) |
| 3 | Update checking | **GO** | ACF `timeupdated` == API `time_updated` AND ACF `manifest` == API `hcontent_file` (two exact signals, verified equal on live items) |
| 4 | Search / browse | **PARTIAL** | `IPublishedFileService/QueryFiles/v1` — hard 403 without a user-supplied (free) Steam Web API key; same bring-your-own-key model as NexusMods |
| 5 | Download, legacy items | **GO** | Unauthenticated GET of `file_url` (populated only on old UGC-era items) |
| 6 | Download, modern items | **PARTIAL** | `steamcmd +login anonymous +workshop_download_item` — works only where the publisher opted in; not predictable from metadata; account login is the fallback |
| 7 | Manage subscriptions (sub/unsub) | **NO-GO** | Requires a Steam client session, not a Web API key. Stays in the Steam client. |

## Empirical evidence

### Local adoption (capability 1)

- This machine: 6 `appworkshop_*.acf` files across libraries; real content for
  Space Engineers 2 (1133870, ~30 items, 916 MB) and Human Host (2393970, 40 MB).
- ACF structure: `AppWorkshop { appid, SizeOnDisk, NeedsUpdate, NeedsDownload,
  WorkshopItemsInstalled { <fileid> { size, timeupdated, manifest } }, WorkshopItemDetails {...} }`.
  Games with no workshop use have empty-stub ACFs (`WorkshopItemsInstalled {}`) — parse cleanly.
- Format is the same VDF dialect `internal/source/steam/vdf.go` already parses.

### Keyless metadata + update signals (capabilities 2, 3)

- `GetPublishedFileDetails` keyless POST → HTTP 200 with title, description,
  time_created/updated, tags, subscriptions, preview_url, file_size, file_url, hcontent_file.
- Exact match verified on live SE2 items: API `time_updated` 1764767935 == ACF `timeupdated`;
  API `hcontent_file` 7987119735124793734 == ACF `manifest`. `hcontent_file` is the stronger
  signal (content identity; timestamps can theoretically move without content change).
- Dead/delisted items return `result: 9` (file-not-found) while still appearing on scraped
  browse pages — treat `result != 1` per item, not per response.
- No documented rate limit for keyless calls; Valve reserves the right to change the API
  without notice → caching and backoff are mandatory, not optional.

### Search (capability 4)

- Keyless `QueryFiles` → HTTP 403 "Please verify your key= parameter". Live-tested.
- Web API ToU: keys are personal, confidential, 100k calls/day. **Embedding a shared key in
  a distributed app violates the ToU** — each user must bring their own key
  (free at steamcommunity.com/dev/apikey). Precedent: Playnite, ArchiSteamFarm. This is an
  interpretation-plus-precedent posture, not an explicit grant, and it mirrors lmm's
  existing NexusMods key handling.

### Downloads (capabilities 5, 6)

- **Legacy:** Skyrim 2012 item 68336872 → `file_url` populated; unauthenticated GET returned
  the full file, size exactly matching API `file_size` (572,330 B). The CDN rejects HEAD
  (404) — use GET. Modern items (2019+ even for the same game) have empty `file_url`.
- **steamcmd anonymous, live matrix (2026-08-27):**
  - Space Engineers 2 (1133870): **success** (verified in fully isolated HOME too)
  - Space Engineers 1 (244850): **success**
  - RimWorld (294100): **success**
  - Wallpaper Engine (431960): **fail** — `ERROR! Download item ... failed (Failure).`
- Anonymous availability is a per-app Steamworks opt-in ("Enable anonymous game servers to
  download workshop items") plus visibility to the anonymous account (SteamDB sub/17906 is
  the community index). Not detectable from item metadata (the refused Wallpaper Engine item
  returns full metadata) → design must be **probe-and-report**, with account login as the
  documented fallback for refused apps.
- Error taxonomy observed: disallowed app → `(Failure)`; invalid/delisted item → `(Access Denied)`.
- `+force_install_dir <dir>` **does** control workshop item placement in the current
  steamcmd (items land in `<dir>/steamapps/workshop/content/<appid>/<fileid>/`) —
  supersedes older reports (steam-for-linux #11662) that it was unreliable.
- **Trap:** without `force_install_dir`, steamcmd run under the user's real HOME found the
  existing Steam install and wrote directly into the real library's workshop content dir.
  Any lmm integration must always pin `force_install_dir` (and ideally HOME).
- Footprint: steamcmd self-bootstraps to ~200 MB. It should be an **optional, user-installed
  shell-out dependency** (probed at runtime like other optional tools), never vendored.

## ToS / risk posture (safest → riskiest, sourced in issue #268)

1. **Reading locally-subscribed content — safest.** Pure local reads of data Steam already
   delivered; exactly what Vortex and the incumbent ecosystem do (their entire Workshop
   posture is "read what Steam downloaded, let Steam manage subscriptions").
2. **Keyless metadata — very safe.** Consistent with official per-method docs (no `key`
   parameter listed); risks are operational (undocumented limits, changeable without notice).
3. **User-supplied API key for search — moderate.** Strong precedent, not an explicit grant.
   Store like a credential; never embed a shared key.
4. **steamcmd downloads — riskiest, but categorically unlike the 2022
   steamworkshopdownloader.io takedown.** That takedown targeted *redistribution to
   non-owners* ("Valve has requested that we stop retrieving and redistributing content").
   Per-user fetch from Valve's own CDN with the user's own entitlements is the model
   DepotDownloader has run publicly for ~15 years without action (last push 2026-08-24).
   No source shows Valve sanctioning it; none shows Valve blessing it. Residual uncertainty
   acknowledged. Mitigations: user's own steamcmd + cached sessions, never store passwords,
   never rehost or redistribute downloaded content.

## What lmm must NOT do

- Embed or share a Steam Web API key.
- Rehost, redistribute, or proxy workshop content to other users.
- Store Steam account passwords (account-login fallback uses steamcmd's own cached session).
- Attempt subscription management (needs a Steam client session; NO-GO).

## Recommended shape (if/when built — feature work is a separate decision + issue)

Tiered `ModSource`, each tier independently useful, shipped in order of risk:

1. **Tier 1 — adopt + track:** discover subscribed items from ACFs (existing VDF parser),
   surface them as installed mods, check updates via keyless `GetPublishedFileDetails`
   (`hcontent_file` primary, `time_updated` secondary). Zero auth, incumbent-standard.
2. **Tier 2 — search:** `QueryFiles` behind a user-supplied API key using the existing
   auth-token store; collections via keyless `GetCollectionDetails`.
3. **Tier 3 — download:** legacy `file_url` GET when populated; else optional steamcmd
   shell-out with pinned `force_install_dir`, anonymous first, per-app probe-and-report,
   account-session fallback. Gate behind steamcmd presence.

Notable scope fact: none of the user's actively-modded games overlap — Icarus has no
Workshop — so demand is driven by Workshop-native games (SE1/SE2 etc.).

## Throwaway artifacts

All probe artifacts (scratchpad steamcmd, isolated HOME, downloaded test items) are
session-scratchpad-only and auto-cleaned. No spike code was written; nothing to keep.
The first steamcmd run re-downloaded already-installed SE2 item 3617086610 into the real
library (byte-identical, same manifest; verified intact).
