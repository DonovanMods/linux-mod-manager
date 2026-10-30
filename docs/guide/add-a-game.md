---
title: Adding a game by hand
nav_order: 4
---

# Adding a game by hand

lmm works with almost any game that loads mods from a folder. Automatic
detection covers Steam and [Battle.net](#battlenet-games), so a game from
GOG, Heroic, the Epic store or a plain folder needs adding by hand. It takes
a minute.

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

## Pick a CurseForge game flavor

Some games come in several flavors that CurseForge files under one game.
World of Warcraft is the common case: Retail, Classic and the other Classic
versions are all CurseForge game `1`, and an addon can have a different
file for each. Tell lmm which flavor a game is, or it may offer a Classic
install the Retail file.

CurseForge marks every file with a *game version type*. Tell lmm which ones
the game accepts.

In the browser, open the game's editor: once the game uses CurseForge, the
**CurseForge game version types** section lists the game's version types by
name. Tick the ones this game is, and save. If lmm can't load the list (no
CurseForge API key yet, or you're offline), the section is a text box for
the ids instead.

On the command line, list the game's version types with their slugs:

```bash
lmm game version-types wow-classic
```

Then give the slug (or the id) to `lmm game edit`. For a Classic Era
install:

```bash
lmm game edit wow-classic --curseforge-version-type wow-classic
```

Repeat the flag to accept more than one, and run
`lmm game edit wow-classic --curseforge-version-type ""` to go back to every
file. A value that matches no version type fails, and the error lists the
ones that do. Both the list and slugs need your CurseForge API key; ids
don't. A slug is looked up in the CurseForge game the same command maps, so
`lmm game edit wow-classic --source curseforge=1 --curseforge-version-type wow-classic`
adds the mapping and picks the flavor in one go.

For World of Warcraft, the common ones are:

| Flavor                    | Install folder  | Slug                            | Id      |
| ------------------------- | --------------- | ------------------------------- | ------- |
| Retail                    | `_retail_`      | `game`                          | `517`   |
| Classic Era               | `_classic_era_` | `wow-classic`                   | `67408` |
| Mists of Pandaria Classic | `_classic_`     | `wow-mists-of-pandaria-classic` | `79434` |

From then on, search only shows addons that have a file for that flavor,
and installs and updates only use those files. An addon with no file for the
flavor is reported as having no installable file for this game.

{: .note }
lmm reads the list from CurseForge's
`GET /v1/games/{gameId}/version-types`. If you'd rather look it up yourself,
ask for it with your API key (the game id is the one in the game's
`curseforge` source, `1` for World of Warcraft):
`curl -s -H "x-api-key: $CURSEFORGE_API_KEY" https://api.curseforge.com/v1/games/1/version-types`

## Battle.net games

lmm finds Battle.net games installed under Wine or Proton, and lists them
with your Steam games in **⚙ Setup → Games** and in `lmm game detect`. For
now that means **World of Warcraft**.

lmm looks in every Wine prefix it can find:

- `$WINEPREFIX` and `~/.wine`
- Lutris prefixes, and any other prefix folder, in `~/Games/`
- Bottles, both the normal and the Flatpak version
- prefixes Steam made for a non-Steam game (`steamapps/compatdata/*/pfx`)

Inside each, it reads the game's own `.build.info` file to see which
products are installed. World of Warcraft keeps every flavor in one folder,
with a subfolder for each, and each flavor you have is its own game:

| Flavor      | Folder           | lmm game id       | CurseForge flavor |
| ----------- | ---------------- | ----------------- | ----------------- |
| Retail      | `_retail_`       | `wow`             | `517`             |
| Classic     | `_classic_`      | `wow-classic`     | `79434`           |
| Classic Era | `_classic_era_`  | `wow-classic-era` | `67408`           |
| Forever     | `_forever_`      | `wow-forever`     | not set yet       |

Each one's mods go in its own `Interface/AddOns` folder and come from
CurseForge, already set to the [game flavor](#pick-a-curseforge-game-flavor),
so a Classic game is never offered a Retail addon. The Forever flavor
doesn't have a CurseForge flavor id yet, so it takes every file; set one
with `lmm game edit wow-forever --curseforge-version-type <id>` when you
know it.

In the terminal, a Battle.net game is named by `battlenet:` and its product
code, where a Steam game shows its app id:

```bash
lmm game detect
lmm game add --from-detected battlenet:wow_classic
```

If you already added the game by hand, lmm recognises it by its folder and
marks it **configured** rather than offering it twice.

A game lmm adds this way can also be started from lmm, through Battle.net:
see [Battle.net games](launch.md#battlenet-games) on the launching page.

{: .warning }
lmm never lists Overwatch, Hearthstone, Heroes of the Storm or Call of
Duty, not even with `--include-unknown`. These are competitive games with
anti-cheat, and lmm won't offer to put files into them.

To teach lmm another Battle.net game, or change one it knows, add it to
`~/.config/lmm/battlenet-games.yaml`, keyed by its product code:

```yaml
wow_classic:
  slug: wow-classic
  name: World of Warcraft Classic
  install_dir: _classic_       # the folder under the install the game lives in
  mod_path: Interface/AddOns   # relative to install_dir
  sources:
    curseforge: "1"
  curseforge:
    game_version_types: [79434]
```

An entry may also set `launch_code`, the code Battle.net's
`--exec="launch <code>"` takes, so Play launches the game rather than
opening the Battle.net client (see the launching page).

## Games from other stores

For every other store, adding the game by hand works the same as for any
other game.
