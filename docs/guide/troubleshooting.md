---
title: When something goes wrong
nav_order: 9
---

# When something goes wrong

## Every change shows you a preview first

Before lmm changes anything, it shows exactly what it's about to do: which
mods, which files, and where. In the browser you confirm that preview. In the
terminal, most commands that change things take `--dry-run`, which prints
the preview without changing anything:

```bash
lmm uninstall <mod-id> --dry-run
lmm profile switch coop --dry-run
lmm purge --dry-run
```

## Check your mods: verify

**Verify** checks that every mod lmm installed is still complete and
unchanged, and that the game folder holds what it should. If anything is
wrong, a **Health** card appears on the main screen, with a **Repair**
button for each problem lmm can fix by itself.

The **Verify** button is in the library toolbar. In the terminal:

```bash
lmm verify
lmm verify --fix
```

## Turn one mod off

Not sure which mod is causing trouble? Turn mods off one at a time with the
switch at the start of each row in the library. A switched-off mod is taken
out of the game but kept, so switching it back on doesn't download anything.

```bash
lmm mod disable <mod-id>
lmm mod enable <mod-id>
```

## Two mods change the same file

When two mods both include the same file, only one can win. A **Conflicts**
card on the main screen lists these, and **Resolve…** lets you change the
load order so the right mod wins: the mod lower in the list wins. In the
terminal, `lmm conflicts` lists them and `lmm profile reorder` changes the
order.

## Save a restore point: snapshots

A **snapshot** records your whole setup for a game: which mods, which
versions, which are switched on. If an experiment goes badly, restore the
snapshot to get back to exactly that setup.

Take one before big changes, with **Snapshot now** in the **Snapshots**
card below the library, or:

```bash
lmm snapshot create --name before-big-update
lmm snapshot list
lmm snapshot restore before-big-update
```

Snapshots are small: they record what you had, and lmm keeps the mod files
themselves anyway. Restoring takes a snapshot of your current setup first,
so a restore can be undone too.

{: .tip }
To have lmm take a snapshot automatically before every deploy, profile
switch and update, add `auto_snapshot: true` to `~/.config/lmm/config.yaml`.
It keeps the last 10.

## Back to an unmodded game

To take every mod out of the game, use **Purge** in the Profiles window, or:

```bash
lmm purge
```

The game folder goes back to how it was before lmm touched it. If a mod
replaced one of the game's own files, lmm kept the original and puts it back.

Your mods stay in lmm's library, so getting them back is one step:

```bash
lmm deploy
```

## Still stuck?

Search the [issue tracker](https://github.com/DonovanMods/linux-mod-manager/issues),
or open a new issue. It helps to include your lmm version (`lmm --version`),
the game, and what you were doing when it went wrong.
