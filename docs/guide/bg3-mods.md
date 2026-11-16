---
title: Pak mods and their order (Baldur's Gate 3)
nav_order: 9.6
---

# Pak mods and their order (Baldur's Gate 3)

Most Baldur's Gate 3 mods are **paks**: files ending in `.pak` in the
game's `Mods` folder. A pak in that folder does nothing on its own. The
game loads it only while its **mod list**, a file called `modsettings.lsx`,
names it. The mod list also decides the order the paks load in. When two
paks change the same thing, the one lower in the list (loaded later) wins.

lmm writes the mod list for you. Each time it deploys your mods, it adds
the paks it installed and keeps them in the order you chose.

Both files live in the game's Wine/Proton prefix, under
`drive_c/users/steamuser/AppData/Local/Larian Studios/Baldur's Gate 3/`.
The paks go in its `Mods` folder, and the mod list is
`PlayerProfiles/Public/modsettings.lsx`. See
[Adding a game by hand](add-a-game.md#baldurs-gate-3s-pak-mods) for where
lmm puts each kind of file, and how it finds the prefix.

## Which games

Baldur's Gate 3 set up with the `baldurs-gate-3` adapter. lmm sets this
when it finds the game on Steam. For a copy you added by hand, run
`lmm game edit <game> --adapter baldurs-gate-3`.

## Run the game once first

The game writes `modsettings.lsx` the first time it starts. Until it has,
lmm can't enable any paks. It doesn't make up the file itself, because
what goes at the top depends on the game's version. A deploy before then
installs the paks and warns that they are not enabled yet. Start the game
once, quit, then run `lmm deploy` (or `lmm verify --fix`) and lmm lists
them.

## What lmm writes

Each pak carries a small description of itself, `meta.lsx`, with the name
and ID the mod list needs. lmm reads it from the pak and adds one entry
for each pak it installed.

- **The game's own entry stays first.** The list always starts with an
  entry for the game itself (`GustavX`, or `GustavDev` on older versions).
  lmm never moves it.
- **Mods you enabled yourself are left alone.** An entry lmm didn't add,
  whether you added it with another tool or the game's own mod manager did,
  keeps its place. lmm never moves or removes it.
- **lmm never enables a pak it didn't install.** A pak you put in `Mods`
  yourself stays as you left it, listed or not.
- **Paks that only replace game files get no entry.** A pak with no
  `meta.lsx` of its own doesn't need to be listed: the game loads it
  anyway. lmm doesn't list it, as Vortex doesn't.
- **A pak lmm can't read is left out, with a warning.** If a pak is
  damaged, or its `meta.lsx` is packed in a way lmm can't read (zstd
  compression, which almost no mods use), the deploy says which pak, and
  the game won't load it until you fix it. Reinstall the mod, or enable
  that one pak with another tool such as BG3 Mod Manager.

When you switch a mod off, or uninstall it, lmm takes its entry out of the
list. Everything else stays where it was.

lmm writes the version of the list the game has used since Patch 7. A list
from an older version of the game (one with a separate `ModOrder` section)
is left alone, and lmm says so.

## The order

Your profile can keep the order of your paks. Until you move one, lmm
keeps the paks in the order the list already has them, and adds a new one
at the end, where Vortex adds it too. The first time you move a pak, your
profile records the whole order as you see it. From then on:

1. Paks you placed go in the order you chose. Your profile keeps their
   places while their mod is switched off, so switching it back on puts
   them back where they were.
2. A pak from a mod you added since, which you haven't placed yet, goes
   after the ones you placed.
3. Entries lmm didn't add stay where they are among yours.

## In the web interface

Open **Reorder…** from your mod library. For Baldur's Gate 3 the window
has a **Paks** tab beside **Mods**. It lists the mod list's entries top
first, and each row says what placed it (**order**: your order;
**default**: not placed yet; **external**: not added by lmm, such as the
game's own entry) and which mod ships it. Use the arrows, **First** and
**Last** to move one of your paks; each move is saved at once and written
to `modsettings.lsx`.

**Reset order…** shows what it will do, then clears your profile's order.
Your paks stay enabled, in the order they have now.

## At the command line

```bash
lmm load-order list
lmm load-order move ImprovedUI.pak --after MCM.pak
lmm load-order move MyFix.pak --last
lmm load-order reset
```

`lmm load-order list` shows the list, what placed each entry and the mod
it comes from. `lmm load-order move` takes `--before`, `--after`,
`--first` or `--last`, and names paks by their file names. Add `--dry-run`
to a move or a reset to see what it would do, and `--json` to any of them
for a script. `-p` picks a profile other than the active one.

## When the game resets the list

If the game finds a mod it can't load, it empties the mod list and starts
without mods. `lmm verify` (and the web UI's Health card) reports each of
your paks the list no longer names, or names out of order.
`lmm verify --fix`, or the next deploy, lists them again. Find out which mod the game
didn't like before you start it again, or it will reset the list once
more.

## Collections

A NexusMods collection for Baldur's Gate 3 carries the curator's pak
order. Importing it saves that order on the new profile, and the import
plan lists it. A collection exported from lmm carries your order too. See
[Profiles](profiles.md#importing-a-nexusmods-collection).

## Getting your old list back

The first time lmm changes `modsettings.lsx`, it keeps a copy of the file
you had. `lmm purge` puts it back, keeping anything you changed in it
since. Switching off every mod that has a pak does the same.
