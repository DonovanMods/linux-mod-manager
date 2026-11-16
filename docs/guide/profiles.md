---
title: Profiles
nav_order: 5
---

# Profiles

A **profile** is a list of mods for one game: which mods, which versions, and
in what order they load. Every game starts with one profile, called
`default`.

You can make more. A common reason is to keep a "vanilla plus" setup next to
a heavily modded one, or a co-op setup that matches your friends' mods next to
your solo game, and swap between them without reinstalling anything.

## The active profile

The game folder can only hold one set of mods at a time, so one profile per
game is **active**: it's the one actually in the game. The others are saved
lists, ready to switch to.

Switching takes the old profile's mods out of the game and puts the new
profile's mods in. Mods that both profiles use stay where they are, and
nothing is downloaded again if lmm already has it.

## In the browser

- The **profile picker** at the top of the page chooses which profile you're
  *looking at*. That alone doesn't change your game.
- **Switch and deploy…** next to it makes that profile the active one. As
  with everything else, lmm shows you what will change before it does it.
- The **Profiles** window, opened from the picker, is where you create,
  rename, duplicate, delete, export and import profiles.
- **Import collection…**, in the picker's menu under **Manage profiles…**,
  opens the import on its own: a collection file, a collection's address,
  and a Steam Workshop or NexusMods collection when the game uses that
  site (its sources, in the game's editor under **⚙ Setup → Games**; a game
  that uses neither says so, with a link there). Each kind of import has
  its own **Profile name** field. Mission Control's **Profile** card, when
  it's showing, has the same button. See
  [Sharing a profile](#sharing-a-profile).
- Pasting a collection's address into the **search box** offers to import
  it instead of searching for it: a NexusMods collection's page (or its
  slug, when the search finds no mods for it), an `https://` address of a
  `.lmmc` file, or a Steam Workshop collection's link. A NexusMods
  collection for a different game than the one you have selected says so
  rather than offering an import that would be refused.

## In the terminal

```bash
lmm profile list
lmm profile create coop
lmm profile switch coop
```

`lmm profile list` shows every profile for the game and which one is
active. Commands like `lmm install` and `lmm list` work on the active
profile unless you say otherwise.

## Duplicating a profile

To start a new profile from one you already have, duplicate it. The copy has
the same mods in the same load order, at the same versions, with the same
locks, the same disabled mods, and the same settings (link method, hooks and
config overrides). It is not made the active profile: its mods count as
installed straight away, and they go into the game when you switch to it.
If you share the profile as a collection, the copy counts as a new one: it
keeps the title, description and credits, and starts again at version 1.

```bash
lmm profile duplicate coop coop-test
```

In the browser, use **Duplicate** in the Profiles window.

## Copying a mod to another profile

A mod you already have in one profile can be copied into another without
installing it again. Nothing is downloaded: lmm already has the files. The
copy keeps the same version, and the lock if the mod is locked.

```bash
lmm mod copy 12345 --to coop
lmm mod copy --all --to coop
```

`--all` copies every mod of the profile you're in. Add `--dry-run` to see
what would happen first. If the other profile already has the mod, lmm
stops and tells you; add `--replace` to overwrite it. Steam Workshop items
are never copied: every profile already has them.

If the other profile is the active one, the mod goes into the game right
away, linked the way that profile links its mods. Otherwise it's only added
to the profile, and goes into the game when you switch to it.

In the browser, choose **Copy to profile…** from a mod's menu in the
library, or on the mod's panel, and pick the profile to copy it to. If that
profile already has the mod, the confirm button reads **Replace**.

## Grouping mods by category

Every mod can have a category. Mods from NexusMods and CurseForge come with
the category the site files them under, such as "Weapons" or "User
Interface". You can give any mod a category of your own, which replaces the
site's. Your categories are saved in the profile, so they go with it when
you duplicate, copy, export or import it. A mod installed before lmm knew
about categories gets its site's category at the next update check.

Groups are only a way of looking at the list. They are sorted by name, with
the mods that have no category last, under **Uncategorized**. They don't
change the load order: inside a group the mods keep their load order, and
**Reorder…** always shows the full list in load order.

In the terminal:

```bash
lmm mod set-category 12345 Gameplay
lmm mod set-category 12345 --clear
lmm list --group-by category
```

`--clear` removes your category, and the site's applies again.
`lmm list --group-by category` lists the mods under one heading per
category. `lmm mod show` says which category a mod has and whether it's
yours or the site's. With `--json`, `lmm list` still prints one flat list,
and each mod in it carries its category.

In the browser, tick **Group by category** above the library. Click a
group's heading to fold it away. The search box, Filter and Sort still
apply: they choose which mods are shown and in what order, and the groups
are made from those. lmm remembers both the setting and the groups you
folded, in this browser.

