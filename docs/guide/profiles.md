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
  rename, delete, export and import profiles.

## In the terminal

```bash
lmm profile list
lmm profile create coop
lmm profile switch coop
```

`lmm profile list` shows every profile for the game and which one is
active. Commands like `lmm install` and `lmm list` work on the active
profile unless you say otherwise.

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
