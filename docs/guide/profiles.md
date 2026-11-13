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
**Export** downloads a `.lmmc` file.

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
two things before it does anything, and leaves them out unless you ask:

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
  too**).

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

A Steam Workshop **collection** can be imported as a profile too, by pasting
its link into the Profiles window, or with
`lmm profile import --workshop-collection <link>`.

## "Your profile and the game don't match"

A profile is the list of what *should* be installed. If the two drift apart,
for example after you edit a profile file by hand, lmm's main screen shows a
**Profile** card with two ways to fix it:

- **Apply profile…** changes the game to match the profile, installing,
  removing, upgrading or downgrading mods as needed.
- **Sync…** does the opposite and updates the profile to match what's
  installed.

The terminal versions are `lmm profile apply` and `lmm profile sync`.