To set a mod's category, open the mod's panel, or choose **Set category…**
from its menu in the library. Type a name or pick one of the categories
already in use, then **Save**. The line under the field says where the
category comes from: the mod's site, or your own. **Clear** removes your
own category, so the site's applies again.

## Sharing a profile

Exporting a profile saves it as a **collection**: a small file listing its
mods, their versions and your settings for them. Send it to a friend, or keep
it as a backup, and importing it on another computer downloads and installs
the same mods at the same versions.

```bash
lmm profile export coop > coop.yaml
lmm profile export coop -o coop.lmmc
lmm profile import coop.lmmc
```

Without `-o`, the export is printed as text (YAML), which you can save,
compare or keep in git. `-o` saves a compressed `.lmmc` file instead, or a
text file if you add `--plain`. Both import the same way, and so does a
file exported by an older version of lmm. One exception: a profile
imported from a NexusMods collection whose mods are patched carries those
patches only in a `.lmmc` file, so share that one (the export says so).

In the browser, **Export** and **Import** are in the Profiles window, and
**Import collection…** in the profile picker's menu opens the same import
on its own. **Export** downloads a `.lmmc` file. **Import** takes the same files the
terminal does, of any size it does: choose the file, and the import shows
its plan before anything is written. To import a collection under another
profile name (what `--as` does), type the name into the **Profile name**
field under **From a file** before pressing **Choose file…**. The import goes into the game you have
selected, as `--game` does, and the plan warns when the collection names a
different game. The plan has a box for each thing an import holds back
(see below): **Import these hooks** for `--with-hooks`, and **Import these
overrides too** for `--allow-any-overrides`.

A collection names its game in a way that works on any computer (its Steam
app id, and its id on each mod site), so the import finds the right game
even if you named it differently. If it can't tell which of your games is
meant, add `--game <your game id>`.

You can describe the collection for the people you share it with:

```bash
lmm profile export coop -o coop.lmmc --title "Co-op night" \
  --author me --description "What we play on Fridays." --collection-version 2
```

lmm remembers these for the profile, so next time you only give what
changed, such as `--collection-version 3` when you publish an update.

### Sharing your own files with a collection

A collection never contains mods from NexusMods, CurseForge or any other
site: your friend downloads those with their own account. Files you made
yourself, such as a compatibility patch or a pack of config tweaks, can
travel inside the `.lmmc` file, so your friend gets them with the
collection.

