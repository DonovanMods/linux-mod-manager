# Collections, FOMOD and plugin order — spike findings (#597, #354, #355)

**Date:** 2026-11-10 · **Feeds:** [2026-11-10-collections-design.md](2026-11-10-collections-design.md)

Raw findings from the three external-format spikes that the design rests on.
Every claim carries its own tag: **VERIFIED** (read in the cited source, or run
live against the API this session), **INFERRED** (reasoning from verified
facts), or **UNVERIFIED** (not checked). Nothing here was tested with a Nexus
API key; the items that need one are listed at the end of Part 1.

Sources were read, never copied: Vortex (`github.com/Nexus-Mods/Vortex`) and
its `fomod-installer` are GPL-3.0, as are libloot, libloadorder and esplugin.
lmm is MIT. Implementations must be written from observed behaviour and
published documentation, not ported.

- Part 1 — NexusMods Collections: the GraphQL API, the collection archive and `collection.json`
- Part 2 — FOMOD
- Part 3 — Plugin load order and LOOT

---

## Part 1 — NexusMods Collections

Date 2026-11-10. Sources: live anonymous GraphQL introspection of https://api.nexusmods.com/v2/graphql (V-live);
Vortex master, shallow clone (V-src = github.com/Nexus-Mods/Vortex, paths under src/renderer/src/extensions/);
node-nexus-api (github.com/Nexus-Mods/node-nexus-api, V-nna); lmm repo.

### 1. GraphQL v2

- VERIFIED (V-live): the endpoint answers ANONYMOUSLY (no `apikey`) for introspection AND for collection data. An anonymous
  `collectionsV2`, `collection` and `collectionRevision` all worked, including `modFiles`, `externalResources` and `downloadLink`.
  An API key is not needed to read a collection's file list. (Whether anonymous access is intended/stable is UNVERIFIED; send `apikey` anyway, as lmm does.)
- VERIFIED (V-live) Query root fields:
  - `collection(slug: String, viewAdultContent: Boolean, domainName: String): Collection`
  - `collectionRevision(slug: String, revision: Int, viewAdultContent: Boolean, domainName: String): CollectionRevision`
  - `collectionsV2(facets, viewUserBlockedContent, filter: CollectionsSearchFilter, postFilter, sort, offset, count)`; `collectionGames`
  - `modFiles(modId: ID, gameId: ID)`
  - Unknown slug -> `{"errors":[{"message":"Collection not found", "extensions":{"code":"NOT_FOUND"}}],"data":null}`.
- VERIFIED (V-live) `Collection` includes: id:Int!, slug (via collectionsV2 node; present), name, description, game:Game!, gameId,
  latestPublishedRevision:CollectionRevision, draftRevisionNumber, collectionStatus:CollectionStatus, firstPublishedAt, lastPublishedAt,
  endorsements, category, metadata, collectionSchemaId, discardedAt/discardReason.
- VERIFIED (V-live) `CollectionRevision` fields: id:Int!, revisionNumber:Int!, latest:Boolean!, status:String!, revisionStatus:String!,
  modCount:Int!, modFiles:[CollectionRevisionMod!]!, externalResources:[ExternalResource!]!, downloadLink:String!,
  installationInfo:String, adultContent:Boolean!, gameVersions:[GameVersion!], totalSize:BigInt!, assetsSizeBytes:BigInt!,
  collectionSchema/collectionSchemaId, collectionChangelog, collection, collectionId, contentPreviewLink, createdAt/updatedAt,
  discardedAt, retractionReason, modAuthors(connection), metadata, totalDownloads, uniqueDownloads.
- VERIFIED (V-live) `CollectionRevisionMod`: id:ID!, fileId:Int!, gameId:Int!, collectionRevisionId:Int!, optional:Boolean!,
  updatePolicy:String! (observed "prefer"), version:String!, file:ModFile (nullable).
- VERIFIED (V-live) `ModFile`: fileId, modId, name, uri (the archive FILE NAME, e.g. `The Great Town...-37471-2-1-1633208979.7z`),
  version, size:Int (KB), sizeInBytes:BigInt (string in JSON), category/categoryId, primary, date, description, game, mod:Mod,
  scannedV2, uid, sqid, ... `Mod`: modId, name, game{domainName}, gameId, adultContent, version, status, author, supportsVortex, ...
