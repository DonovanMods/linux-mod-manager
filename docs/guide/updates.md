---
title: Updating, rolling back and locking
nav_order: 6
---

# Updating, rolling back and locking

## Checking for updates

On the main screen, an **Updates** card appears whenever a mod has a newer
version. From there you can:

- click **Update all** to take every update, or
- tick just the mods you want and update those.

Each mod in the library also gets an **Update** button when it has one
waiting. **Check for updates** in the library toolbar asks the mod sites
again right away.

In the terminal:

```bash
lmm update
lmm update <mod-id>
```

`lmm update` on its own lists the mods that have updates. Give it a mod's id
to update that mod. `lmm list` shows each mod's id.

## Going back to an older version

If an update breaks something, roll it back. In the browser, open the mod
(click **More info →** in its side panel) and use the **Versions** table to
install any earlier version. In the terminal:

```bash
lmm update rollback <mod-id>
```

## Choosing how each mod updates

Each mod has an update setting:

| Setting             | What it means                                              |
| ------------------- | ---------------------------------------------------------- |
| **Notify** (normal) | lmm tells you about updates, and you decide                |
| **Auto**            | lmm installs updates for this mod when it checks           |
| **Pinned**          | lmm stops checking this mod for updates at all             |

Change it in the mod's side panel, or in the terminal:

```bash
lmm mod set-update <mod-id> --auto
lmm mod set-update <mod-id> --notify
lmm mod set-update <mod-id> --pin
```

## Skipping one update

Not ready for a particular update? Skip it. lmm stops mentioning that version
and tells you again when a newer one comes out.

```bash
lmm mod skip-update <mod-id>
lmm mod unskip-update <mod-id>
```

## Locking a mod to one version

**Pinned** only stops lmm *asking* about updates. A **lock** goes further:
it says "this profile uses exactly this version". lmm then refuses to update
the mod, and returns it to that version whenever the profile is deployed or
applied, until you unlock it.

Lock a mod when a newer version would break your setup, or when you share a
profile and everyone needs the same version.

In the browser, use the lock in the mod's side panel, or **Lock** in the
mod's **⋯** menu. In the terminal:

```bash
lmm mod lock <mod-id>
lmm mod lock <mod-id> 1.2.3
lmm mod unlock <mod-id>
```

With no version, the lock holds the version you have now. Locking to a
different version changes the record straight away; the game folder catches
up the next time you deploy or apply the profile.

{: .note }
A lock needs a mod site that can find old versions of a mod: NexusMods,
CurseForge, Thunderstore and similar. For a mod lmm can't look up, such as
one you added from your own download, use **Pinned** instead.
