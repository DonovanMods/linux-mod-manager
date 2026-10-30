---
title: FAQ and glossary
nav_order: 11
---

# FAQ and glossary

## Questions

### Does lmm only work with Steam games?

No. lmm works with almost any game that loads mods from a folder. Steam games
are found automatically; any other game takes a minute to
[add by hand](add-a-game.md).

### Which mod sites does it support?

NexusMods, CurseForge, Thunderstore and the Steam Workshop. You can also
point lmm at a folder of mods, or at a mod list hosted somewhere else; the
[README's Custom Sources section](https://github.com/DonovanMods/linux-mod-manager#custom-sources)
explains how.

### Does lmm change my game's files?

Only by adding mods to it. lmm records every file it puts in the game folder,
and if a mod has to replace one of the game's own files, lmm keeps the
original and puts it back when the mod is removed. [Purging](troubleshooting.md#back-to-an-unmodded-game)
takes everything back out.

### Does lmm need to run while I play?

No. Once mods are installed, start the game the way you normally do.

### Is the web interface on the internet?

No. `lmm serve` runs on your own computer and only your computer can open
it. It doesn't load anything from the internet, either; the only outside
connections are to the mod sites themselves.

### Where does lmm keep things?

| What                                  | Where                        |
| ------------------------------------- | ---------------------------- |
| Settings, games and profiles          | `~/.config/lmm/`             |
| Downloaded mods, and lmm's records    | `~/.local/share/lmm/`        |

If you back up your computer, back up both. Your saved API keys are
encrypted with a key file in `~/.local/share/lmm/` as well, so keep that with
the rest.

### Does it cost anything?

No. lmm is free and open source. If you'd like to support its development,
there's an optional
[GitHub sponsorship](https://github.com/sponsors/dyoung522).

## Glossary

**API key**
: A code from your account on a mod site that lets lmm download on your
  behalf. See [connecting your mod sites](first-mods.md#3-connect-your-mod-sites).

**Archive**
: A `.zip`, `.7z` or `.rar` file that a mod comes packed in.

**BepInEx**
: A mod loader used by many Unity games. See
  [Unity games and BepInEx](bepinex.md).

**Conflict**
: Two mods that include the same file. The one later in the load order wins.

**Deploy**
: Putting a profile's mods into the game folder. Installing deploys a mod
  straight away; the **Deploy** button brings the game folder up to date if
  anything is waiting.

**Load order**
: The order mods are applied in. When mods overlap, the later one wins.

**Lock**
: Holding a mod at one exact version. See
  [Locking a mod](updates.md#locking-a-mod-to-one-version).

**Mod folder**
: The folder inside a game where its mods go, often `Mods` or `Data`.

**Profile**
: A named list of mods for one game. See [Profiles](profiles.md).

**Purge**
: Taking every mod out of the game, without forgetting them.

**Snapshot**
: A saved restore point for your mod setup. See
  [snapshots](troubleshooting.md#save-a-restore-point-snapshots).

**Source**
: A place lmm gets mods from, such as NexusMods or a folder on your disk.

**Verify**
: Checking that installed mods are complete and unchanged.
