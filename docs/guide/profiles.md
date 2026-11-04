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

Exporting a profile saves it as a small text file. Send it to a friend, or
keep it as a backup, and importing it on another computer downloads and
installs the same mods at the same versions.

```bash
lmm profile export coop > coop.yaml
lmm profile import coop.yaml
```

In the browser, **Export** and **Import** are in the Profiles window.

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
