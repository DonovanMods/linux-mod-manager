---
title: Disk space and the mod cache
nav_order: 12.5
---

# Disk space and the mod cache

lmm keeps the files of every mod version it installs in its **cache**
(`~/.local/share/lmm/cache` unless you changed it). That is why a rollback,
a profile switch or a reinstall needs no download. Without a limit, though,
a mod that updates every week fills the cache with versions you will never
go back to.

## What lmm keeps

For each mod, the cache keeps:

1. every **installed** version: the one any of the game's profiles has;
2. the **3 newest other versions**, newest first. This is the
   `cache.keep_previous_versions` setting, so 3 is only the default;
3. on top of those, every older version something still needs:
   - the version a mod would roll back to (`lmm update rollback`);
   - a version a profile is locked or pinned to, or a collection pins;
   - a version another profile has installed;
   - a version a [snapshot](troubleshooting.md#save-a-restore-point-snapshots)
     records, so restoring the snapshot never depends on the mod's site
     still offering the file.

A version that is both needed and among the 3 newest counts as one of the
3. So after updating a mod from 1.0 through 5.0, lmm keeps 5.0 (installed)
and 4.0, 3.0 and 2.0. Version 1.0 is removed, unless something above still
needs it.

When lmm can't read something that might need a version (a profile, a
snapshot), it removes nothing from that game's cache and tells you why.
To let go of versions that only an old snapshot needs, delete the snapshot.

## When lmm cleans up

lmm removes a mod's old versions by itself right after an update, a
rollback, a profile apply or switch, or a collection update changes that
mod's version. The preview before you confirm says how much that frees, for
example "frees 1.2 GB (2 old versions)". If the cleanup fails, the update
still counts and you get a warning.

Mods that haven't updated since you started using a version of lmm with
this feature are not cleaned up until they update, or until you prune the
cache yourself.

## See how much space the cache takes

In the browser, go to **Setup → Storage**. It lists each game's cache: its
size, how many mods and versions it holds, and how much a cleanup would
free.

In the terminal:

```bash
lmm cache
```

## Clean up the whole cache

In **Setup → Storage**, click **Prune this game…** or **Prune every
game…**. The preview lists every version it will remove, with its size,
before you confirm.

In the terminal, look first, then prune:

```bash
lmm cache prune --dry-run
lmm cache prune
lmm cache prune --game skyrim-se
```

## Keep more, or fewer, versions

In **Setup → Storage**, choose how many previous versions to keep per mod.
In the terminal:

```bash
lmm cache keep 5          # keep 5 previous versions
lmm cache keep 0          # keep only installed (and needed) versions
lmm cache keep unlimited  # never remove anything
```

The same setting lives in `config.yaml`:

```yaml
cache:
  keep_previous_versions: 3
```

Changing it removes nothing by itself. The next update of a mod, or a
prune, applies it.
