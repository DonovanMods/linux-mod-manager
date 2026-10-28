---
title: Adding a game by hand
nav_order: 4
---

# Adding a game by hand

lmm works with almost any game that loads mods from a folder. Automatic
detection only covers Steam, so a game from GOG, Heroic, Lutris, the Epic
store or a plain folder needs adding by hand. It takes a minute.

## What lmm needs to know

- **The game's name**, as you'd like to see it.
- **Where the game is installed**: the folder the game lives in.
- **Where its mods go**: often a folder called `mods`, `Mods` or `Data`
  inside the game folder. The game's modding community or the mod's own
  instructions will say. You can give it as a folder inside the game folder,
  such as `Data`, or as a full path.
- **Which mod site to use, and the game's name on that site.** For
  NexusMods, that's the word in the game's address: for
  `https://www.nexusmods.com/skyrimspecialedition` it's
  `skyrimspecialedition`. CurseForge has a search to find it for you.

## In the browser

1. Open **⚙ Setup**, then the **Games** section.
2. Choose to add a game manually and fill in the form. The **Browse…**
   buttons next to the folder fields let you pick the folders instead of
   typing them.
3. Pick a mod site. lmm searches it for the game's name and suggests a
   match, which you can accept or change.
4. Save. The game now appears in the game picker at the top of the page.

If the game *is* on Steam but wasn't marked **Known**, use **Pick an
installed game…** in the same form. lmm fills in the name and install folder
for you, and you add the rest.

## In the terminal

`lmm game add` asks for each piece of information in turn:

```bash
lmm game add
```

Or give it everything at once:

```bash
lmm game add --name "Stardew Valley" --source nexusmods --id stardewvalley --game-id stardew-valley --path ~/Games/StardewValley --mod-path Mods
```

For a Steam game that lmm lists but doesn't know, start from its Steam app
id and lmm fills in the rest:

```bash
lmm game detect --include-unknown
lmm game add --from-detected 413150 --source nexusmods --id stardewvalley
```

## Changing a game later

In the browser, click the pencil next to the game in **⚙ Setup → Games**.
In the terminal, use `lmm game edit`, and `lmm game show` to see what lmm
knows about a game:

```bash
lmm game show stardew-valley
lmm game edit stardew-valley --mod-path Mods
```

{: .warning }
If mods are already installed, lmm won't let you move the mod folder out
from under them. It tells you what to do first: take the mods out of the
game folder, change the folder, then put them back. The message lists each
step.

## Games from other stores

Automatic detection for Battle.net games is planned. Until then, and for
every other store, adding the game by hand works the same as for any other
game.
