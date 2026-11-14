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

## Tell lmm where the game's prefix is

A Windows game runs inside a Wine or Proton **prefix**, the folder that
holds its `drive_c`. A few games keep files there that lmm needs to
write. For a Steam game you don't have to do anything: lmm finds Steam's
prefix for it each time, in whichever of your Steam libraries has it, so
it keeps up when Steam moves the game. Set a prefix only to override
that, or for a game from Heroic, Lutris or another launcher:

```bash
lmm game edit skyrim-se --prefix "~/Games/Heroic/Prefixes/default/Skyrim Special Edition"
lmm game show skyrim-se     # the prefix lmm uses, and where it came from
```

If the prefix has more than one user folder under `drive_c/users`, pick
the game's with `--prefix-user <name>`. lmm only uses a user folder that
is really inside the prefix; one that is a link to somewhere else, such as
your home folder, is refused. `--prefix ""` goes back to what
lmm finds by itself. In the browser, the same two fields are in the
game's editor under **⚙ Setup → Games**. See
[Configuration](../configuration.md#prefix-gamesyaml) for the details.

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
with your Steam games in **⚙ Setup → Games** and in `lmm game detect`:
**World of Warcraft**, **Diablo II: Resurrected**, **Warcraft III:
Reforged** and **StarCraft II**.

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
| Retail      | `_retail_`       | `wow-retail`      | `517`             |
| Classic     | `_classic_`      | `wow-classic`     | `79434`           |
| Classic Era | `_classic_era_`  | `wow-classic-era` | `67408`           |
| Forever     | `_forever_`      | `wow-forever`     | `88568`           |

Each one's mods go in its own `Interface/AddOns` folder and come from
CurseForge, already set to the [game flavor](#pick-a-curseforge-game-flavor),
so a Classic game is never offered a Retail addon.

In the terminal, a Battle.net game is named by `battlenet:` and its product
code, where a Steam game shows its app id:

```bash
lmm game detect
lmm game add --from-detected battlenet:wow_classic
```

If you already added the game by hand, lmm recognises it by its folder and
marks it **configured** rather than offering it twice, whatever you called
it. Only the folder counts: a game you named `wow` for the Forever flavor is
Forever, and Retail beside it is still offered as a new game.

### Diablo II: Resurrected

Diablo II: Resurrected (game id `diablo2resurrected`) takes its mods from
NexusMods, into the `mods` folder of the install. The game loads a mod from
`mods/<name>/<name>.mpq/`, so lmm puts each mod there, however its archive
was packed: already in that shape, inside a `mods/` folder, or as the bare
`<name>.mpq` folder. An archive with no `.mpq` folder, or with several, is
deployed as packaged, with a warning. An archive whose `<name>` has a space
in it (`Loot v2.mpq`) is refused: Battle.net splits its arguments on
spaces, so no `-mod` could ever name it. Repack it with both folders
renamed without the space (`Loot_v2/Loot_v2.mpq/`) and add it with
[`lmm import`](manual-mods.md).

The game loads a mod only when it is started with `-mod <name> -txt`, and
Battle.net can't be asked to pass that on. So after each deploy, and each
time you launch the game from lmm, lmm tells you exactly what to enter in
Battle.net: see [Diablo II: Resurrected](launch.md#diablo-ii-resurrected)
on the launching page.

### Warcraft III: Reforged and StarCraft II

These two read custom maps from the `Documents` folder of the Wine user
that plays them, not from the install: `Documents/Warcraft III/Maps` and
`Documents/StarCraft II/Maps` (game ids `warcraft3` and `starcraft2`). A
prefix can hold several users, such as the one Wine made, Proton's
`steamuser` and the shared `Public`, so lmm picks the one that plays the
game:

1. the user whose `Documents` already has the game's folder (the game
   makes it the first time it runs);
2. otherwise the prefix's own user: the one whose Documents folder its
   `user.reg` names, or `steamuser` in a Proton prefix;
3. otherwise the only user there is.

`Public` is never picked. If lmm still can't tell, it doesn't guess: the
game isn't offered, and detection says why. Run the game once so it makes
its folder, then detect again, or add the game by hand with
[`lmm game add`](#in-the-terminal). Older prefixes call the folder
`My Documents`; lmm uses whichever one `user.reg` names, and otherwise
looks in both. When `Documents` is a link to your own `~/Documents` (or
your home folder), as Wine makes it, lmm saves the real folder. A link
that leads anywhere outside the prefix and your home folder is refused,
with a detection warning: add the game by hand if you mean it.

Both games come with their NexusMods source, but most custom maps are on
community sites lmm has no source for, such as Hive Workshop and
SC2Mapster. Download the map and add it with `lmm import <file>`: a single
`.w3x` or `.SC2Map` file is copied into the maps folder as it is, and a
`.zip` is unpacked there.

{: .note }
lmm's authors don't have these three games installed, so their product
codes (`osi`, `w3`, `s2`), launch codes and the NexusMods ids for Warcraft
III and StarCraft II haven't been checked against a real install yet. If
lmm doesn't find one of them, or finds the wrong folder, please open an
issue, and correct the entry in your own `battlenet-games.yaml` (below)
meanwhile.

### Starting a Battle.net game

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
opening the Battle.net client (see the launching page), and `adapter`, the
game's adapter (`d2r` for Diablo II: Resurrected).

A game that keeps its mods in the player's `Documents` sets
`mod_path_base: documents`. Its `mod_path` is then relative to that folder,
and must start with the game's own folder there, which is how lmm picks the
prefix user:

```yaml
w3:
  slug: warcraft3
  name: "Warcraft III: Reforged"
  mod_path_base: documents
  mod_path: Warcraft III/Maps   # relative to the user's Documents
  sources:
    nexusmods: warcraft3reforged
  launch_code: W3
```

## Bethesda games

Skyrim Special Edition (and Anniversary Edition), Skyrim VR, Fallout 4,
Fallout 4 VR, Starfield and Skyrim (2011) use the `bethesda` adapter. When
`lmm game detect` adds one from Steam it sets that for you. It lays out
archives packed relative to the game folder, such as SKSE's, into `Data/`,
and it knows each game's plugin rules.

### The plugin list

For a game with the `bethesda` adapter, every deploy writes the game's
`Plugins.txt` (and `loadorder.txt`, for Skyrim 2011) in the game's Wine
prefix, so the plugins your mods ship are active and in a working order:
masters first, each plugin after its masters, and then the order of your
mods. `lmm deploy` prints what it wrote, and the web UI says it when the
deploy finishes. A plugin you installed by hand, or one bought through the
game's Creations menu, stays where it was and keeps its state. The first
time lmm writes the list it keeps the one you had, and `lmm purge` puts it
back, keeping whatever you or the game changed in it since. A plugin new to
the profile loads last, so a mod you just added wins its conflicts. The list
is written before an `install.after_all` or `uninstall.after_all` hook
runs, so a hook of yours can read or adjust it.

The deploy reports a plugin that needs a master you do not have active, or
more active plugins than the game loads, as an error: the game would not
start. When lmm cannot find the game's prefix, it deploys the mods, does
not write the list, and says what to set (see the section above).
`lmm verify` reports the same, and notices when one of lmm's plugins has
been moved or deactivated since; `lmm verify --fix` writes the list again.
A profile can set its own plugin order and rules: see
[The plugin list](../configuration.md#the-plugin-list-plugins).

A game you added before lmm knew this keeps the adapter it has: lmm never
changes it by itself. Mission Control shows a note offering the `bethesda`
adapter, with a **Use the bethesda adapter** button, and `lmm verify`
prints the same suggestion. In the terminal:

```bash
lmm game edit skyrim-se --adapter bethesda
```

{: .warning }
Mods you already installed keep the layout they were installed with: the
switch only changes how mods installed or updated after it are laid out.
To lay one out again, uninstall it (from every profile that has it) and
install it again. When the game has mods installed, Mission Control asks
before it switches, and `lmm game edit` prints the same warning.

## The Witcher 3 and Baldur's Gate 3

The Witcher 3 uses the `witcher3` adapter, and Baldur's Gate 3 the
`baldurs-gate-3` adapter; `lmm game detect` sets them when it adds the
game from Steam. Both tell lmm where the mod types a NexusMods collection
gives its mods are deployed (see
[Importing a NexusMods collection](profiles.md#importing-a-nexusmods-collection)),
so such a collection can be imported. A game you added before keeps its
adapter; Mission Control and `lmm verify` suggest the new one. In the
terminal:

```bash
lmm game edit witcher3 --adapter witcher3
lmm game edit baldurs-gate-3 --adapter baldurs-gate-3
```

The `witcher3` adapter changes nothing about how a mod you install is laid
out: switching to it needs no reinstall, and mods you already installed stay
exactly where they are.

### Baldur's Gate 3's .pak mods

Baldur's Gate 3 loads `.pak` mods only from the `Mods` folder in its
Wine/Proton prefix,
`drive_c/users/steamuser/AppData/Local/Larian Studios/Baldur's Gate 3/Mods`,
not from the game's `Data/` folder. With the `baldurs-gate-3` adapter lmm
deploys every `.pak` a mod ships there, directly inside `Mods` (a folder the
archive wraps it in is dropped, because the game ignores a pak in a
subfolder). Everything else a mod ships, loose files and a pak named like
one of the game's own (a replacer), still goes into `Data/`. A mod's
`info.json` is not deployed.

lmm finds the prefix as described in
[Tell lmm where the game's prefix is](#tell-lmm-where-the-games-prefix-is).
If it can't, the deploy is refused and says what to set; lmm never puts
the pak anywhere else instead. Wherever lmm lists a deployed file, one in
the prefix is shown with the prefix named in front:
`<prefix>/AppData/Local/Larian Studios/Baldur's Gate 3/Mods/MyMod.pak`, and
the web UI's health view marks it with a **Prefix** badge. A pak of your
own with the same name is kept first and put back when the mod goes, and
lmm only writes inside the prefix's user folder: a folder on the way that
is a link to somewhere else is refused.

The game also has to be told to load a pak: its mod list,
`PlayerProfiles/Public/modsettings.lsx` beside the `Mods` folder, must
enable it. lmm doesn't write that file yet, and says so after a deploy
that placed a pak. Enable the mods with a tool that writes it, such as BG3
Mod Manager. A pak that only replaces game files and has no `meta.lsx`
needs no entry.

Switching a game you added before to `baldurs-gate-3` moves its paks: the
next deploy takes them out of `Data/` and puts them in the `Mods` folder,
with no reinstall. The switch warns you that it will.

## Telling lmm which game a copy is

lmm recognises a game from Steam by itself. A copy from GOG or Epic, added
by hand through Heroic or Lutris under an id of your own, can say which
game it is by naming its entry in lmm's game catalog (the games
`lmm game detect` knows). In the browser, pick it under **Game catalog
entry** in the game's editor. In the terminal:

```bash
lmm game edit my-gog-skyrim --catalog skyrim-se
```

lmm then treats it as that game: a Bethesda game gets its plugin rules,
and lmm suggests the adapter the catalog sets the game up with. If you
give an id the catalog doesn't have, lmm lists the ones it does.
`--catalog ""` takes the setting off again.

### Files that belong in the game folder

A script extender's loader (`skse64_loader.exe` and its DLLs) and an ENB
(`d3d11.dll`, `enbseries/`, or a whole preset with an `enbseries.ini`)
belong in the game folder itself, beside the game's executable, not in
`Data/`. The `bethesda` adapter recognises them, and lmm deploys them
there; the rest of the same mod still goes into `Data/`.

Wherever lmm lists a deployed file, one in the game folder is shown with
the folder named in front: `<game root>/d3d11.dll`, beside
`textures/sky.dds` for a file in `Data/`. That is how `lmm deploy
--dry-run`, `lmm verify`, `lmm purge`, `lmm conflicts` and `lmm mod files`
print them, and how the web UI's health and verify views show them.

The game folder holds the game's own files. If a mod's file would replace
one the game ships (a `d3d11.dll` of the game's own, say), lmm keeps a copy
of the game's file first and puts it back when the mod is uninstalled,
disabled or purged, exactly as it does in `Data/`. lmm only ever writes
inside the game folder: a folder there that is a link to somewhere else is
refused, and the mod's deploy says so.

A mod deployed before the game used the `bethesda` adapter put those files
into `Data/`. The next deploy (`lmm deploy`, or **Deploy** in the web UI)
moves them to the game folder; there is no need to reinstall the mod.

## Games from other stores

For every other store, adding the game by hand works the same as for any
other game.
