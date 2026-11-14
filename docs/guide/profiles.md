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
file exported by an older version of lmm.

In the browser, **Export** and **Import** are in the Profiles window.
**Export** downloads a `.lmmc` file. **Import** takes the same files the
terminal does, of any size it does: choose the file, and the import shows
its plan before anything is written. To import a collection under another
profile name (what `--as` does), type the name into the **Profile name**
field before choosing the file. The import goes into the game you have
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

In the browser, paste the address into **From a URL** in the Profiles
window and press **Import from URL**. The address must be `https://`, and
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
its link into the Profiles window, or with
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
```

Give the collection's slug (the code at the end of its page's address),
`slug@revision` for one revision, or the page's address. Without a
revision you get the latest one, and the plan says which. A link to
another game's collection is refused. In the browser, type the same thing
into **NexusMods collection** in the Profiles window.

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

The plan lists every item and what the import does with it:

- Each mod is pinned to the **exact file** the curator chose, even an old
  or archived one. A mod the curator pinned exactly is
  [locked](updates.md#locking-a-mod-to-one-version).
- When the curator asks for a mod's **latest** file, lmm installs the file
  the collection was tested with now, and sets the mod to update
  automatically, so the next update check moves it to the newest file.
- **Optional** mods are left out unless you ask: `--with-optional`
  includes all of them. In the browser each optional mod also has its own
  tick box in the plan; the terminal has no per-mod choice.
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
- Items NexusMods doesn't host are listed with where to get them and the
  curator's instructions. The curator's tool shortcuts are listed too; lmm
  never creates or runs them.

A NexusMods collection installs **all or nothing** while its files are
fetched: if one mod can't be downloaded, nothing is imported, not even the
profile. If something goes wrong after that, while the downloaded mods are
being installed, the import says which mods failed, as any import does. And a collection
that uses something this version of lmm can't do yet (an exact file list,
files bundled in the collection, patches, a plugin rule marking two plugins
incompatible) shows its
plan, with what is missing and which mods need it, and is refused, with
nothing written. Optional mods that need such a thing are skipped instead.

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
deploy again. Switching to a profile without the override does the same.
Until you do, `lmm verify` reports the leftover copy as `override_stale`,
and `lmm verify --fix` (or **Repair** on the Health card) puts the mod's
own file back.

## "Your profile and the game don't match"

A profile is the list of what *should* be installed. If the two drift apart,
for example after you edit a profile file by hand, lmm's main screen shows a
**Profile** card with two ways to fix it:

- **Apply profile…** changes the game to match the profile, installing,
  removing, upgrading or downgrading mods as needed.
- **Sync…** does the opposite and updates the profile to match what's
  installed.

The terminal versions are `lmm profile apply` and `lmm profile sync`.
