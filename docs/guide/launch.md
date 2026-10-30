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

Add `--game <game-id>` if you mod more than one game. A **Play** button in
the web interface is coming next.

## Steam games need nothing set up

For a game you added from Steam, lmm finds its Steam app id by itself and
asks Steam to start it, the same as clicking Play in Steam. Your Steam
launch options, Proton and its settings all apply as usual. lmm only asks;
Steam starts the game, so lmm finishes as soon as Steam has the request.

If you use the Flatpak version of Steam, lmm notices which Steam a game is
installed in and asks that one.

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

Only the active profile can be launched, because it's the one whose mods
are in the game folder. To play with another profile, switch to it first
with `lmm profile switch <name>`.

## Check what lmm would run

```bash
lmm launch --dry-run
```

prints the launch method and the full command line, and starts nothing.

## The game keeps running without lmm

lmm starts the game as its own background service in your desktop session
(a systemd user service), not as part of lmm. Closing the terminal, or
restarting `lmm serve`, leaves the game running. On a system without
systemd, lmm starts the game on its own instead.

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
