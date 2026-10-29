---
title: Home
nav_order: 1
permalink: /
---

# lmm, the Linux Mod Manager

lmm installs and manages game mods on Linux, natively. There's no Wine and
no Windows mod manager running in the background.

You tell lmm which game you're modding. It downloads mods from the sites you
already use (NexusMods, CurseForge, Thunderstore and the Steam Workshop),
puts them in the right place in the game folder, and keeps track of every
file it adds. Because it knows exactly what it added, it can update a mod,
roll it back to an older version, or take it out again, all without touching
the game's own files.

You can use lmm two ways, and they share everything:

- **A web interface.** Run `lmm serve` in a terminal, and lmm opens a page in
  your browser with your games and mods, search and update checks. This is
  the easiest way to start.
- **The command line.** Everything the web interface does is also an `lmm`
  command, so you can manage your mods straight from the terminal, over SSH
  on another machine, or from your own scripts and programs: add `--json` to
  any command and it answers in a form a program can read.

## Start here

1. [Install lmm](install.md)
2. [Set it up and install your first mods](first-mods.md)

Then read whichever of these you need:

| I want to…                                   | Read                                                    |
| -------------------------------------------- | ------------------------------------------------------- |
| Mod a game lmm didn't find by itself         | [Adding a game by hand](add-a-game.md)                  |
| Keep several mod setups for one game         | [Profiles](profiles.md)                                 |
| Update mods, or go back to an older version  | [Updating, rolling back and locking](updates.md)        |
| Use a mod I downloaded myself                | [Mods you downloaded yourself](manual-mods.md)          |
| Mod a Unity game such as Valheim             | [Unity games and BepInEx](bepinex.md)                   |
| Keep the web interface running all the time  | [Run the web UI in the background](background.md)       |
| Fix something, or get my game back to normal | [When something goes wrong](troubleshooting.md)         |
| Look up a word or a quick answer             | [FAQ and glossary](faq.md)                              |

## Where to find more

This guide covers everyday use in plain terms. The
[README](https://github.com/DonovanMods/linux-mod-manager#readme) is the full
reference, with every command and option. Each command also has its own help:
run `lmm --help`, or `lmm <command> --help`, for example `lmm install --help`.

Questions, bugs and game requests are welcome on the
[issue tracker](https://github.com/DonovanMods/linux-mod-manager/issues).
