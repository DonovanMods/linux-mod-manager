---
title: Launching your game
nav_order: 9
---

# Launching your game

lmm can start the game for you, with the mods of its active profile. You
don't have to: starting it through Steam or your launcher as usual works
just the same. Launching from lmm is a shortcut that also checks your mods
are in place first.

In the terminal:

```bash
lmm launch
```

Add `--game <game-id>` if you mod more than one game. In the web
interface, press **Play** in the top bar, next to **Deploy** (see
[In the web interface](#in-the-web-interface)).

## Steam games need nothing set up

For a game you added from Steam, lmm finds its Steam app id by itself and
asks Steam to start it, the same as clicking Play in Steam. Your Steam
launch options, Proton and its settings all apply as usual. lmm only asks;
Steam starts the game, so lmm finishes as soon as Steam has the request.

If you use the Flatpak version of Steam, lmm notices which Steam a game is
installed in and asks that one.

## Battle.net games

A Battle.net game lmm [detected](add-a-game.md#battlenet-games) is started
through the Battle.net client in its Wine prefix. lmm writes this for you
when it adds the game:

```yaml
launch:
  via: battlenet
  battlenet:
    prefix: ~/Games/battlenet  # the Wine prefix Battle.net is installed in
    product: WoW               # optional: which game Battle.net launches
    runner: umu                # optional: umu or wine
    proton: GE-Proton          # optional: the Proton umu-run uses
```

With `product`, lmm asks Battle.net to launch that game, the same as
`Battle.net Launcher.exe --exec="launch WoW"`. Without it, Play opens the
Battle.net client and you press Play there. lmm only fills in `WoW`, for
World of Warcraft Retail: Battle.net's launch codes aren't the product codes
lmm detects, so for the Classic flavors and Forever it leaves `product`
out. If you know a flavor's launch code, add it yourself.

A prefix made by umu-launcher or Steam (a Proton prefix) runs with
`umu-run`, the way `omarchy-launch-battlenet` and similar launchers start
it. lmm sets `WINEPREFIX`, `GAMEID=umu-battlenet`, `PROTON_VERB=run` and,
when you set `proton`, `PROTONPATH`. If the prefix was last run by
GE-Proton, lmm sets `proton` to that exact build (say `GE-Proton11-11`)
when it adds the game, so the prefix keeps running on the Proton that made
it. When that build isn't installed where `umu-run` looks for it
(`~/.local/share/Steam/compatibilitytools.d` or
`~/.local/share/umu/compatibilitytools`), lmm sets `proton: GE-Proton`,
which `umu-run` takes to mean the newest GE-Proton, and detection says so:
a newer Proton may upgrade the prefix, so install the build or set `proton`
yourself. Any other
prefix runs with `wine` and `WINEPREFIX`. `runner` overrides the choice. If
`umu-run` isn't installed, lmm won't start a Proton prefix with plain Wine:
it says so, and you install umu-launcher or set `runner: wine`.

Battle.net starts the game, so lmm finishes as soon as Battle.net has the
request.

### Diablo II: Resurrected

Diablo II: Resurrected loads a mod only when it's started with
`-mod <name> -txt`, and Battle.net doesn't pass extra arguments on when
another program asks it to launch a game. You set them once, in Battle.net
itself:

1. In Battle.net, select Diablo II: Resurrected.
2. Click the gear beside **Play** and choose **Game Settings**.
3. Tick **Additional command line arguments** and enter the line lmm gave
   you, for example `-mod LootFilter -txt`.

lmm gives you that line after every deploy and every launch, named after
the mod's folder:

```text
Warning: Diablo II: Resurrected loads Loot Filter only when started with "-mod LootFilter -txt": in Battle.net, open the game's settings (the gear beside Play) > Game Settings, tick "Additional command line arguments" and enter: -mod LootFilter -txt
```

The game loads one mod at a time. With several enabled, lmm names the one
last in the load order, the one that wins when mods overlap, and warns that
the others won't load: disable them, or move the one you
want to the end (`lmm profile reorder`, or **Resolve…** on the web UI's
Conflicts card). When you change mods, check the line still matches. To
play without mods, untick the setting.

## Any other game: tell lmm what to run

For a game lmm can't start through Steam (a Lutris, Heroic or Bottles game,
a game you run with Wine yourself, or a wrapper script of your own), add a
`launch` block to the game in `~/.config/lmm/games.yaml`:

```yaml
games:
  wow:
    name: World of Warcraft
    # ...
    launch:
      command: ["lutris", "lutris:rungame/wow"]
```

`command` is a list: the program first, then each argument on its own.
It is never run through a shell, so quotes and `$VARIABLES` are passed as
they are. The program is either a name lmm finds on your `PATH`, or a full
path, which may start with `~/` or `{install_path}`. The command starts in
the game's install folder.

Arguments may contain these placeholders, which lmm fills in:

| Placeholder      | Becomes                         |
| ---------------- | ------------------------------- |
| `{game_id}`      | The game's id                   |
| `{install_path}` | The game's install folder       |
| `{mod_path}`     | The folder lmm deploys mods to  |
| `{profile}`      | The profile being launched      |
| `{steam_app_id}` | The game's Steam app id         |

The program also gets `LMM_GAME_ID`, `LMM_GAME_PATH`, `LMM_MOD_PATH` and
`LMM_PROFILE` in its environment, as a hook does.
Your API keys are never passed to the game: lmm leaves out
`NEXUSMODS_API_KEY`, `CURSEFORGE_API_KEY`, `STEAM_WEB_API_KEY` and every
`LMM_<SOURCE>_API_KEY`, so a game's mods can't read them.

When a game has both a `command` and a Steam app id, lmm runs the command.
Two more optional settings change that:

```yaml
launch:
  via: steam             # always use Steam, even with a command set
  steam_app_id: "892970" # use this app id instead of the one lmm finds
```

## If some mods aren't deployed

Before it starts the game, lmm checks that the mods in the game folder
match the profile: that every mod you enabled is deployed, and nothing you
disabled still is. If they don't match, lmm warns you and launches anyway.

- `lmm launch --deploy` deploys the profile first, then launches.
- `lmm launch --strict` refuses to launch instead of warning.

A deploy puts enabled mods into the game folder, but it doesn't take out a
mod you disabled. If a disabled mod is still deployed, run
`lmm profile apply`, which does both. `lmm launch --deploy --strict`
refuses that case before it deploys anything. If the mods folder itself
needs attention, the warning says what to change.

Only the active profile can be launched, because it's the one whose mods
are in the game folder. To play with another profile, switch to it first
with `lmm profile switch <name>`.

## In the web interface

**Play** in the top bar launches the game you're looking at, with its
active profile. When lmm has asked Steam to start it, a message says
"Launch requested". If lmm can't launch the game, **Play** is greyed out;
point at it to see why, which usually means adding a `launch` block as
above. Choosing another profile in the top bar only shows it, so **Play** is
greyed out there too: use **Switch and deploy…** in the profile menu to make
it the active profile first.

If some mods aren't deployed, **Play** asks before it starts anything:

- **Deploy & launch** deploys the profile, as **Deploy** does, and
  launches once the deploy has finished. It is offered only when a deploy
  fixes everything. **Play** stays greyed out until the game has started.
- **Launch anyway** starts the game with the mods as they are.

When a deploy can't fix it (a mod you disabled is still deployed, or the
mods folder needs attention), the message says what to do instead, such as
**Apply profile…** on the Profile card.

If the Health card says BepInEx hasn't run since you deployed new plugins
("launch the game once to confirm"), that row has a **Play** button too.

The web interface only launches games through systemd, so that the game
keeps running when `lmm serve` restarts. On a computer without it, use
`lmm launch` in a terminal instead.

## Check what lmm would run

```bash
lmm launch --dry-run
```

prints the launch method and the full command line, and starts nothing.

## The game keeps running without lmm

lmm starts the game as its own background service in your desktop session
(a systemd user service), not as part of lmm. Closing the terminal, or
restarting `lmm serve`, leaves the game running. lmm waits only until
the program has started. If it can't start (the program is missing, or
the game folder is gone), `lmm launch` fails and names the unit to check
with `journalctl --user -u <unit>`. On a system without systemd, lmm
starts the game on its own instead.

## "No display to launch the game on"

The game has to know which screen to open on. lmm passes along your
desktop's display settings, but when lmm runs in the background as a
service (see [Run the web UI in the background](background.md)) it may not
have them. If launching says there is no display, run this once in a
terminal on your desktop:

```bash
dbus-update-activation-environment --systemd DISPLAY WAYLAND_DISPLAY
```

To make it permanent, have your desktop session run that command when you
log in, or set the two variables in a file under
`~/.config/environment.d/`.
