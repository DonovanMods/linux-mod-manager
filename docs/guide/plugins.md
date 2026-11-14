---
title: Plugin order (Skyrim, Fallout, Starfield)
nav_order: 9
---

# Plugin order (Skyrim, Fallout, Starfield)

Bethesda games (Skyrim, Fallout 4, Starfield and their editions) load
their mods' content from **plugins**: files ending in `.esm`, `.esp` or
`.esl`. The game loads them one after another, and when two plugins change
the same thing, the one loaded later wins. Which plugins are switched on,
and in what order, is kept in a file called `Plugins.txt`.

lmm writes that file for you every time it deploys your mods. This page
explains the order it picks and how to change it.

Plugin order is not the same as mod order. Mod order (the **Reorder…**
window's **Mods** tab, or `lmm profile reorder`) decides which mod's file
wins when two mods ship a file with the same name. Plugin order decides
the order the game loads its plugins in. One mod can ship several plugins,
so the two are kept separately.

## Which games

Games set up with the `bethesda` adapter: Skyrim Special Edition and
Anniversary Edition, Skyrim VR, Skyrim (2011), Fallout 4, Fallout 4 VR and
Starfield. lmm sets this when it finds one of them on Steam. For a game you
added by hand, see [Adding a game by hand](add-a-game.md).

lmm writes `Plugins.txt` inside the game's Wine prefix (the folder Steam's
Proton keeps for the game). Run the game once through Steam before you
deploy, so the prefix exists.

## The order lmm picks

lmm works the order out the same way every time:

1. The game's own plugins (`Skyrim.esm`, `Update.esm`, the official
   add-ons) always load first. lmm never lists them, and you can't move
   them.
2. A plugin always loads after the plugins it needs (its **masters**), and
   masters load before ordinary plugins.
3. Your **rules** come next (see [Rules](#rules) below).
4. Within that, plugins go in the order you chose. A plugin you haven't
   placed yet, because its mod is new, goes at the end, so a newly added
   mod wins its conflicts.

Plugins you didn't install with lmm (ones you copied in yourself, or that
the game's Creations menu added) keep their place and stay on or off as
you left them. lmm never switches off a plugin it didn't install.

## In the web interface

Open **Reorder…** from your mod library. For these games the window has a
**Plugins** tab beside **Mods**. It lists the plugins in the order the game
loads them, top first. Each row shows:

- a tick box: whether the plugin is switched on;
- **M**, **L** or **D**: the plugin is a master, a light plugin or a
  medium plugin;
- why it is where it is: **game** (one of the game's own), **order** (where
  you put it), **new** (not placed yet, so it loads last), **rule** (one of
  your rules keeps it there), **master** (it has to load after one of its
  masters), or **external** (lmm didn't install it);
- the masters it needs, with a missing one in red, and the mod it comes
  from.

Changes on this tab are saved as you make them, and `Plugins.txt` is
written straight away:

- Untick a plugin to switch it off, or tick it to switch it on.
- Use the arrows to move a plugin up or down, or **First** and **Last**.
  If a rule or a master keeps it in place, it stays where it is and the
  row says why.
- **Sort…** shows what sorting would change, and **Apply sort** saves it.
  Sorting writes the order you see into your profile, new plugins
  included, so you can rearrange from there.

Problems the game won't survive, such as a plugin whose master isn't
installed, or more plugins switched on than the game can load, are listed
under **Findings**.

## At the command line

```sh
lmm plugins list
```

prints the order, with the same columns: whether each plugin is on (`*`),
its flags, what placed it, the masters it needs and its mod. Problems are
listed after the table.

To change it:

```sh
lmm plugins disable "Optional Thing.esp"
lmm plugins enable "Optional Thing.esp"
lmm plugins move Patch.esp --after SkyUI_SE.esp
lmm plugins move "Optional Thing.esp" --last
lmm plugins sort
```

Add `--dry-run` to any of these to see what it would change without
changing anything. Plugin names don't care about upper or lower case.

## Rules

A rule tells lmm that a plugin has to load after another one, whatever
order you put them in. Rules work the way LOOT's user rules do, so rules
from a LOOT userlist or a collection fit as they are.

- **after**: this plugin loads after that one, when both are there.
- **requires**: the same, and lmm warns you when the one it needs isn't
  switched on.
- **group**: plugins can be put in a named group, and a group can load
  after other groups. Every plugin without a group is in the group called
  `default`.

In the web interface the **Rules** section at the bottom of the Plugins tab
lists your rules, with a **Remove** button on each, and has a form to add
one. At the command line:

```sh
lmm plugins rules add Patch.esp --after SkyUI_SE.esp
lmm plugins rules add Patch.esp --req "Unofficial Skyrim Special Edition Patch.esp"
lmm plugins rules group add "Late Loaders" --after default
lmm plugins rules add Patch.esp --group "Late Loaders"
lmm plugins rules list
lmm plugins rules remove Patch.esp --after SkyUI_SE.esp
lmm plugins rules remove Patch.esp
```

A rule can name a plugin you haven't installed yet; it applies once you do.
A rule can't be about a plugin lmm didn't install (its place is the
game's), but such a plugin can be the one another plugin loads after.
lmm refuses a rule that would make the order impossible, for example one
that puts a plugin before its own master, and says which rules clash.

## Profiles

The order and the rules are part of the profile, so each profile keeps its
own, and they travel with the profile when you export it. Only the profile
you are playing has its plugins deployed. Changing another profile's order
saves it in that profile, and `Plugins.txt` is written when you switch to it.

## Getting your old list back

The first time lmm writes `Plugins.txt`, it keeps a copy of the file you had.
`lmm purge` puts it back, keeping anything you or the game changed since.
