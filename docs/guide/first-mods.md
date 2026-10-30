---
title: Your first mods
nav_order: 3
---

# Your first mods

This page takes you from a fresh install to a modded game. It uses the web
interface. The terminal commands for each step are in the box at the end.

## 1. Start the web interface

In a terminal, run:

```bash
lmm serve
```

Your browser opens on lmm's page. If it doesn't, open
<http://127.0.0.1:7420/> yourself. The page only exists while that command is
running, so leave the terminal open. Press **Ctrl+C** there to stop it. To
keep it running without a terminal, see
[Run the web UI in the background](background.md).

{: .note }
The page lives on your own computer. Nothing else on your network can reach
it, and nothing is installed or uploaded anywhere.

## 2. Add your games

The first time you open lmm, it offers to look through your Steam library for
games it can mod. Games lmm already knows about, such as Skyrim, Fallout 4,
Baldur's Gate 3, Cyberpunk 2077, Valheim and Lethal Company, are marked
**Known**. Tick the ones you want and add them, and lmm fills in where each
game lives and where its mods go.

Your other Steam games are listed too, with an **Add with details…** button
instead of a tick box. lmm doesn't know where those games keep their mods, so
it asks you.

lmm also looks for **World of Warcraft** installed through Battle.net under
Wine or Proton (a Lutris, Bottles, umu or Steam "non-Steam game" prefix).
Each flavor you have installed, such as Retail, Classic or Classic Era, is
its own game, with its addon folder and its CurseForge flavor already set.
It finds Diablo II: Resurrected, Warcraft III: Reforged and StarCraft II
too. See [Battle.net games](add-a-game.md#battlenet-games).

If your game isn't on Steam or Battle.net, see
[Adding a game by hand](add-a-game.md).

## 3. Connect your mod sites

To download mods for you, some sites need you to sign in with an **API key**,
a long code you copy from your account page on that site. lmm stores it
encrypted on your computer and only ever sends it to that site.

| Site            | Key needed?                     | Where to get it                                                                        |
| --------------- | ------------------------------- | -------------------------------------------------------------------------------------- |
| NexusMods       | Yes                             | [Your NexusMods API page](https://www.nexusmods.com/users/myaccount?tab=api), "Personal API Key" |
| CurseForge      | Yes                             | [The CurseForge console](https://console.curseforge.com/): create a project, then an API key |
| Thunderstore    | No                              | Nothing to do                                                                          |
| Steam Workshop  | Only to search the Workshop     | [Steam's API key page](https://steamcommunity.com/dev/apikey). Keep this key private.  |

You only need keys for the sites your games use. The setup screen asks for
each one and shows you where to paste it.

{: .warning }
NexusMods only allows its **Premium** members to download through apps like
lmm. With a free NexusMods account, download the file on the NexusMods
website instead and hand it to lmm: see
[Mods you downloaded yourself](manual-mods.md).

## 4. Bring in mods you already have

If you've modded this game before, lmm can scan the game's mod folder and
take over the mods it finds there. It matches each one against your mod
sites by name, so it can tell you about updates later. Anything it can't
match is still managed, just without update checks.

You can skip this and do it later from **Setup → Adopt**.

## 5. Find and install a mod

You're now on lmm's main screen, **Mission Control**. Your installed mods are
listed in the middle (the library), with the game and profile at the top.

1. Type the name of a mod into the search box at the top and press **Enter**.
   lmm searches every site the game is connected to.
2. Click **Install** on the mod you want.
3. lmm shows you a preview: exactly which files it will add and where. Check
   it and confirm.

The install runs in the background. The bell icon at the top shows its
progress, and you can keep working while it runs. If the mod needs other mods
to work, lmm finds and installs those as well.

Installing puts the mod straight into the game. If the **Deploy** button at
the top ever shows changes waiting, click it to bring the game folder up to
date.

## 6. Play

Start the game the way you always do, through Steam or your launcher. lmm
doesn't need to be running while you play. You can also start it from lmm
with `lmm launch`, which checks your mods are deployed first; see
[Launching your game](launch.md).

## In the terminal

The same steps as commands. `lmm init` walks you through steps 2 to 4 with
questions, and is safe to run again any time, for example after installing a
new game.

```bash
lmm init
lmm search "sky ui"
lmm install "sky ui"
lmm list
```

If you mod more than one game, add `--game <game-id>` to choose which one
(`lmm game list` shows the ids), or pick one to use by default:

```bash
lmm game set-default skyrim-se
```

## What next?

- [Update, roll back or lock your mods](updates.md)
- [Keep several mod setups for the same game](profiles.md)
- [Get the game back to normal if something goes wrong](troubleshooting.md)
