---
title: Archive load order (Cyberpunk 2077)
nav_order: 9.5
---

# Archive load order (Cyberpunk 2077)

Cyberpunk 2077 mods are mostly **archives**: files ending in `.archive` in
the game's `archive/pc/mod` folder. When two archives change the same game
file, the one the game loads **first** wins. On its own the game loads them
in alphabetical order of their names, which is why so many mod archives
start with `!` or `#`. A file called `modlist.txt` in that folder changes
this: the game loads the archives it lists first, in the order it lists
them, then every other archive in alphabetical order.

lmm can write `modlist.txt` for you, from an order you keep in your profile.
This page explains how.

Archive order is not the same as mod order. Mod order (the **Reorder…**
window's **Mods** tab, or `lmm profile reorder`) decides which mod's file
wins when two mods ship a file with the same name. Archive order decides
which archive wins when two archives change the same thing inside the game.

## Which games

Cyberpunk 2077 set up with the `cyberpunk2077` adapter. lmm sets this when
it finds the game on Steam. A Cyberpunk 2077 you added before this version
keeps the adapter it had: `lmm verify` suggests the switch, and
`lmm game edit <game> --adapter cyberpunk2077` makes it. The switch moves
none of your mods.

## Nothing changes until you ask

lmm leaves `modlist.txt` alone until your profile has an order. Until then
the game uses its own order, and lmm only shows it to you. The first time
you move an archive, the profile records the whole order as you see it, and
from then on lmm writes `modlist.txt` every time it deploys your mods.

## The order lmm writes

1. Archives you placed go in the order you chose. Your profile keeps their
   places even while their mod is switched off, so switching it back on
   puts them back where they were.
2. An archive from a mod you added since, which you haven't placed yet,
   goes in next to the archive it sorts after alphabetically: where the
   game would have loaded it without a list.
3. Archives you didn't install with lmm keep the place `modlist.txt` gave
   them. lmm never adds one that the list doesn't name; the game loads it
   after every listed archive, and lmm says so when it deploys.

If a mod ships its own `modlist.txt`, lmm doesn't write over it, and says so.

## In the web interface

Open **Reorder…** from your mod library. For Cyberpunk 2077 the window has
an **Archives** tab beside **Mods**. It lists the archives in the order the
game loads them, top first, and each row says what placed it (**order**:
your order; **default**: not placed yet; **external**: not installed with
lmm) and which mod ships it. Use the arrows, **First** and **Last** to move
an archive; each move is saved at once and written to `modlist.txt`.

**Reset order…** shows what it will do, then clears your profile's order
and gives `modlist.txt` back as it was before lmm first wrote it.

## At the command line

```bash
lmm load-order list
lmm load-order move "! my fix.archive" --first
lmm load-order move basegame_b.archive --before basegame_a.archive
lmm load-order reset
```

`lmm load-order list` shows the order, what placed each archive and its
mod. `lmm load-order move` takes `--before`, `--after`, `--first` or
`--last`. Add `--dry-run` to a move or a reset to see what it would do, and
`--json` to any of them for a script. `-p` picks a profile other than the
active one.

## Collections

A NexusMods collection for Cyberpunk 2077 usually carries the curator's
archive order. Importing it saves that order on the new profile, and the
import plan lists it, with the mod each archive comes from where the
collection says. A collection exported from lmm carries your order too.
See [Profiles](profiles.md#importing-a-nexusmods-collection).

## Getting your old list back

The first time lmm writes `modlist.txt`, it keeps a copy of the file you
had. `lmm purge` and `lmm load-order reset` put it back, keeping anything
you changed in it since, or remove the file if there was none before.