First add your files to the profile as a mod, by importing them from an
archive (see
[A mod that isn't on any site](manual-mods.md#a-mod-that-isnt-on-any-site)).
That way they are deployed and you can test them like any other mod, and
they keep their place in the load order. Then name that mod with `--bundle` when you export:

```bash
lmm import my-patch.zip --profile coop
lmm profile export coop -o coop.lmmc --bundle "my-patch"
```

`--bundle` takes the mod's name, its id, or `source:id` as `lmm list`
shows it, and you can give it more than once. It only accepts mods you
imported from a file: lmm refuses to bundle a mod that came from a mod
site. Bundled files only fit in a `.lmmc` file, so `--bundle` needs `-o`
and doesn't work with `--plain`.

In the browser, use **Export with my files…** in the Profiles window: tick
the mods whose files are yours, then **Export**.

{: .note }
Bundle only files you made, or that you are allowed to share.

When your friend imports the collection, the plan lists each bundled item
with how many files it has and their size, and lmm installs it like any
other mod. lmm keeps a copy of the collection file, so `lmm verify --fix`
can rebuild the files later. A bundled item has no updates of its own: a
new version arrives with a new version of the collection. If they export
the profile again, the bundled files go with it.

{: .note }
lmm keeps every collection file with bundled items that you import, in
the game's cache folder under `collections/`, and doesn't remove them when
you delete a profile. They are usually small; to free the space, delete
the `collections/lmm-*` folders there, and import a collection again if a
profile still uses its bundled files.

{: .note }
The first export of a profile saves its collection id in the profile, and
that waits for any other lmm operation, such as a job running in the
browser. If it can't, the export stops and asks you to try again, and a
file you were writing with `>` is left empty; run the export again once
the other operation has finished.

The import shows this before it does anything, along with:

- **items to get by hand**, which lmm lists but can't download for you.
- mods from a site you haven't set up in lmm, and what to do about them.

A collection made by a newer version of lmm that needs something this
version can't do is refused, and nothing is imported. Update lmm and try
again.

### What an import holds back

Every import from a file, whether a collection or an older export, lists
two things before it does anything, and leaves them out unless you ask (an
import from a link never imports hooks at all; see
[Importing a collection from a link](#importing-a-collection-from-a-link)):

- **Hooks**, the scripts the profile runs. They are only imported if you
  add `--with-hooks` (in the browser, tick **Import these hooks**), so a
  file from someone else can't run anything you didn't agree to. This
  includes your own older exports: to keep their hooks, import them with
  `--with-hooks`.
- **Overrides that aren't config files.** An override is a file the
  profile writes into the game folder. Text config files (`.ini`, `.cfg`,
  `.conf`, `.txt`, `.json`, `.xml`, `.toml`, `.yaml`, `.yml`,
  `.properties`, `.config`, `.ltx`, `.lsx`, `.cvars`, `.ron`) are imported
  as usual; anything else, such as a `.dll`, is skipped unless you add
  `--allow-any-overrides` (in the browser, tick **Import these overrides
  too**). So is a loader configuration, such as `doorstop_config.ini`
  (BepInEx) or `enblocal.ini` (ENB): it is a text file, but it can make the
  game load a DLL. This includes your own older exports: to keep such an
  override, import them with `--allow-any-overrides`.

- **Bundled files that are programs.** A collection's own files (see
  [Sharing your own files with a collection](#sharing-your-own-files-with-a-collection))
  are imported as usual, unless one holds something the computer can run
  as a program: a `.dll`, an `.exe` or another executable, a system script
  such as a `.sh` or `.bat`, or a loader configuration. Such an item is
  skipped unless you add `--allow-any-overrides` (in the browser, tick
  **Import these bundled items too**). The plan names the files that hold
  it back.
- **Patches to programs.** A collection can patch its mods' files (see
  [Importing a NexusMods collection](#importing-a-nexusmods-collection)).
  A patch to a program (a `.dll`, an `.exe`, a system script or a loader
  configuration) is listed in the plan, and the import is refused unless
  you add `--allow-any-overrides` (in the browser, tick **Apply these
  patches**): leaving out the patch would install a mod that isn't the one
  the collection was made with. The same goes for a patch lmm keeps from
  one collection that a profile file names for a different file, since a
  patch can turn a file into anything: an older export or a hand-edited
  profile is held to this as a collection is.

When something was held back, the import says so at the end and names the
option that would include it.

```bash
lmm profile import coop.yaml --with-hooks
lmm profile import coop.lmmc --with-hooks --allow-any-overrides
```

{: .note }
If a profile came from someone else's collection, exporting it keeps their
name on it. Whether to share it further is up to you.

`--as <name>` imports a collection under another profile name.

An import downloads every mod before it saves anything. You see the
**Installing** and download lines for each mod first, then the profile is
saved and the **Installed** lines follow. If one mod can't be downloaded,
that mod fails on its own and the rest are imported as usual. But if a mod
turns out to need something you have to set up first, such as
[BepInEx](bepinex.md), nothing is imported: not the profile, not the other
mods. lmm says what to do and keeps the files it downloaded, so running the
import again after you've fixed it doesn't download them again. (Steam
Workshop items that lmm fetches through steamcmd are the exception: those
are fetched again.)

{: .tip }
To make sure a friend gets *exactly* the version you have, lock the mod
first (see [Locking a mod](updates.md#locking-a-mod-to-one-version)). Locks
are saved in the exported file.

### Importing a collection from a link

A collection someone has put online (a gist, a file in a git repository, a
download link) imports straight from its address:

```bash
lmm profile import https://example.com/coop.lmmc
lmm profile import https://example.com/coop.yaml --as coop-friday --game cp2077
```

In the browser, paste the address into **From a URL** (in the Profiles
window, or **Import collection…** in the profile picker's menu) and press
**Import from URL**; or paste a `.lmmc` address into the search box and
press **Import it…** on the offer above the results. The address must be `https://`, and
it must lead to the file itself (a `.lmmc`, its YAML, or an older export),
not to a web page about it. lmm downloads at most 64 MiB for a `.lmmc` and
4 MiB for a YAML file, and won't follow a link that sends it on to a plain
`http://` address.

An import from a link differs from one from a file in two ways:

- **Hooks are never imported**, even with `--with-hooks`. The plan still
  lists them, so you can see what the collection would have run, and the
  import says at the end that they were left out. If you trust them,
  download the file and import it with `--with-hooks`.
- **The profile remembers the address**, so a later update of the
  collection can fetch it again. A `user:password@` part of the address is
  used for the download and never saved. Only the profile file keeps the
  whole address; everywhere else (the plan, `--json` output, the browser,
  messages) lmm shows it without anything after a `?`, in case that holds
  a token.

The overrides rule is the same as for a file. An older export imports into
the game you name with `--game`, or else the game its `game_id` names (in
the browser, the selected game). Unlike an older export from a file, one
from a link takes `--as` (in the browser, the **Profile name** field), since
it imports the way a collection does.

A Steam Workshop **collection** can be imported as a profile too, by pasting
its link into **Steam Workshop collection** in the Profiles window (offered
when the game uses the Steam Workshop), or with
`lmm profile import --workshop-collection <link>`.

### Importing a NexusMods collection

A NexusMods **collection** imports as a new profile: lmm reads the
collection's list and its install recipe, turns them into an lmm
collection, and imports that.

```bash
lmm profile import --nexus-collection iszwwe --game cyberpunk2077
lmm profile import --nexus-collection iszwwe@484 --game cyberpunk2077
lmm profile import --nexus-collection https://www.nexusmods.com/games/skyrimspecialedition/collections/xk05aw --as essentials
lmm profile import --nexus-collection xk05aw --with-optional
lmm profile import --nexus-collection xk05aw --optional 3863:1
lmm profile import --nexus-collection https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe/revisions/484
```

Give the collection's slug (the code at the end of its page's address),
`slug@revision` for one revision, or the page's address; the address of
one revision's page (ending `/revisions/484`) imports that revision, as
`iszwwe@484` does. An `http://` page address works as well as `https://`:
lmm only reads the collection's name from it, and always talks to
NexusMods over https. Without a
revision you get the latest one, and the plan says which. A link to
another game's collection is refused. In the browser, type the same thing
into **NexusMods collection** (in the Profiles window, or **Import
collection…** in the profile picker's menu; it is offered when the game
uses NexusMods), or paste the collection's
page address into the search box and press **Import it…** on the offer
above the results. The search box recognises the same slugs and
addresses the terminal takes.

To find a collection rather than import one you already know, search the
game's NexusMods collections:

```bash
lmm search --collections "survival" --game skyrim-se
lmm search --collections --game skyrim-se --sort popular --limit 20
```

Each result shows the collection's name, its curator, its endorsements,
how many mods its latest revision holds, that revision's number, and its
page address: pass the address to `lmm profile import --nexus-collection`.
Without a search term you get the game's collections in the order
`--sort` names (relevance, updated, downloads or popular); `--limit` sets
how many (up to 100), and `--json` prints them as a document. Searching needs no API
key. Adult collections are listed, as NexusMods lists them, and marked
`[adult]`. In the browser, search as usual and choose the **Collections**
tab on the search page (or **Search collections** under the search box's
results; **Browse this game's NexusMods collections** in **Import
collection…** lists them all): each collection has **Import…**, which
opens **Import collection…** with its address filled in, so you can name
the profile before the plan.

You need a NexusMods API key (see [Your first mods](first-mods.md)), and
lmm needs the `7z` command, because the recipe is a `.7z` file (see
[Installing lmm](install.md#optional-7-zip)); the plan says so if `7z` is
missing. A **free** account can read any
collection, but NexusMods only serves downloads to Premium accounts, so on
a free account the plan marks every mod as a manual download and links its
file's page. The import then installs only what needs no download (a mod
another of your profiles already has) and saves the profile with the rest
still to download: get each file from its page, then install them all at
once with `lmm profile fill <profile> ~/Downloads` (in the browser, drop the
files on the import's entry in the activity tray, or use **Fill from
downloads…** on the profile in **Manage profiles**). lmm recognises each
file by the checksum the collection records; see [A NexusMods collection on
a free account](manual-mods.md#a-nexusmods-collection-on-a-free-account).
lmm asks NexusMods for your account's tier
each time it plans a collection, so after upgrading to Premium, plan again.

The plan leads with a summary: how many mods will be installed, how many
are optional (and how many of those you picked), how many installers will
ask you questions, how many to download by hand, how many items are
external or prerequisite steps, and anything lmm cannot import. Then come
the **optional mods**, each with its version, the curator's words for it,
its file page and its id; then **Needs your attention**: mods whose
installer will ask (the collection records no answers for it), mods pinned
to an old or archived file, items lmm does not fetch, and prerequisite
steps such as SMAPI, each with why. Last is **every item**, in the
collection's order, with what the import does with it. In the browser the
summary's counts go to their section, the optional mods have **Select
all** and **Select none**, and the full list is folded away until you open
it. A collection update's plan groups its items the same way.

What the import does with each item:

- Each mod is pinned to the **exact file** the curator chose, even an old
  or archived one (NexusMods still serves an archived file, and the plan
  notes that it is archived). A mod the curator pinned exactly is
  [locked](updates.md#locking-a-mod-to-one-version).
- A pinned file **NexusMods no longer offers** (its author removed it, as
  many authors do with every update) is found by the plan,
  before anything downloads, and listed under **Needs your attention**
  with the current file that would stand in for it. The stand-in plays
  the gone file's role: a gone main file (or a mod whose every pinned file
  is gone) takes the mod's current main file, and the plan says which of
  its optional files that does not replace; a gone optional file beside a
  main file that is still offered takes the current file of the same kind
  whose name matches it, when exactly one does. lmm never puts the current
  main file beside the curator's. The stand-in is the **default**: the plan
  says "Ten 1.0 is no longer on NexusMods; installing the current 2.0
  instead (--skip 10 to leave it out)", and when the curator pinned that
  version exactly it says so ("the curator pinned 1.0 exactly"), since the
  curator never tested the newer file. (Vortex would not install it; lmm
  does unless you skip it.) `--skip <mod-id>` leaves the mod out,
  `--skip-file <mod-id>` leaves a gone optional file out and installs the
  rest of the mod, and `--use-current <mod-id>` (or `--use-current-all`)
  names the default explicitly. In the browser the stand-in is preselected,
  with Skip beside it. Naming a mod whose pinned file is not gone, or a
  choice its pin does not offer, is refused. A gone file nothing stands in
  for does not stop the import: the mod is left to finish (see
  [Finishing a collection](#finishing-a-collection)). A mod
  installed at its current file is still the collection's,
  and lmm remembers which file it stands in for; a skipped mod is
  remembered too (see [Updating a collection
  profile](#updating-a-collection-profile)). Installer choices the curator
  made against the old file are not replayed on the new one: its installer
  asks.
- When the curator asks for a mod's **latest** file, lmm installs the file
  the collection was tested with now, and sets the mod to update
  automatically, so the next update check moves it to the newest file.
- **Optional** mods are left out unless you ask: `--with-optional`
  includes all of them, and `--optional <id>` one of them, by the id the
  plan lists (repeat it for more). An id that is not one of the
  collection's optional mods is refused, and the message lists the ones
  that are. In the browser each optional mod has its own tick box in the
  plan.
- Mods are put in the collection's install order, and the curator's
  "load after" rules are followed. A rule that names a mod outside the
  collection is skipped, and the plan says so.
- **Installer choices** the curator made for a FOMOD installer are
  replayed, as they are for any [FOMOD](fomod.md) mod.
- A mod the curator gave a **type** goes where that type belongs, on a
  game whose adapter knows it: a script extender to the game folder, a
  Starfield "Data folder" mod into `Data/`, a Witcher 3 top-level mod to
  the game folder (its `mods/` into the game's mods folder), a Baldur's
  Gate 3 loose-file mod into `Data/`. A type the game's adapter doesn't
  know refuses the collection, as below; Witcher 3's DLC and menu mod
  types are not supported yet.
- **SMAPI**, which Stardew Valley mods load through, is installed with its
  own installer, not by lmm. The plan lists it as a step for you, with its
  page and how to run the installer, and imports the rest of the
  collection.
- The curator's **plugin load order** (a Bethesda game's `.esm`, `.esp`
  and `.esl` files: which load, in what order, and which are left off)
  becomes the profile's plugin list, with the curator's plugin rules and
  groups. The plan shows how many plugins it holds, which are inactive and
  the rule groups, and lists the whole order behind **Show the load order**
  in the browser (the terminal's `--json` plan has it under
  `profile.plugins`). lmm writes it to the game's `Plugins.txt` at every
  deploy, for a game whose plugin list it manages (`adapter: bethesda`); on
  any other game such a collection is refused. A curator who marked their
  rules not to be applied gets the order without them, and the plan says
  so. If the curator's rules contradict each other (a cycle no order can
  satisfy), the plan warns: the import still goes ahead, but every deploy
  leaves `Plugins.txt` as it was and names the cycle until you change one
  of the rules in the profile's `plugins:`.
- The curator's **archive load order** (Cyberpunk 2077's `modlist.txt`:
  which archive wins when two change the same thing) becomes the profile's
  archive order. The plan lists it, with the mod each archive comes from
  where the collection says (the browser behind **Show the load order**;
  the terminal shows the first ten, and the `--json` plan has them all
  under `profile.load_orders`). lmm writes it at every deploy, on a game set
  up with `adapter: cyberpunk2077`; on any other game such a collection is
  refused. See [Archive load order](load-order.md).
- A mod the curator installed with an **exact file list** (Vortex's
  "replicate" mode) installs only the files on that list, each checked
  against the checksum the collection records; the plan says how many.
- The curator's **patches** to a mod's files are applied as the mod
  installs. A patch only applies to the file it was made against: if your
  copy is different, that mod is not installed, rather than installed
  unpatched (Vortex would warn and carry on); it is left to finish. The
  plan lists every patch; a patch to a program needs
  `--allow-any-overrides` (see [What an import holds back](#what-an-import-holds-back)).
- Files the curator **bundled** in the collection itself install like any
  mod, even on a free account, since there is nothing to download; lmm
  keeps them with the collection's recipe so it can rebuild them later.
  One that holds a program is skipped unless you add
  `--allow-any-overrides`, as a shared collection's bundled files are.
- Items NexusMods doesn't host are listed with where to get them and the
  curator's instructions. The curator's tool shortcuts are listed too; lmm
  never creates or runs them.

A collection import is **never all or nothing**. It installs every mod it
can, and each one it cannot - a download that failed, an installer whose
recorded answers no longer fit, a pinned file nothing stands in for, a
file to download by hand - stays in the profile, in its place in the load
order, as an item left to finish, with why and what resolves it. The
import never stops part-way to ask. It ends with a summary:

```text
Imported 998 of 1000 mods; 2 need you:
  ! FSMP (nexusmods:57339): FSMP's installer changed since the curator recorded their choices; answer it to continue
      lmm profile finish "Skyrim Plus" --retry 57339
  ! USSEP (nexusmods:266): the download of USSEP failed; retry it
      HTTP 503 from the CDN
      lmm profile finish "Skyrim Plus" --retry 266
Finish the collection with:
lmm profile finish "Skyrim Plus"
```

The last line is always the command to run next, and the exit status is
non-zero while anything is left to finish - except a free account's files
to download by hand, which is how that import is meant to end. `--fomod-choices <file>` or
`--fomod-defaults` on the import answers the installers once everything
else is installed. A collection that uses something this version of lmm
can't do yet (a patch in a format other than bsdiff's, a plugin rule
marking two plugins incompatible) shows its plan, with what is missing and
which mods need it, and is refused, with nothing written. Optional mods
that need such a thing are skipped instead.

In the browser, a running import shows its progress in a card at the
bottom right of the page (**Importing collection…**, with how many of the
collection's mods it has reached). The card stays on the other pages of
the web UI, and comes back if you reload the page while the import runs.
When it ends the card says how it went: "Imported 40 mods", or, when
mods are left to finish, "Imported 38 of 40 mods; 2 need you" in amber
rather than green, with **Finish collection** first. Into a profile that
is not the active one it says "Recorded", as the CLI does. **Show in activity**
opens the import's entry in the activity tray (on the Setup page it is not
offered, so an unsaved draft there is not lost); the entry offers
**Finish collection** too. With `--json`, an
import's result lists the mods left to finish under `todos`, each with
its `reason` and `action`.

### Finishing a collection

`lmm profile finish` lists a collection profile's mods left to finish,
each with why, what resolves it, and the command for it, and resolves
them:

```bash
lmm profile finish "Skyrim Plus"
lmm profile finish "Skyrim Plus" --retry all
lmm profile finish "Skyrim Plus" --retry 57339 --fomod-defaults
lmm profile finish "Skyrim Plus" --use-current 266
lmm profile finish "Skyrim Plus" --skip-file 105
lmm profile finish "Skyrim Plus" --skip 999
lmm profile finish "Skyrim Plus" --from ~/Downloads
```

- `--retry <mod-id>` installs the mod again; `--retry all` retries every
  mod on the list. An installer that asks is answered at the terminal, or
  with `--fomod-choices` or `--fomod-defaults`.
- `--use-current <mod-id>` installs a gone pin's mod at its current main
  file, if it has one now, and `--skip-file <mod-id>` installs it without
  its gone optional files. A mod NexusMods no longer has at all can only
  be skipped.
- `--skip <mod-id>` leaves the mod out. lmm remembers the skip, so a
  collection update doesn't add the mod back without asking.
- `--from <folder>` installs the files you downloaded by hand from that
  folder, as `lmm profile fill` does.

Each mod resolves on its own, in its place in the load order; one that
fails again stays on the list with the new reason. A file an earlier
attempt downloaded is installed without downloading it again ("Reusing
1 downloaded file(s)"). `--dry-run` shows what it would do, and `--json`
prints the list (or the plan, or the result).

In the browser, **Finish collection** opens a list of the mods left to
finish ("2 of 40 need you"), each with why and its one action beside **Skip…**:
**Answer installer…** (the installer's questions open in the installer
wizard once you confirm), **Use current file…** or **Skip file…** for a
gone pin, **Retry…**, or **Download by hand…**, which opens the same
folder chooser and drop zone as **Fill from downloads…**. A mod its source
deleted offers only Skip. **Retry all…** retries everything on the list.
Each opens a plan to confirm first, which says how many downloaded files
it reuses; the list comes back afterwards, with the job's progress at the
top, and refreshes when it ends. Once the list is empty, the import's
activity entry stops offering Finish collection and the bell stops counting
it. Finish collection is on the Profile card
("Finish collection: 2 of 40 need you"), on the profile's row in **Manage
profiles…**, and on the import's own card and activity entry.

You can deploy, play, switch to or apply an unfinished collection: each
warns "collection incomplete: N item(s) unresolved (...); the game may
not behave as the curator intended", and leaves the mods left to finish
alone. `lmm status` counts them. In the browser the deploy, switch and
apply plans say "Collection incomplete" and name each mod left; the
Profile card says how many are left, and **Play** says so on its notice,
with **Finish collection** beside it.

### Updating a collection profile

A profile imported from a link or from a NexusMods collection remembers
where it came from, and updates from there when the collection changes:

```bash
lmm profile sync coop --collection --dry-run
lmm profile sync coop --collection
lmm profile sync coop --collection --remove-obsolete --override 3863
```

In the browser, **Update collection…** appears beside such a profile in
the Profiles window. A profile imported from a link fetches the address
again: if it now serves the same collection (its `id`) at a newer
`version`, that is the update; the same version means there is nothing to
do, and another collection, or an older version, is refused. A NexusMods
collection updates to its newest revision. A profile imported from a file
has nowhere to update from: import the new file with `--force`.

The plan lists:

- **mods the new version adds**, which the update installs;
- **mods whose pin it moves** (version, files, lock, installer choices or
  type). A mod the collection brought in follows the collection, even when
  it is locked: that lock is the collection's;
- **mods it no longer lists**. They are kept, as your own mods, unless you
  choose to remove them: name one with `--remove <mod-id>`, or all with
  `--remove-obsolete`. In the browser each has a **Remove and uninstall**
  tick box. lmm never removes one silently;
- **conflicts**: mods you changed yourself that the new version changes
  too. Any deliberate change makes a collection's mod yours: locking or
  unlocking it, updating it (`lmm update`, or **Update** in the browser),
  rolling it back, updating it from a file, re-answering its installer
  ([Reconfigure installer…](fomod.md)), reinstalling it at another version
  or with other installer answers, setting it to update automatically
  (`lmm mod set-update --auto`), or re-linking it to another mod.
  Your version is kept unless you choose the collection's:
  `--override <mod-id>`, or `--override-conflicts` for all; in the
  browser, **Take the collection's version**. An update that the mod's
  automatic update policy makes on its own (a collection asking for a
  mod's latest file sets it) is not a change of yours, and the mod stays
  the collection's.

A pin whose files change while its version stays the same reinstalls the
mod with the new files, and the plan says so.

A mod the import installed at its current file, because the pinned one was
gone, stays as it is while the new version still pins that gone file, and
moves to the curator's new pin when there is one. A mod you skipped stays
out while the new version pins the same file (the plan lists it as
skipped). When the curator pins it at a file NexusMods offers, the plan
lists it under **Needs your attention** and the update adds it back;
`--keep-skipped <mod-id>` keeps it out (in the browser, its **Keep it
skipped** tick box), and lmm remembers that pin, so the next update offers
the mod again only when the curator pins another file. A new pin NexusMods
no longer offers is handled as at an import: the current file stands in
by default, `--skip-file` or `--skip` decide otherwise (skipping a mod the
profile already has keeps the version it has), and one nothing stands in
for is left to finish. For a mod you changed yourself (a conflict), your
version is kept unless you take the collection's with `--override`; then
the current file stands in by default, and if nothing does, your version
is still kept, and the update says so.

The plugin list, file load orders (Cyberpunk 2077's archive order, see
[Load order](load-order.md)) and config overrides follow the same rule. lmm remembers
what the collection last applied, so it can tell your changes from the
collection's: a plugin you switched off or moved, a plugin you added, or an
override you edited stays yours, and the new version's changes apply to
everything you left alone. Where you and the collection both changed the
same thing, the plan names it as a conflict and keeps yours; take the
collection's with `--override plugin:<name>`, `--override plugins:order`,
`--override plugins:rules`, `--override load_order:<id>` or
`--override override:<path>` (the plan
prints each key), or `--override-conflicts` for all of them. In the
browser each has its own **Take the collection's** tick box. An override
the collection no longer carries is removed if you left it as the
collection wrote it.

If an obsolete mod you chose to remove can't be uninstalled (an uninstall
hook refuses, say), the update still lands, the mod stays in the profile
and installed as your own, and the result says so.

Mods you added yourself are left alone and keep their place in the load
order. The new version passes the same checks as an import: a feature this
lmm lacks refuses the update with nothing changed, hooks are listed and
never applied (the profile keeps its own), and overrides that aren't text
config files are held back unless you pass `--allow-any-overrides` (in the
browser, **Import these overrides too**). The same option lets through
bundled files that are programs and patches to programs: a new version
that patches a program is refused without it (in the browser, **Apply
these patches**), since lmm never updates a mod unpatched. For a NexusMods collection,
`--with-optional` and `--optional <id>` include optional mods, and an
optional mod you already have stays in; in the browser, unticking one in
the plan leaves it out of the new version, where it is listed with the
mods the collection no longer has. Like an import, an update installs
what it can and leaves the rest to finish (see [Finishing a
collection](#finishing-a-collection)). Once no profile uses the old revision (of any game
that shares its cache folder), lmm drops its recipe from the cache.

## An override of a mod's own file

An override can change a file that a mod ships, such as a mod's `.ini`.
On deploy, lmm replaces that one file in the game folder with a copy
holding the override, whichever link method the profile uses; the mod's
cached copy, which every profile shares, is never changed. Deploying
again leaves the copy as it is, and uninstalling, disabling or purging the
mod removes it with the rest of the mod's files.

`lmm verify` lists the file as "mod file, overridden by the profile"
(`deployed_overridden` in `--json`). That is not a problem, and
`lmm verify --fix` leaves it. In the browser, the Health card shows it
with an **Overridden** badge.

If the file there is your own - you edited the overridden copy, or
replaced the mod's file before adding the override - lmm sets it aside
before writing the override, and says so. Removing the mod, or
`lmm purge`, puts your file back.

To go back to the mod's own file, take the override out of the profile and
deploy or apply it again. Switching to a profile without the override does
the same. Until you do, `lmm verify` reports the leftover copy as `override_stale`,
and `lmm verify --fix` (or **Repair** on the Health card) puts the mod's
own file back.

## Overrides when you switch or apply a profile

A profile's overrides are part of what it describes, so `lmm profile
switch` and `lmm profile apply` (in the browser, **Switch and deploy…** and
**Apply profile…**) put them in place as `lmm deploy` does:

- The profile's overrides are written. An override over a file the game
  shipped, such as an INI, keeps the game's own file aside first.
- An override the profile doesn't have any more is taken away: the game's
  own file comes back, or a file the override created is removed. A mod's
  own file goes back to the mod's.
- If you changed a file after lmm wrote the override, lmm leaves your file
  as it is and says so. The game's original stays kept.
- If another override is written over a file you changed, lmm keeps your
  file first and names where it is: under
  `snapshots/<game>/_originals/set-aside/` in lmm's data folder
  (`~/.local/share/lmm`). Copy it back from there if you want it.

`lmm deploy` takes an override you removed from the profile away the same
way, and its plan lists the game files its overrides change.

The plan lists each file under "Will change N file(s) for the profile's
overrides" in the terminal, and under **Profile overrides** in the
browser, before you confirm. In `--dry-run --json`, the plan's `overrides`
list has one entry per file, with its `path`, its `root` (`install_path`
for a game file) and its `action`: `place`, `update`, `replace_changed`
(your changed file is kept first), `restore`, or `keep` (your changed file
is left as it is, which is no change). A
switch or apply that only changes overrides still has changes, so it asks
you to confirm (`--yes` skips the question).

## "Your profile and the game don't match"

A profile is the list of what *should* be installed. If the two drift apart,
for example after you edit a profile file by hand, lmm's main screen shows a
**Profile** card with two ways to fix it:

- **Apply profile…** changes the game to match the profile, installing,
  removing, upgrading or downgrading mods as needed.
- **Sync…** does the opposite and updates the profile to match what's
  installed.

The terminal versions are `lmm profile apply` and `lmm profile sync`.

For the active profile, the card offers **Apply profile…** whenever an
apply would change something, and says what: for example "Differs from
what is installed: 2 mods at another version, 1 override changed". The
reasons it names are mods not installed, at another version, of another
mod type, with other files, with other installer choices, or not
enabled; mods enabled but not in the profile, or enabled when the profile
switches them off; and override files that differ. `lmm status --game
<game>` shows the same line under the active profile, and `lmm status
--game <game> --json` carries the counts as `profile_drift` (absent when
nothing differs). Working these out reads only the profile and lmm's own records,
never a download. An override is the one reason that looks at the game
directory, and lmm remembers that answer until the next change it makes
or the next edit of a profile file. So if you edit an override's file in
the game folder yourself, the card may not notice until then; the
**Apply profile…** plan always compares afresh.

A mod the profile pins to another version, other installer answers or
other files than the ones installed is reinstalled the same way, by an
apply or a switch to the profile. A file swapped for another at the same
version shows in the plan as `reinstall: files changed`. The order the
files are listed in doesn't matter.

A profile entry can still list copies of one main file from more than one
version of a mod: one recorded before lmm checked this, or edited by hand.
When an apply, a switch or an import installs that mod, it keeps the copy
at the version the entry records (the newest copy, if none is at that
version) and drops the others; a separate part of the mod at its own
version ("Textures 1.0" beside "Core 1.2"), and every optional, update and
miscellaneous file, stays. The plan lists each such mod under **Stored
files of more than one version** (`... have stored main files of more than
one version` in the terminal) with the version kept, the files dropped
and any separate part kept, and the profile records the files kept, so the
next apply has nothing to do. To use a dropped copy instead, reinstall the
mod at that copy's version. A switch or import plan looks up at most 25
such mods and says how many it left; the rest are healed when the run
reaches them, with a warning. A deploy that has to download the mod again,
or an update, does the same and says so in a warning; neither drops a
separate part. A collection's own entry is the exception: the curator
chose those files, so they install as they are, and the apply plan lists
the mod under **Collection files of mixed versions**.