- VERIFIED (V-live) `ExternalResource`: id, name, resourceType:String!, resourceUrl, fileExpression:String!, optional:Boolean!, version, author, collectionRevisionId.
- NOTE: `ModFile.md5`/hash is NOT exposed by the type listing (no md5 field seen). Collection.json carries md5/fileSize instead (see 2).
- Working query (VERIFIED, ran anonymously against slug `rqhcxy`, rev 1, 12 mods):
  ```
  { collectionRevision(slug:"rqhcxy", revision:1, viewAdultContent:true, domainName:"skyrimspecialedition") {
      revisionNumber latest status modCount downloadLink installationInfo adultContent
      externalResources{name resourceType resourceUrl fileExpression optional}
      modFiles{fileId optional updatePolicy version
        file{name uri size sizeInBytes version mod{modId name game{domainName}}}} } }
  ```
  Result: `downloadLink` = `"/v2/collections/335/revisions/467/download_link"` (a PATH, relative to https://api.nexusmods.com), installationInfo null.
- Latest revision: `collection(slug){ latestPublishedRevision{ revisionNumber } }` (VERIFIED works anonymously), or omit `revision` arg on
  collectionRevision (INFERRED: Vortex passes `undefined` when revisionNumber<=0, V-src nexus_integration/eventHandlers.ts onGetNexusCollectionRevision,
  so omission = latest; not run live).
- `viewAdultContent: true` is an arg on both queries; adult collections presumably need it (INFERRED from arg + `adultContent` field; not tested with an adult collection). Account-level adult-content settings may further gate (UNVERIFIED).
- URL forms: https://www.nexusmods.com/games/<domain>/collections/<slug> (task description; slug is what the query takes). Old next.nexusmods.com/<game>/collections/<slug> format: UNVERIFIED (not fetched; parse last path segment after `/collections/` as the slug, domain from the segment before... treat as INFERRED). A revision may be in the URL (`/revisions/<n>`) - UNVERIFIED.
- Slug example confirmed: `rqhcxy` (The Great Cities Collection, skyrimspecialedition).

### 2. Collection archive and collection.json

- VERIFIED (V-src collections/eventHandlers.ts ~L70-100; V-nna src/Nexus.ts L1209): Vortex takes `latest.downloadLink` (the path above),
  calls `GET https://api.nexusmods.com + downloadLink` through node-nexus-api (`getCollectionDownloadLink`), reads `res.download_links ?? [res.download_link]`
  (items `{name, short_name, URI}` = IDownloadURL), then starts a normal download of `URI`s named `<collection name>-rev<N>.7z`.
  So the archive is a 7z file.
- VERIFIED (V-live): that URL anonymously returns HTTP 401 `{"message":"Please provide an authentication method"}`. So it needs the `apikey` header (or OAuth bearer).
- UNVERIFIED: whether a free account's key gets 200 here. Evidence for yes (INFERRED): Vortex nxmProtocol.ts says "A free user can't ask the api for a download link, so only a keyless link to a mod file ... can ever need the website round trip. Collections, extensions from the site domain, and links the website already authorised go straight to the api." i.e. collection downloads are NOT in the premium-gated website-round-trip path. No key/expires is passed for them. Needs one real test with a free key.
- VERIFIED (V-src collections/makeInstall.ts): the archive root has `collection.json` (read from destinationPath/collection.json), plus `bundled/` (BUNDLED_PATH, constants.ts) files and `patches/` (PATCHES_PATH) ; other top-level files are copied as-is.
- VERIFIED (V-src collections/types/ICollection.ts, quoted):
  ```ts
  interface ICollectionInfo { author; authorUrl; name; description; installInstructions; domainName: string; gameVersions?: string[] }
  type UpdatePolicy = "exact" | "latest" | "prefer";
  type SourceType = "browse" | "manual" | "direct" | "nexus" | "bundle";
  interface ICollectionSourceInfo { type: SourceType; url?; instructions?; modId?: number; fileId?: number; updatePolicy?: UpdatePolicy;
     adultContent?: boolean; md5?: string; fileSize?: number; logicalFilename?: string; fileExpression?: string; tag?: string }
  interface ICollectionModDetails { type?: string; category?: string }
  interface ICollectionMod { name; version; optional: boolean; domainName; source: ICollectionSourceInfo; hashes?: IFileListItem[];
     choices?: IChoiceType; patches?: IModPatches; instructions?; author?; details?: ICollectionModDetails; phase?: number; fileOverrides?: string[] }
  type RuleType = "before"|"after"|"requires"|"conflicts"|"recommends"|"provides";
  interface ICollectionModRule { source: IModReference; type: RuleType; reference: IModReference }
  interface ICollectionTool { name; exe; args: string[]; cwd; env; shell: boolean; detach: boolean; onStart: "hide"|"hide_recover"|"close" }
  interface ICollection extends Partial<ICollectionGamebryo> { info; mods: ICollectionMod[]; modRules: ICollectionModRule[]; collectionConfig?: ICollectionConfig }
  ```
  (`tools` is defined as a type but is NOT a field of ICollection in this version; so no tools list in collection.json - VERIFIED by absence in that file.)
- VERIFIED (V-src mod_management/types/IMod.ts): `IFileListItem {path; md5? (hex); xxh64? (base64)}`; `IModReference extends IReference` (modmeta-db: fields like fileExpression, fileMD5, logicalFileName, versionMatch, tag, repo{gameId,modId,fileId} - the exact IReference fields UNVERIFIED, lives in an npm package not cloned; Vortex code uses `versionMatch`, `logicalFileName`, `fileExpression`, `md5Hint`, `id`, `idHint`, `tag`).
- VERIFIED (V-src installer_fomod_shared/types/interface.ts) FOMOD choices: `IChoiceType = { type: string; options: IChoices }`,
  `IChoices = { name: string; groups: { name: string; choices: { name: string; idx: number }[] }[] }[] | undefined` (steps -> groups -> choices by name and index). `type` is "fomod".
- VERIFIED (V-src collections/util/gameSupport/gamebryo.tsx): Gamebryo collections add top-level `plugins?: {name: string; enabled?: boolean}[]` (absent in drag-and-drop load-order mode) and
  `pluginRules: IGamebryoRules` = `{ plugins: IUserlistEntry[] (LOOT userlist entries with `after`/`group`...), groups }`. `toLootType` maps requires->"req", incompatible->"inc", else "after". Exact IGamebryoRules/IUserlistEntry shape: UNVERIFIED (not read).
- VERIFIED: `collectionConfig: { recommendNewProfile: boolean; excludePluginRules: boolean; referenceTagScheme?: string }` (collections/types/ICollectionConfig.ts).
- VERIFIED (transformCollection.ts L318, L322): `optional` -> rule type "recommends" else "requires"; `phase` default 0. Phases = install ordering groups (a mod in phase N installs after all lower phases) - semantics INFERRED from name.
- `fileOverrides: string[]` on a mod: file paths this mod's version wins on conflicts (VERIFIED type; use INFERRED).
- Non-game-specific load order (file_based_loadorder, e.g. BG3/Cyberpunk mod types) has its own `ICollectionMod`-keyed types at extensions/file_based_loadorder (not cloned): UNVERIFIED.
- ICollectionAttributes (local Vortex state, NOT collection.json): per-mod `instructions`, `source`, `installMode`, `saveEdits`, `fileOverrides`.

### 3. Free vs Premium

- VERIFIED (V-src nexus_integration/eventHandlers.ts L368-377): Vortex's programmatic `downloadFile` rejects with "Only available to premium users"; code comment: "nexusmods can't let users download files directly from client, without showing ads".
- VERIFIED (V-src nxmProtocol.ts L75-80): a free user "can't ask the api for a download link"; a mod-file nxm link without `key` (+`expires`, parsed at nxmProtocol.ts ~L509) needs a "website round trip". Flow: Vortex opens the file's page, user clicks "Slow download", the website fires `nxm://<game>/mods/<id>/files/<id>?key=...&expires=...`, Vortex's handler then uses the key to get the CDN link. (Flow shape VERIFIED from code names/comments; website UI wording INFERRED.)
- VERIFIED: Vortex now also uses a v3 API (`@vortex/nexus-api-v3`, `getModFileVersionDownloadLink(gameDomain, fileVersionId, {collectionId, applicationName})`, comment: "for a user who's eligible without a website key, as Premium users are"). Passing `collectionId` records the download as collection-originated. The REST shape of v3 is UNVERIFIED (package not cloned). Check it before building: lmm uses v1 `download_link.json`.
- Premium collection installs: Vortex downloads all mods automatically; free users get per-mod prompts (V-src InstallDriver.ts L305 mentions "free user ... skipped download"). Details INFERRED.
- ToS: UNVERIFIED for the specific "automation of free downloads is forbidden" claim. What I could fetch (https://help.nexusmods.com/article/114-api-acceptable-use-policy) only says: no scraping ("Fetching data en-masse with the intent to rehost"), no "Storing user API keys on your own server and/or using them without the action being initiated by the user", and `Application-Name` must be accurate. It does not mention free vs Premium. The claim in #597 remains plausible but I could not source it; cite it as a project policy decision, not a quoted rule. Rate limits (https://help.nexusmods.com/article/105): 20,000 req / 24h then 500/h.

### 4. Binary patches

- VERIFIED (V-src collections/util/bsdiff.ts, binaryPatching.ts, constants.ts): format is BSDIFF40 (header "BSDIFF40"; classic bsdiff, bzip2-compressed blocks). Vortex also ships an HDiffPatch wasm (scripts/README.md mentions `hdiffz.wasm` for src/main/src/bsdiff) - whether new collections can contain HDiffPatch patches: UNVERIFIED; check the main/src/bsdiff/patch.ts sniffing before relying on BSDIFF40 only.
- VERIFIED: in collection.json, mod `patches: { [filePath]: string }` = file path inside the mod -> CRC32 (uppercase hex, `fileCRC32`) of the BASELINE (unpatched) file; applyPatches only patches when the file's CRC equals that value, else warns "patch not applied because reference CRC differs". (IMod.ts calls it "hash of the baseline file".)
- VERIFIED: patch files live in the collection archive at `patches/<modName>/<filePath>.diff` (PATCHES_PATH="patches"). Constants: MAX_PATCH_SIZE=0.2 (patch must be <=20% of source), PATCH_OVERHEAD=130 bytes.
- Bundled mods: `bundled/` dir in the archive; source.type "bundle" (md5 not recorded for bundle since stored uncompressed).

### 5. Revisions / updates / optional

- VERIFIED (V-live): each revision lists `modFiles[]` with exact `fileId` plus `updatePolicy` ("prefer" observed). Revision numbers are per-collection ints; `latest` boolean; `collection.latestPublishedRevision`.
- VERIFIED (V-src transformCollection.ts L105-130): updatePolicy absent -> bundle: "exact"; else derived from the version matcher: "*" -> "latest", "+prefer" suffix -> "prefer", else "exact". Semantics: exact = that fileId only; latest = newest file of the mod; prefer = the pinned file, but a newer one is acceptable (INFERRED from names; Vortex ICollectionSourceInfo comment: "determines which file to get if there is an update compared to what's in the mod pack").
- VERIFIED (V-src collections/util/collectionUpdate.ts): updating an installed collection to a new revision installs the new collection mod, then computes "obsolete members": dependency members (installedAsDependency) of the old revision not referenced by the new revision's requires/recommends rules nor by any other mod, and shows a review dialog to remove/keep them.
- optional: `CollectionRevisionMod.optional` / `mods[].optional` -> "recommends" rule; Vortex asks the user whether to install recommended mods (INFERRED from will-install-dependencies recommendations flag).

### 6. lmm's nexusmods source today (VERIFIED, read code)

- internal/source/nexusmods/client.go: REST base `https://api.nexusmods.com` (v1 paths), GraphQL `https://api.nexusmods.com/v2/graphql` (plain net/http POST with `graphqlRequest{Query,Variables}`, header `apikey: <key>`; used by SearchMods and GetModRequirements).
  Auth = API key only (`AuthHeader: "apikey"`); `ExchangeToken` returns an error ("uses API key authentication, not OAuth") though oauth URL consts exist.
- Download links: `GetDownloadLinks` -> v1 `/v1/games/{domain}/mods/{id}/files/{fileId}/download_link.json`, returns `[]DownloadLink{Name,ShortName,URI}`. No premium check or key/expires handling found in nexusmods.go/client.go (grep for premium/403/nxm hit nothing there); a free account hits the API's refusal. (#543 manual-only flow is the existing fallback per #597 text.)
- Types: types.go has REST v1 structs only (ModData, FileData, ModFileList, FileUpdate, UpdateEntry, DownloadLink); no collection types.
- schema_contract_test.go pins a recorded introspection of ModsFilter (testdata/modsfilter-schema.json) with httptest; a collection query should get the same treatment (record Collection/CollectionRevision/CollectionRevisionMod schema).
- source.CollectionResolver (internal/source/source.go L220-234): `ResolveCollection(ctx, ref string) (Collection{ID, Name, URL, ItemIDs []string}, error)`. It is item-ID-list shaped (Workshop); a Nexus collection needs more per item (modId, fileId, optional, updatePolicy, version, and file names) so `ItemIDs` is too thin. Either extend Collection with structured items or add a second optional interface. (Design observation.)
- Extending: yes - the existing GraphQL path is generic enough; add one `collectionRevision` query (works with the same `apikey` header; works unauthenticated too). Resolving the collection.json (FOMOD choices, plugins, patches, rules) additionally needs: GET `https://api.nexusmods.com/v2/collections/{id}/revisions/{id}/download_link` with apikey, download the .7z, extract (lmm already has 7z tooling per extractor.go), parse collection.json.

### Not confirmed / to test with a real key

1. Free-account 200/403 on collection `download_link`; whether `download_links` has expiry (signed CDN URL; assume short-lived).
2. v3 download-link API shape and its free-user eligibility.
3. Quoted Nexus ToS text on automating free downloads.
4. IReference (modmeta-db) field list; IGamebryoRules/IUserlistEntry; file_based_loadorder collection types.
5. Adult-content behaviour; old next.nexusmods.com URL handling; whether collection.json `mods[].source.fileId` always equals GraphQL modFiles[].fileId (very likely; not diffed).

---

## Part 2 — FOMOD

Sources (shallow clones, read only):

- `fomod-installer` = github.com/Nexus-Mods/fomod-installer (Vortex's installer, C#, GPL-3.0, HEAD 2026-09-23). Cited as FI.
- `vortex` = github.com/Nexus-Mods/Vortex (sparse). Cited as VX.
- `modorganizer-installer_fomod` (C++ XML installer) and `modorganizer-installer_fomod_csharp` (C# script installer). Cited as MO2.
- `go-fomod` = github.com/Rethunk-Tech/go-fomod (Go). Cited as GOFOMOD.

Tags: VERIFIED (source read), INFERRED (my reasoning), UNVERIFIED (not checked).

### 1. Format

#### Files

- `fomod/ModuleConfig.xml` (required) and `fomod/info.xml` (optional metadata). VERIFIED (MO2 `isArchiveSupported` requires `ModuleConfig.xml`; MO2 `findFomodDirectory` descends through a single top-level dir; FI `XmlScriptType.ListFileNames = {script.xml, ModuleConfig.xml}`).
- `info.xml` is display-only (Name, Author, Version, Website, Description). It has no install semantics. INFERRED (neither installer's resolution reads it; MO2 only references it at installerfomod.cpp:146 for the file tree).
- Vortex's XSD set is `XmlScript1.0..5.0.xsd`, in `FI/src/InstallScripting/XmlScript/Schemas/`. The version is picked by a regex over `xsi:noNamespaceSchemaLocation="...ModConfigN.0.xsd"`, and a missing match defaults to 1.0. VERIFIED (FI `XmlScriptType.GetXmlScriptVersion`). Parsers exist per version (Parser10..Parser50).

#### ModuleConfig schema (5.0 XSD, VERIFIED from `XmlScript5.0.xsd`)

Root `<config>`, a sequence:

1. `moduleName` (text, attrs `position`, `colour`).
2. `moduleImage?` (`path`, `showImage`, `showFade`, `height`).
3. `moduleDependencies?` (compositeDependency, i.e. prerequisites. If unmet Vortex returns a fatal "Installer Prerequisits not fulfilled").
4. `requiredInstallFiles?` (fileList).
5. `installSteps?` (stepList, attr `order` Ascending (default) / Descending / Explicit).
6. `conditionalFileInstalls?` -> `patterns` -> `pattern`+ each `{dependencies, files}`.

installStep: `name` attr, optional `<visible>` (compositeDependency), `optionalFileGroups` (groupList, with `order`).
group: `name`, `type` in {SelectAtLeastOne, SelectAtMostOne, SelectExactlyOne, SelectAll, SelectAny}, `plugins` (pluginList, with `order`).
plugin: `name` attr; `description`, `image?`, then `files` and/or `conditionFlags` (either order, both optional), then required `typeDescriptor`.

- `conditionFlags` -> `<flag name="X">value</flag>`+.
- typeDescriptor is a choice of `<type name="Required|Optional|Recommended|NotUsable|CouldBeUsable"/>` or `<dependencyType>` = `defaultType` + `patterns` -> `pattern`+ each `{dependencies, type}`. The first matching pattern wins. INFERRED (resolver order not read in FI `ConditionalOptionTypeResolver`; GOFOMOD `PluginType` is the place to confirm).

fileList: any mix of `<file>` and `<folder>`, each a fileSystemItem with attrs `source` (required), `destination` (optional), `alwaysInstall` (bool, default false), `installIfUsable` (bool, default false), `priority` (int, default 0).

Dependencies (compositeDependency, attr `operator` And (default) | Or, nestable via `<dependencies>`) contain:

- `fileDependency file= state=Missing|Inactive|Active`
- `flagDependency flag= value=`
- `gameDependency version=`
- `fommDependency version=`
- 5.0 adds `foseDependency`, `skseDependency`, `nvseDependency`, `f4seDependency` (all `versionDependency`).
- The xsd text declares `version` as a string. A bad version parses to 0.0.0.0 in Vortex (`Parser.ParseVersion` catch).

Orders: `Ascending`/`Descending` sort steps, groups and plugins by name. `Explicit` keeps the document order. Missing attribute means Ascending in the XSD, but FI `ParseSortOrder` maps empty to Explicit. VERIFIED, and a discrepancy. GOFOMOD says "already sorted in declared order".

Types: Required = forced on and cannot be unchecked. Recommended = preselected. NotUsable = forced off. CouldBeUsable = selectable with a warning. Optional = default off. VERIFIED (FI `fixSelected`, `preselectOptions`).

#### Encoding / path quirks (all VERIFIED unless noted)

- Real files are UTF-8 (with and without BOM), UTF-16 LE/BE with BOM, and also UTF-16 without a BOM (MO2 sniffs `3C 00 3F 00`). GOFOMOD also decodes Windows-1252. Declarations are often wrong:
  - Vortex strips `encoding="..."` from the XML header before validating (FI `XmlScriptType.StringStream`: "usually wrong anyway") and re-encodes as UTF-16.
  - MO2 `skipXmlHeader` sniffs BOM and bytes, then discards the declaration.
  - GOFOMOD says encoding "from the bytes, ignoring the declaration".
- Paths are case-insensitive. FI `TextUtil.NormalizePath(... toLower=true)` lowercases by default. File deps are lowercased at parse time (Parser50 `.Value.ToLower()`).
- Backslashes are the author's separator. FI `XmlScriptInstaller.NormalizeSeparators` replaces both `\` and `/` with the OS separator. FI added an explicit fix because on Linux AltDirectorySeparatorChar == '/', so backslashes were not being converted (comment in `TextUtil.NormalizePath`).
- An empty `destination` means the file goes to the root under its own base name (FI `InstallFileFromMod`: `toPath==""` -> `GetFileName(fromPath)`). For `<folder destination="">`, contents go to the root. MO2: destination defaults to source (`readFileList`). These differ for `<file>` without destination, so this is a real divergence. See "tolerance" below.
- A `destination` ending in a separator creates a dir (mkdir instruction) rather than a file.
- Missing source: a warning only in Vortex (`Source doesn't match any files`, install continues). MO2 collects them and asks the user "install anyway?". For lmm, a headless replay needs an explicit policy.
- Non-unique names: step, group, and plugin names are not unique in the wild. Vortex source comments say "step names are not unique :(" and "group names not unique either...". Any recording scheme keyed on names has to carry an index too.
- XSD validation: Vortex validates strictly against the XSD and errors out; real-world files that fail are a known source of "Invalid XML" errors (INFERRED from the code path, no corpus measured). UNVERIFIED how many real files fail strict validation. MO2 uses a QXmlStreamReader, tolerant, no XSD.

### 2. Evaluation order

#### Vortex (FI `XmlScriptInstaller.InstallFiles`, VERIFIED)

Instruction list is built in this order, with a priority offset of 10^9:

1. `requiredInstallFiles`, priority = file.priority **- 1e9**.
2. Files of selected options (plus unselected options' files with `alwaysInstall`, or `installIfUsable` when the option isn't NotUsable), priority = file.priority + 0. (Files, collected in step/group/option document order.)
3. `conditionalFileInstalls` patterns whose dependencies are satisfied by the final flag state, priority = file.priority **+ 1e9**.

Conflict rule per destination (`ShouldUpdate`): higher priority wins, and on a tie the larger `fromPath` string compare wins (lexicographic on source path, not install order). The destination key is the exact normalised dest string.
Quirk: `InstallFolderFromMod` passes `installableFile.Priority` WITHOUT the offset, so `<folder>` entries in requiredInstallFiles and conditionalFileInstalls ignore the layering. VERIFIED by reading; it looks like a bug but is what Vortex ships. Matters if lmm wants to be bug-compatible, or choose "correct".

#### MO2 (C++, `fomodinstallerdialog.cpp`, VERIFIED via fetch)

`updateTree` gathers required files, then conditional installs whose condition passes, then checked choices on visible pages. It does `std::stable_sort` by priority, then by a sequence number. Folders `MERGE`, files `REPLACE`. Same-priority overwrites log a warning. So the tie rule is "later in document order wins", differing from Vortex's lexicographic path rule. Required/conditional layering by offset is NOT in MO2 (no ±1e9). Both agree: higher priority wins.
Consequence: for tied or layered cases, results can differ between managers. Collection authors almost always use Vortex, so a replay for #597 should follow Vortex semantics. A plain user FOMOD works the same in both for the typical no-conflict case.

#### Flags and visibility (VERIFIED, FI)

- Selecting an option sets its flags (`enableOption`), deselecting removes them (`m_csmState.RemoveFlags(option)`). Flags are keyed by the option that set them. A step's `<visible>` is evaluated against the flags at the time the step is reached, in order. Steps are walked in order, invisible ones are skipped (also in headless preset mode, which evaluates visibility as it goes). So evaluation is sequential and stateful: step N's visibility depends on choices from steps < N.
- Final `conditionalFileInstalls` use the flag state at the end.
- Flag semantics in MO2 (`testCondition(maxIndex, flag, value)`): scans back from the most recent visible page, first matching flag name decides; with no matching flag the condition passes only if the expected value is empty. Vortex keeps a ConditionStateManager (flag name -> value, with set-by-option tracking). Last-writer behaviour with several options setting the same flag was not checked. UNVERIFIED (see `ConditionStateManager.cs`).

#### fileDependency / unknown plugin state (VERIFIED)

- Vortex delegates `IsActive(file)` / `IsPresent(file)` to the host (`PluginDelegates`): Active = IsActive, Inactive = IsPresent && !IsActive, Missing = !IsPresent. With null delegates the condition returns false (`PluginCondition.GetIsFulfilled`). So "unknown" in Vortex = whatever the host callback says; when delegates are absent every fileDependency is false (so even `Missing` is false). Note `Missing` with unknown host = false, not true.
- MO2 `fileState`: only .esp/.esm/.esl consult the plugin list; other files are searched through the VFS only if the `allowAnyFile` setting is on, else they are always Missing (with a warning); with `checkDisabledMods` also scans disabled mods for Inactive. VERIFIED (`installerfomod.cpp:156-205`).
- Implication for lmm: this input has no analogue until a bethesda adapter knows plugin state (the profile's mods' .esp/.esm/.esl files and a load order). A reasonable first definition: Active = a mod in this profile (enabled) ships that file at the matching path; Inactive = disabled in profile; Missing = absent. The same file might also come from the base game's Data dir (e.g. `Skyrim.esm`, `Update.esm`, DLC masters, which many FOMODs test!). lmm would need the game's installed master list. INFERRED. Vortex's collections replay: the recorded choices bypass most of this because the pattern lookups are only needed for the option type resolution (NotUsable forces).

#### "Scripted" FOMODs (C#)

- A FOMOD with `fomod/script.cs` (C# script, executes arbitrary code via a `Script` class using `IScriptFunctions`) instead of `ModuleConfig.xml`. Older NMM/FOMM-era format (Oblivion, FO3/NV era). VERIFIED that both stacks handle it separately:
  - Vortex: `FI/src/InstallScripting/CSharpScript/` (compiler, executor, function proxy); FI README: "C# scripts (Windows only)" in the IPC variant; the Native AOT variant "limited to XML scripts". Linux/native: no C# scripts. VERIFIED (FI README).
  - MO2: the main plugin is XML only (supported iff `ModuleConfig.xml` exists); a separate plugin `installer_fomod_csharp` handles C# scripts (README: without it MO2 falls back to NCC; the plugin "is better integrated"). VERIFIED.
- Frequency: UNVERIFIED (no corpus measured). INFERRED: rare today, confined to older titles (Oblivion/FO3/FNV, some old Skyrim mods); modern Skyrim SE / Fallout 4 / Starfield FOMODs are XML. Decision: refuse C# scripts with a clear message ("scripted FOMOD not supported"), never execute them (code exec). Also there is the older `omod` support dir in FI (`InstallScripting/omod`) - Oblivion-only.

#### Vortex's choice replay (headless, the key reference for #597; VERIFIED, FI `XmlScriptExecutor.DoExecute/preselectOptions`)

- Preset type: `OptionsPreset { steps: [{ name, groups: [{ name, choices: [{ name, idx }] }] }] }`.
- Replay matching is by NAME only (step name, group name, option/plugin name); `idx` is carried but never compared (checked all `preChoice.name == option.Name`).
- With a preset and not in "preselect" mode: no UI; for each step in document order, skip if its `visible` is false under current flags, run `preselectOptions` + `fixSelected`, then `collectInstructions`.
- In `preselectOptions`, an option is enabled if type == Required, or group type SelectAll, or it is in the preset. Recommended is NOT auto-selected when a preset exists (`!m_Preset.HasValue && Recommended`). When a group has any preset entries, other options in the group get explicitly disabled. `SelectExactlyOne` groups with nothing selected get option[0] (`setFirst`). A preset option that resolves to NotUsable is forced to CouldBeUsable "otherwise the user might not be able to deselect it" (so a recorded choice can override NotUsable).
- Also `fixSteps`: a group with >1 Required options is downgraded (AtMostOne->Any, ExactlyOne->AtLeastOne).
- If a recorded name no longer matches (mod updated), it silently doesn't select. No error. lmm should be stricter: report unmatched choices (GOFOMOD has `Match`, `Unanswered`, `Keep` for exactly this).

### 3. collection.json choices (VERIFIED, VX source read)

`src/renderer/src/extensions/collections/types/ICollection.ts`:

```ts
export interface ICollectionMod {
  name: string; version: string; optional: boolean; domainName: string;
  source: ICollectionSourceInfo;          // type: "browse"|"manual"|"direct"|"nexus"|"bundle"; modId, fileId, md5, fileSize, logicalFilename, fileExpression, tag, updatePolicy ("exact"|"latest"|"prefer")
  hashes?: IFileListItem[];                // [{ path, md5?, xxh64? }]  (md5 hex, xxh64 base64)
  choices?: IChoiceType;                   // installer-specific data to replicate the choices the author made
  patches?: IModPatches;                   // { [filePath]: baselineHash }  binary diffs applied after install
  instructions?: string; author?: string; details?: { type?, category? };
  phase?: number;
  fileOverrides?: string[];
}
```

`installer_fomod_shared/types/interface.ts`:

```ts
export type IChoices = { name: string; groups: { name: string; choices: { name: string; idx: number }[] }[] }[] | undefined;
export type IChoiceType = { type: string; options: IChoices };   // type === "fomod"
```

So JSON: `"choices": {"type":"fomod","options":[{"name":"<step>","groups":[{"name":"<group>","choices":[{"name":"<plugin>","idx":<int>}]}]}]}`. `collections/util/defaults.ts` checks `installerChoices?.type === "fomod"`, confirming `"fomod"` is the discriminator. The same type is the preset FI reads.

- `idx`: the index of the option within its group in the order the installer UI presented (FI `convertOptions` `idx++` over the group's options after sorting). INFERRED for exact sort basis (post-`order` sorting or raw?). Matching ignores it, so tolerate any value.
- Producer rules (`modToCollection.ts`): per-mod `installMode` is "fresh" (default; no choices, no hashes), "choices" (emit `choices` only), or "clone" ("replicate": emit `hashes` for every staged file; no `choices`). So `choices` and `hashes` are alternatives per mod entry, not both. VERIFIED (lines 127-160).
- `hashes` replay: `IFileListItem[]` becomes `fileList`; in Vortex the install option `fileList` means "the listed files (and only those) get extracted" (InstallManager comment at ~1181). So for clone mode a file set is replayed by filtering the archive, with no FOMOD logic. Verify afterward by md5/xxh64. UNVERIFIED exact verification code path; file types verified.
- `fileOverrides`: `string[]` per mod, and `ICollectionAttributes.fileOverrides: {[modId]: boolean}` author toggle. Semantics (files this mod must win conflicts on) are INFERRED, not read. Not needed for FOMOD replay.
- `patches` also exist (binary diffs); skip for MVP, but note collections may contain them.
- Not in the collection.json mod entry: no "plugin state"; a FOMOD replay in Vortex still evaluates fileDependency against the author-machine host. Choices record only selections, so any dependencyType patterns that depend on installed plugins re-evaluate on the replaying machine (that only affects forced NotUsable/Required, and presets override NotUsable as above).

### 4. Go ecosystem

- go.mod (checked): `golang.org/x/text` is NOT a direct or indirect requirement and not in go.sum (grep count 0). `golang.org/x/{sync,term,sys}` are. The repo's rule is no new dependency without a stated reason the standard library cannot meet. VERIFIED.
- Stdlib covers it: `encoding/xml` with `Decoder.Strict=false` (+ `CharsetReader` or decode first), `unicode/utf16` for UTF-16 LE/BE (BOM or the `3C 00 3F 00` sniff), and a 32-entry table for the Windows-1252 0x80-0x9F range (otherwise Latin-1 identity). So no x/text needed. INFERRED but straightforward. XML parse must cap size and not expand entities (encoding/xml does not expand custom entities by default).
- Existing Go lib: `github.com/Rethunk-Tech/go-fomod` (MIT). 1 commit, 0 stars, pushed 2026-11-09, i.e. brand new and unproven. It's ~950 non-test lines (decode 64, find 87, parse 332, resolve 327, types 133). Features: UTF-8/16/1252 decode ignoring declaration, `Resolve(cfg, choices, ctx) []Op` sorted by priority, `Match/Unanswered/Keep` for replay, `VisibleSteps/FlagsFrom/PluginType` for wizards, caps on read size and nesting. Dependencies: `go-safefs`, `go-semverish` (both from the same org) and a huge tool block (lint tooling only, not runtime). Verified by reading README/go.mod/function list, I did NOT audit semantic correctness against Vortex (priority offsets, tie rule, folder quirk). No other Go FOMOD implementation found (web search + GitHub repo search).
- Assessment: a pure-stdlib in-tree implementation is reasonable: types+parse ~350 lines, conditions/flags/type-resolution ~200, resolve/replay ~200, decoding ~100, so roughly 800-1000 lines plus a table-driven test corpus (fixtures from real FOMODs; FI's `test/TestData` has Vortex's test archives to crib the expected-instruction format: `InstallInstruction.cs`). Writing it in-tree avoids a new dependency and lets lmm pin Vortex-compatible semantics (offsets, tie rule). Vendoring GOFOMOD's ideas (Match/Unanswered/Keep) is sensible.

### 5. Interaction design needs

(a) Plan rendering (both frontends; Apply never calls back):

- A FOMOD's steps depend on earlier answers (visibility via flags), so the plan can't be a flat static option list. Needs a core-level stepwise query: `FomodSession{Config, Choices}` with `VisibleSteps(choices)`, per-plugin resolved type (Required/NotUsable/etc. with dependency context) and the "unanswered" list. The plan = {module name, steps -> groups -> plugins (name, description, image path, type, group rule), current selection, validation errors per group (AtLeastOne/ExactlyOne violated)}. CLI: flags like `--fomod-choice 'Step/Group=Plugin[,Plugin]'` (or `--fomod-choices file.json`) plus `--fomod-defaults` (accept Recommended/Required); web UI: the plan modal re-posts `POST /api/v1/plans/install` with the chosen set until the plan reports no unanswered groups (matching the existing "typed error answered by re-running with an Options field" pattern: a `FomodChoicesRequired` typed error carrying the remaining steps).
- Images/descriptions need to be extracted to a temp location for the web UI (paths inside the archive; case-insensitive lookup).
- Non-interactive CLI: without choices supplied, error with the step list; `--yes`/defaults fall back to Vortex's "Recommended + first-of-ExactlyOne + Required" rule, which is the preselect behaviour in FI (VERIFIED above).

(b) Recording in the profile:

- `domain.ModReference` is the profile entry (`internal/domain/mod.go:121`). It already has precedent for optional per-profile intent: `Disabled` (`omitempty`/`omitzero`) and `Category`, each documented as "absent means default, byte-identical output when unset". A `Fomod *FomodChoices` (or `Install *InstallChoices`) field with `yaml:"fomod,omitempty"` fits the same way: store Vortex-shaped `[{step,[{group,[plugin names]}]}]` (names plus occurrence index to disambiguate non-unique names) and the FOMOD config hash/mod version it was recorded against (so a changed ModuleConfig.xml is detected: `Unanswered`).
- Export/import/duplicate/copy/`row_copy.go` (`copyRowToProfile`, the one per-profile row mint) must carry it, and the JSON goldens under `internal/{domain,core}/testdata/json` need updating. VERIFIED the field list and mint point exist; INFERRED the carrying work.
- Cache key problem: `cache.ModPath(gameID, sourceID, modID, version)` (`internal/storage/cache/cache.go:45`) keys the cache by version only. Two profiles installing the same mod+version with different FOMOD choices produce different trees. Needs a variant segment (hash of canonical choices) or per-profile resolution at deploy time (see below). VERIFIED the key shape.

(c) Replay from collection.json (#597):

- Map `mods[].choices` (type "fomod") to the profile's recorded form directly; match by names as Vortex does, but report unmatched names rather than silently skipping. `source.fileId`/`md5` choose the archive; verify `md5` before applying choices (a differing archive makes names unreliable). `hashes` (clone mode) are a different mechanism: file filter + verification.
- A collection's mod list has `phase`, `optional`, `fileOverrides`, `patches`, and mod rules, which are outside this spike.

### 6. Fit with lmm code

Read: `docs/plans/2026-09-10-game-adapter-design.md`, `internal/adapter/adapter.go`, `internal/core/adapter_layout.go`, `archive_listing.go`, `extractor.go`, `internal/domain/mod.go`.

#### Adapter seam (VERIFIED)

- Required: `ID`, `Label`, `NormalizeArchive(NormalizeRequest{Game, ModName, Members []string}) (Layout, error)`. Optional capability interfaces: `FileRouter`, `Preconditioner`, `Verifier`, `MergeCompiler`, `Guide`. The design says the bethesda adapter "owns FOMOD, plugins and load order" (issue #354) but also notes "Bethesda's Data/ beside a game-root SKSE/ENB has no adapter here to design against; FileRoute is where a Root field lands additively" and "No roots: map, no second deploy target in 2.0". So a bethesda adapter would also need this Root field.
- `NormalizeRequest` is deliberately pure: member names only, "no filesystem handle on purpose". The rewrite table `Layout` is `map[member]dest` (one destination per member, "" = drop).
- Core applies it in `rewriteExtractedTree` (`adapter_layout.go:112`) on the extracted staging tree (download path `layoutStagedExtract` -> `service.go:2116`; archive-import `Importer.layoutExtracted` -> `importer.go:353`), and `rewritePlannedPaths` is the pure plan-side twin so the plan/ingest agree. `validateLayoutTable` REFUSES two members sharing a destination and a destination that is another member's source.

#### Is `Layout` expressive enough? No, for four reasons (VERIFIED from the types + validator)

1. 1 source -> N destinations: FOMOD may install the same source to several destinations. A map keyed by member can hold one. Needs duplication (copy) support.
2. N sources -> 1 destination with priority resolution: FOMOD's whole conflict model. `validateLayoutTable` rejects it by design. The FOMOD resolver would have to pre-resolve winners and emit a table with the losers dropped (""), which does work with today's Layout: resolution happens inside the adapter, the table only carries winners. So this is expressible if the adapter does the priority resolution itself. Loser bytes are dropped: fine.
3. Rename-chain / swap rules (dest is another member's source): FOMOD routinely maps `Option A/Data/x.esp -> x.esp`, and the destination may equal the name of a dropped member; fine, but chained cases like `Data/x -> x` where `x` already exists as a member would be refused. Rare.
4. Folder entries: expand to per-member lists (the adapter has the member list, so doable).

Verdict: the OUTPUT (a pruned, renamed member map) is representable for the common path, with the copy (1->N) case as the main gap. The INPUT is the problem.

#### Missing inputs (what `NormalizeRequest` lacks)

1. The XML bytes: `Members` is names only; FOMOD needs to read `fomod/ModuleConfig.xml` contents (and optionally images). The design deliberately forbids a filesystem handle in the request. Options: add an optional `Read func(member string) ([]byte, error)` (or `fs.FS`) field, or a separate optional capability `ArchiveInstaller` with `PlanInstall(ctx, req)` taking an `fs.FS`. INFERRED recommendation: a new optional capability rather than bending `NormalizeArchive`, because FOMOD is interactive (multi-step) and stateful, and NormalizeArchive returns one `Layout`.
2. The choices (recorded, profile-scoped): not in `NormalizeRequest`; also `NormalizeArchive` is called from places with no profile context (verify re-layout, the download path), so the recorded choices must be threaded in from `ModReference`.
3. Active plugin / installed-file state for `fileDependency` (profile mods + game Data masters), plus game version for `gameDependency` and script extender versions for `skseDependency` etc.
4. Interactive iteration (the stepwise question loop above), which Layout cannot express; the plan type needs a "questions" document.
5. Mod name is already there; file order is sorted (fine).

#### Where it would hook in

- Adapter side: `internal/adapter/bethesda/` implementing a new optional interface (e.g. `InstallerScripts`), registered via `adapter/registry.go`. XML parse/evaluate is a pure package (e.g. `internal/fomod`) that the adapter calls; keeps stdlib-only and table-testable; the adapter package must not import core/linker (boundary_test).
- Core side: at the same two call sites that call `archiveLayout` (`layoutStagedExtract`, `Importer.layoutExtracted`), core detects the capability, reads the extracted `fomod/ModuleConfig.xml` from the staging dir, runs the session with the recorded choices from the `ModReference` (new field), and builds a `Layout` (winners-only rewrite table) to hand to the existing `rewriteExtractedTree`; the fomod/ dir itself would be dropped or kept (an author preference). Plan: PlanInstall/PlanImportArchive return the questions doc; Apply with choices re-validates (stale choices -> typed error). Plan/ingest agreement is preserved if both go through the same resolver, like `importDeployablePaths` shares `Layout`.
- Because the cached tree is post-layout, the cache key needs a choices-hash variant (see 5b), or re-resolve at deploy from the cached full archive tree plus the choices (no new cache variant, but `deployableFiles` would need to run the resolver each deploy, and `FileRouter`/Layout application there doesn't exist today). The first (variant key) is the smaller change. INFERRED.
- Extractor note: `extractor.go` extracts the whole archive (.zip native, .7z/.rar via 7z) into staging, with reserved-name checks and a sanitizePath containment test; FOMOD adds nothing there, and the full tree is available in staging for the resolver and images. VERIFIED function list; did not read bodies beyond the headings.

### 7. Risks / decisions to surface

- Semantics: follow Vortex (offsets, name-based replay, tie rule) since collections are authored in Vortex. Decide on the `<folder>` priority quirk (correct vs bug-compatible).
- fileDependency with no knowledge of the game's masters: unknown-state policy must be explicit (Vortex: host decides, null = false; MO2: only esp/esm/esl). Needs the bethesda adapter to supply plugin/master knowledge, or a documented "assume Missing, warn".
- Scripted (C#) and omod: refuse with a typed error.
- License: Vortex's FI is GPL-3.0; do not copy code, only observed behaviour. (INFERRED: the repo's license is not checked against GPL compatibility; just don't port.)
- Case-insensitive matching of `source` against archive members on a case-sensitive FS; and backslash normalisation, both required before lookup.
- Empty-choices default: if no choices are recorded and the CLI is non-interactive, fail rather than silently pick defaults, unless `--fomod-defaults` is passed.

---

## Part 3 — Plugin load order and LOOT

Legend: VERIFIED (source) = read in the cited file this session. INFERRED/UNVERIFIED = reasoning or not checked.
Sources (abbrev): LLO = github.com/Ortham/libloadorder (NOT loot/libloadorder: that 404s; it lives under Ortham, GPL-3.0; docs in doc/src/*.md, paths in src/game_settings.rs). ESP = github.com/Ortham/esplugin (GPL-3.0). LL = github.com/loot/libloot (now RUST, GPL-3.0-or-later; C++/Python/Node wrappers; depends on esplugin, libloadorder, loot-condition-interpreter (MIT)). VX = github.com/Nexus-Mods/Vortex.

### 0. Framing

- lmm mod order (profile.Mods "first = lowest priority", internal/domain/profile.go:22; `lmm profile reorder`) decides FILE precedence at deploy. VERIFIED (repo).
- Plugin order is a second axis: which .esp/.esm/.esl the ENGINE loads, in what order, and which are active. It lives in a file in the game's Proton prefix (or the INI / mtimes for the old games), not in the Data dir. Mod order cannot be derived 1:1 from plugin order (one mod can ship several plugins; a plugin can sit anywhere). So the profile needs its own plugin list (see 5).

### 1. Per-game active list and order storage

Load-order methods, VERIFIED (LLO doc/src/load_order_mechanisms.md, src/game_settings.rs `load_order_method`):

- Timestamp: Morrowind, Oblivion, Fallout 3, New Vegas. Order = mtime of plugin files in Data (earliest loads first). Active list is separate.
- Textfile: Skyrim LE (plugins.txt for active; loadorder.txt = full order, UTF-8 no BOM, beside plugins.txt). Also Oblivion Remastered.
- Asterisk: Skyrim SE/AE, Skyrim VR, Fallout 4, Fallout 4 VR, Starfield. One file, plugins.txt, `*Name.esp` = active, bare name = inactive, order = file order, `#` comment lines.

Active-list file locations (LLO src/game_settings.rs plugins_file_path / appdata_folder_name; all VERIFIED):

| Game | Active list | Notes |
|---|---|---|
| Morrowind | `<game>/Morrowind.ini`, `[Game Files]` section, lines `GameFile0=Morrowind.esm`, `GameFile1=...` (ini.rs, timestamp_based.rs save) | plugins dir = `<game>/Data Files`; order by mtime |
| Oblivion | `Plugins.txt` in `<game>` if Oblivion.ini `[General] bUseMyGamesDirectory=0`, else `%LOCALAPPDATA%/Oblivion/Plugins.txt` | game reads `Plugins.txt` (capital P) even though launcher writes plugins.txt; mtime order |
| Fallout 3 | `%LOCALAPPDATA%/Fallout3/Plugins.txt` | mtime order |
| New Vegas | `%LOCALAPPDATA%/FalloutNV/Plugins.txt`; Epic: `FalloutNV_Epic` | mtime order |
| Skyrim LE | `%LOCALAPPDATA%/Skyrim/Plugins.txt` + `.../Skyrim/loadorder.txt` (Enderal: `enderal`) | |
| Skyrim SE/AE | `%LOCALAPPDATA%/Skyrim Special Edition/Plugins.txt`. Variants: `... GOG` (Galaxy64.dll present), `... EPIC` (EOSSDK-Win64-Shipping.dll present), `... MS` (appxmanifest.xml) | asterisk |
| Skyrim VR | `%LOCALAPPDATA%/Skyrim VR/Plugins.txt` | asterisk |
| Fallout 4 | `%LOCALAPPDATA%/Fallout4/Plugins.txt`; variants `Fallout4 EPIC`, `Fallout4 MS` | asterisk |
| Fallout 4 VR | `%LOCALAPPDATA%/Fallout4VR/Plugins.txt` | asterisk |
| Starfield | `%LOCALAPPDATA%/Starfield/Plugins.txt` | asterisk; extra plugin dir `Documents/My Games/Starfield/Data` |

Under Proton: `%LOCALAPPDATA%` = `<prefix>/pfx/drive_c/users/steamuser/AppData/Local` (INFERRED from Wine/Proton layout; LLO's non-Windows `local_path` fn returns `Error::NoLocalAppData`, i.e. LLO makes the CALLER supply the path via `GameSettings::with_local_path`, game_settings.rs:378-387). Steam prefix = `<steam library>/steamapps/compatdata/<appid>/pfx`. LLO derives My Games as `local_path/../../../Documents` on non-Windows (game_settings.rs:516), which in a Proton prefix resolves to `drive_c/users/steamuser/Documents` (correct).

Format details (VERIFIED, LLO doc/src/active_plugins.md, textfile_standard.md, asterisk_based.rs):

- Encoding Windows-1252; plugins with names not representable must be skipped and reported (LLO `strict_encode` errors).
- LLO writes LF (`writeln!`), reads either; the textfile standard text says CRLF. Games accept both in practice (INFERRED); recommend writing CRLF to match what the game launchers write? UNVERIFIED which is safer; LLO's LF choice is evidence LF works.
- Active limit 255 total; SSE/FO4: light plugins don't count, separate 4096 light limit.
- No duplicate lines.
- Implicitly active plugins (not required in plugins.txt; LLO skips "early loading" ones when saving, asterisk_based.rs:162-166):
  - SSE: Skyrim.esm, Update.esm, Dawnguard.esm, HearthFires.esm, Dragonborn.esm + Creation Club plugins listed in `<game>/Skyrim.ccc`.
  - SkyrimVR: same + SkyrimVR.esm. Skyrim LE: Skyrim.esm (Update.esm always loaded if present).
  - FO4: Fallout4.esm, DLCRobot, DLCworkshop01/02/03, DLCCoast, DLCNukaWorld, DLCUltraHighResolution + `Fallout4.ccc`. FO4VR: Fallout4.esm, Fallout4_VR.esm.
  - Starfield: Starfield.esm, Constellation, OldMars, ShatteredSpace, SFBGS003/004/006/007/008/00D/047/050 + `Starfield.ccc` (My Games copy overrides game dir copy even if empty).
  - FO4/Starfield ignore plugins.txt and .ccc if `sTestFile1..5` set in the INI (ini.rs).
  - Starfield removes blueprint plugins / `BlueprintShips*` plugins from plugins.txt itself; LLO does not write them and instead sets mtimes for blueprint masters (asterisk_based.rs:166-215).
- Timestamp games: to change order you must set mtimes. IMPORTANT for lmm (INFERRED): lmm links (symlink/hardlink) files; mtime is read from the target of a symlink (LLO `get_target_modified_timestamp`, game_settings.rs:872 suggests it follows links) so setting mtime means touching the CACHED file, which is shared between profiles. Old games (Morrowind/Oblivion/FO3/NV) therefore need either per-profile copies or a "set mtime on link" step: real design cost. Recommend Tier 1 = asterisk/textfile games only.
- Oblivion Remastered has its plugins.txt at `<game>/OblivionRemastered/Content/Dev/ObvData/Data/Plugins.txt` (VERIFIED game_settings.rs:116, 598) with a sibling loadorder.txt.

### 2. Plugin header facts

Minimum header parse a sorter needs (VERIFIED ESP src/record.rs, src/plugin.rs):

- Record header: type(4) size(u32) [Morrowind: 4 unknown bytes] flags(u32) formID(u32, non-Morrowind) 4 bytes [vc info] + 4 more bytes (not Oblivion, not Morrowind). Header length: Morrowind 16, Oblivion 20, everything else 24 (record.rs `header_length`). First record is `TES4` (TES3 for Morrowind).
- Subrecords follow: type(4) + size (u16 in TES4+ games, u32 in Morrowind; INFERRED from UESP, not re-read; plus the `XXXX` oversize subrecord mechanism, rarely in headers).
- `MAST` subrecords = master filenames, null terminated, Windows-1252 (plan.rs:733-746). `DATA` follows each MAST (8-byte size, ignorable).
- Master flag: flags & 0x1 (TES4); Morrowind: HEDR data byte 4, & 0x1 (plugin.rs:479-490).
- Light flag: 0x200 for Skyrim SE and FO4; Starfield 0x100. `.esl` extension implies light. Master = flag OR ext .esm/.esl (SSE, FO4, Starfield); other games flag only (plugin.rs:219-235, 493-500). `.esl` in SSE is also master (light master).
- Starfield: medium flag 0x400, update flag 0x200, blueprint flag 0x800 (plugin.rs:503-515, 275). Starfield and Morrowind need ALL masters parsed to fully understand record IDs (only matters for LOOT overlap, not for masters-first ordering).
- Compressed records flag 0x00040000 (irrelevant for header).
- Size: a pure-Go header reader is ~120-200 lines (stdlib `encoding/binary`, `golang.org/x/text/encoding/charmap` is NOT stdlib: Windows-1252 decode needs a tiny 128-entry table or just treat bytes>=0x80 via a small map; INFERRED, trivial). Tests with testing-plugins fixtures from github.com/Ortham/testing-plugins (licence unchecked). Estimate: 1 day incl. fixtures.
- Hard ordering rules (VERIFIED LLO load_order_mechanisms.md):
  - Masters (master flag) before non-masters; exception where a master depends on a non-master.
  - A plugin must load after its masters.
  - SSE/FO4: official plugins hardcoded first; light plugins loaded "amongst" others but occupy FE slot (no effect on conflict resolution). Skyrim.esm first; Update.esm after other masters if unspecified.
  - Starfield: blueprint masters load after all non-masters (LL sorting.rst "Combine the load orders").
- Limits (VERIFIED active_plugins.md): 255 active (full), 4096 light for SSE/FO4. Starfield medium (FD) / small (FE) slot counts: valid FormID ranges seen in plugin.rs:520-538 (light 0..=0xFFF, medium 0..=0xFFFF); explicit active medium limit (256) UNVERIFIED.
  - Note "254/255": the 255 figure is what LLO documents; the 254 full + FE light split is the runtime slot arithmetic (INFERRED).

### 3. LOOT

Masterlist format (VERIFIED LL docs/metadata/*.rst):

- YAML with top-level `prelude`, `bash_tags`, `globals`, `groups`, `plugins`. Uses YAML anchors/aliases (prelude block with `&anchor`); `plugins` entries may have regex `name`.
- Plugin keys: `name`, `group`, `after`, `req`, `inc`, `msg`, `tag`, `url`, `dirty`, `clean` (+ `lang`, cleaning data). `after` = load after, not a dependency; `req` = requirement (also implies load-after edge in sort); `inc` = incompatibility (message only, not an ordering edge).
- Groups: `name`, `description`, `after` (group names). `default` group default. Group edges become plugin edges (recursive, skipping cycle-causing ones).
- Conditions (any of after/req/inc/msg/tag/url entry may carry `condition:`): grammar `expression: condition {or compound}`, `and`/`or`/`not`, parens. Functions: file(path|regex), file_size, readable, active(path|regex), many(regex), many_active(regex), is_master, is_executable, checksum(path, crc), version / product_version(path, cmp, ver), filename_version(regex, cmp, ver). Regex syntax is Rust-regex-ish.
- Size from loot/skyrimse v0.29: 1.12 MB, 30,159 lines, ~3,213 `- name` entries, 1,642 `condition:` lines, 531 `after:`, 180 `req:`, 284 `inc:` (measured by grep this session). Most of the bulk is msg/dirty/tag. Ordering-relevant surface is small (531 after entries, groups).
- Fetching: raw URL works, e.g. `https://raw.githubusercontent.com/loot/skyrimse/v0.29/masterlist.yaml` (HTTP 200 this session). Repos: loot/{skyrim,skyrimse,skyrimvr?,fallout3,falloutnv,fallout4,starfield,oblivion,morrowind}; branch per masterlist syntax version (v0.10 ... v0.29 present); latest = v0.29 (LOOT 0.29.2 / libloot 0.29.7). `loot/prelude` repo holds the shared prelude (branch v0.29). Branch name = libloot-supported metadata syntax version: pin it.
- Licences: masterlist repos (skyrimse, skyrim, fallout3, falloutnv, fallout4, starfield, oblivion, morrowind, prelude): CC0-1.0 (GitHub API spdx + LICENSE head read for skyrimse). The earlier CC BY-NC-SA belief is WRONG for current repos. libloot GPL-3.0-or-later (Cargo.toml). libloadorder GPL-3.0. esplugin GPL-3.0. loot-condition-interpreter MIT. LOOT app GPL-3.0.
- LICENCE of lmm: MIT (/LICENSE, "MIT License, Copyright (c) 2026 Donovan C. Young"). Implications: (1) CC0 masterlists can be fetched/cached/parsed freely, even bundled. (2) Linking/embedding libloot, libloadorder or esplugin (all GPL) into the lmm binary would make the combined work GPL: incompatible with keeping lmm MIT. Shelling out to a separate GPL executable at arm's length is fine. (3) A clean-room Go reimplementation from the (documented) sorting algorithm is fine but copying Rust code line-by-line is not; reading the docs (sorting.rst) to write independent code is the safe path. MIT crate loot-condition-interpreter could be ported (MIT permits). All INFERRED legal reading, not legal advice.
- Is there a LOOT CLI? NOT in upstream. LOOT 0.29.2 release assets are only `loot_0.29.2-win64.{7z,exe}` (GitHub API); Linux users use the Flathub GUI (io.github.loot.loot) (README). The GUI has no sort-and-exit CLI that I found (UNVERIFIED for flags; none documented in README). MO2's `lootcli` (TanninOne/modorganizer-lootcli) exists, last pushed 2023-10-02, Windows/C++ against libloot, no licence reported by the API. Not a dependable option on Linux.
- Rust/C++/Python/Node bindings for libloot exist; a Rust `libloot` crate on crates.io. For lmm (no CGO, GPL) there is no off-the-shelf Go binding.

Options (estimates are mine, INFERRED):
(a) Pure-Go subset sorter: masters-first + plugin-after-its-masters + hardcoded early plugins + collection/user rules (`after`, `req`, group) + stable tiebreak on existing order. Matches the "Tier 0" in LL's algorithm (edges 1-4, group edges, tie-break) minus overlap edges. ~600-900 LOC + tests (header reader 150, graph/toposort + cycle report 200, plugins.txt/INI writers 200, ccc/implicit lists 100). Risk LOW-MEDIUM: wrong relative to LOOT for overlap-driven ordering but deterministic and explainable; cycles are surfaced as errors. 1.5-2 weeks incl. both frontends excluded.
(b) Shell out to external tool: no maintained upstream CLI; would mean writing our own helper in Rust with libloot (GPL, separate binary shipped separately) or requiring the user to run LOOT (Flatpak GUI, Windows-built sorting in prefix). High friction, packaging burden; risk HIGH for cost, but gives true LOOT parity. Not recommended for v1 of the feature. A Wine-run LOOT (Windows build under the game's prefix) is possible but fragile (UNVERIFIED).
(c) Reimplement masterlist conditions: parser for the mini-language (~300 LOC) + evaluator needing file existence, regex path matching, `active()` (needs current active set), `checksum` (CRC32 of plugin files), `version/product_version` (PE version resource for exes, header HEDR/plugin version for plugins; PE parsing is stdlib `debug/pe` but resources aren't: the hard part), `filename_version` (regex). Add YAML anchor handling (go.yaml.in/yaml/v3 supports anchors). Prelude merging by textual replacement is needed (documented caveat in file_structure.rst). ~1,000 LOC + cache/fetch/pin of masterlist per game. Risk MEDIUM-HIGH: breadth of conditions in 1,642 lines; but a fail-closed rule (unknown condition => treat metadata as not applicable and warn) bounds the harm. ~2-3 weeks. Not needed for #597 because collections carry their own rules.
Recommended path: (a) first, with masterlist (a+c) as an optional later layer for "suggest order" using only `group`/`after`/`req` edges.

### 4. How NexusMods collections carry plugin order (VERIFIED, Vortex src/renderer/src/extensions/collections/util/gameSupport/gamebryo.tsx; types/ICollection.ts)

- Supported collection games: skyrim, skyrimse, skyrimvr, fallout3, fallout4, fallout4vr, falloutnv, starfield, oblivion, enderal, enderalspecialedition (gameSupport/index.ts keys).
- collection.json has `plugins` and `pluginRules` (type ICollectionGamebryo):
  - `plugins?: [{name, enabled?}]`: generated by `getEnabledPlugins`: only ENABLED plugins, sorted by the curator's loadOrder, so it IS an ordered list. Comment says "absent from a manifest authored in drag-and-drop load order mode".
  - `pluginRules: { plugins: [{name, group?, after?: [string|{name,display,condition}]}...], groups?: [{name, after?}] }`: copied from the curator's LOOT USERLIST (only entries having `after` or `group`, filtered to plugins in the collection). So these are RULES for a LOOT sort, not an order.
- On install Vortex (parser): (1) adds new groups + group `after` rules to the user's userlist, (2) enables plugins: all included plugins ON unless listed `enabled === false`, (3) for each pluginRules plugin sets group and adds `requires`/`incompatible`/`after` rules to the userlist (skippable by user: `skipPluginRules`), (4) the user then runs LOOT sort. NOTE `plugins[].order` is NOT applied directly by this parser: the ordered list is advisory; the real ordering on the installer's machine comes from LOOT + the rules. (Read parser lines ~150-260; I did not read Vortex's "Interface" or any later sort invocation; whether Vortex calls LOOT automatically after install is UNVERIFIED.)
- Inconsistency to note (UNVERIFIED which side the wire uses): generator emits LOOT-style keys (`after`, and userlist keys `req`/`inc` if present), but the parser reads `plugin["requires"]`, `plugin["incompatible"]`, `plugin["after"]`. Treat unknown keys leniently; real collection.json samples needed.
- Mod-level rules are a separate thing: `ICollectionModRule { source, type: before|after|requires|conflicts|recommends|provides, reference }` — these order MODS (file precedence) and map onto lmm's existing profile order, NOT plugin order.
- So: yes, the order is rules + an ordered enabled-list hint. Mapping = build a DAG of {plugin masters edges, group edges, `after` edges, `req` edges}, topologically sort with a stable tiebreak using the collection's own `plugins[]` order as the priority (then existing load order, then case-insensitive name per LL's tie-break). Cycle => error naming the rule chain. Also: apply `enabled:false` as inactive entries (`plugins.txt` line without `*`).

### 5. lmm fit

Read from repo (VERIFIED):

- internal/adapter/adapter.go: GameAdapter has 3 required methods (ID, Label, NormalizeArchive); capabilities are optional interfaces (FileRouter, Preconditioner, Verifier, MergeCompiler; Guide dropped in #443) type-asserted by core through nil-safe helpers; adapters are pure tables + read-only reports; core owns all writes; adapter package must not import core or linker (boundary_test.go). Adapters present: bepinex, d2r, icarus, generic.
- domain.Game has Adapter, InstallPath, SteamAppID, Loader; Profile.Mods is ordered, Profile.Overrides map[path -> content] is "relative to game install" (internal/domain/profile.go:77).
- core/overrides.go applyProfileOverrides: writes files under game.InstallPath (traversal-guarded, originals capture #350). Called from deploy.go:974; applyAdapterCopyOnce at deploy.go:985 for RouteCopyOnce members. Both run inside the deploy flow.
- Prefix: lmm has NO compatdata/Proton prefix knowledge for non-BN games. `grep` finds only launch_battlenet.go (prefix discovery for Battle.net: "pfx" folder, umu `WINEPREFIX`) and the Steam source (library scan via libraryfolders.vdf, appmanifest -> `<library>/steamapps/common/<dir>` in source/steam/install_app.go). Game has SteamAppID, so `<library>/steamapps/compatdata/<SteamAppID>/pfx` is derivable from InstallPath (`.../steamapps/common/X` -> `.../steamapps/compatdata/<id>`) (INFERRED, simple). Non-Steam (GOG/Epic via Heroic/Lutris) needs a configured prefix path.

Proposed design hook (INFERRED, my recommendation):

1. New optional capability in internal/adapter, e.g. `PluginOrderer` with PURE methods: `PluginRules(g) PluginSpec` (extensions, header flags per game, hardcoded/implicit lists, active-limit rules, list-file kind: asterisk|textfile|ini|mtime, appdata folder name candidates, `.ccc` filename) and `RenderActiveList(spec, ordered []PluginEntry) ([]byte, error)` (Windows-1252 encode + format). Adapter stays pure; no filesystem access.
2. core owns: scanning a deployed/cached mod for plugin files, header read (small internal helper `internal/plugins` or inside core; a pure-Go TES4 reader, stdlib only), the sort (topo with stable tiebreak), prefix resolution, and the write.
3. State: add per-profile plugin list to the profile document (ordered entries {name, enabled}, plus optional `plugin_rules` {groups, plugins[{name, group, after, req}]} user rules). Do NOT overload Profile.Overrides: its content is a user-authored string map and the plugins.txt target is OUTSIDE InstallPath (prefix AppData), so applyProfileOverrides' containment check would reject it by design. Plugin file mods remain mod-ordered for file precedence; plugin list is a separate ordered list over plugins discovered in the deployed set.
4. Write step in deploy: next to applyProfileOverrides/applyAdapterCopyOnce in deploy.go (after link, before hooks/post-deploy), call `s.applyPluginOrder(game, profile, deployedMods)`: read headers -> validate (missing master, >255 full, >4096 light, unencodable name) as typed warnings/errors -> sort -> render -> write `Plugins.txt` atomically with originals capture (reuse the originalsStore for backup of the user's pre-existing file). Preserve user-activated plugins not owned by lmm? (open question: lmm should manage only plugins it deployed; leave lines for unknown plugins untouched or place them at positions as libloadorder does: LLO appends/removes by installation state.)
5. Creation Club / official plugins: don't write implicit ones; Read the game's `.ccc` for ordering anchors only.
6. Timestamp games (Morrowind/Oblivion/FO3/NV): defer. Order requires mtime control and shared cache entries are symlinked (above). Morrowind's active list lives in the game INI (`[Game Files]`), an in-place edit of a user file rather than a replace.
7. Frontends: plan/apply surfaces both read the same core Plan (show resulting plugin order, warnings); reorder flow could reuse reorder modal with a second tab. Core method pair: `PlanPluginOrder / ApplyPluginOrder` per the project's Plan/Apply convention.

Open risks / questions

- Collection `pluginRules` real-world JSON shape (above): obtain real collection.json samples.
- Whether Skyrim SE under Proton honours a Linux-written `Plugins.txt` in a case-sensitive FS: Wine is case-insensitive for lookups inside the prefix but a file we create as `plugins.txt` vs existing `Plugins.txt` may produce duplicates on a case-sensitive FS; match the existing file's case, else use `Plugins.txt` (LLO comment: game reads `Plugins.txt`). INFERRED.
- Starfield's blueprint masters need mtime control (LLO sets timestamps), another reason for a Tier 1 that excludes Starfield blueprint plugins.
- #355 label says post-v2.0 / milestone v2.1; it blocks #597.
