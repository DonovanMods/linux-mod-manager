---
title: Unity games and BepInEx
nav_order: 8
---

# Unity games and BepInEx

Many games made with the Unity engine, including Valheim, Lethal Company,
Risk of Rain 2, R.E.P.O. and Content Warning, are modded through
**BepInEx**. BepInEx is a *mod loader*: a small program you add to the game
once, which then loads all the other mods.

## Who does what

- **You** install BepInEx into the game, once, and set one launch option in
  Steam (below).
- **lmm** installs, updates and removes the mods that run on top of it.

lmm deliberately leaves BepInEx itself to you. It's part of the game's setup,
it has to stay put whichever profile you use, and every game and Linux setup
needs it done slightly differently.

## Step 1: install BepInEx

Follow the instructions for your game on the game's modding community or
Thunderstore page. That usually means downloading the BepInEx pack and
unpacking it into the game's own folder, so a `BepInEx` folder sits next to
the game's program.

## Step 2: tell Steam to load it

Most Windows games run on Linux through Proton, and Proton needs telling to
load BepInEx. lmm works out the exact launch option your game needs. To see
it, open **⚙ Setup → Games** and click the pencil next to the game, or run:

```bash
lmm game show lethal-company
```

In Steam, right-click the game, choose **Properties**, and paste that text
into **Launch Options**.

## Step 3: add the game to lmm

The games above are recognised automatically, so adding them through Steam
detection sets up everything, BepInEx included. For any other BepInEx game,
add it by hand (see [Adding a game by hand](add-a-game.md)) and tell lmm it
uses BepInEx: in the browser it's in the game's editor, and in the terminal
it's `--loader bepinex`:

```bash
lmm game add --name "My Unity Game" --source thunderstore --id my-unity-game --path ~/Games/MyUnityGame --loader bepinex
```

## Then install mods as normal

From here it's the same as any other game. lmm puts each mod where BepInEx
expects it, however the mod's author packed it.

## Checking it all works

lmm can check that BepInEx is installed and set up the way your game needs:

```bash
lmm game show lethal-company
lmm verify
```

{: .note }
Many Thunderstore mods list "BepInExPack" as something they need. That's
BepInEx itself, so lmm doesn't install it as a mod. If BepInEx isn't set up
yet, lmm stops before downloading anything and tells you the steps above.
