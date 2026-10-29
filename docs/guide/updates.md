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

## Updating lmm itself

Once a day, lmm asks GitHub whether a newer version of lmm is out. The first
time it does, it tells you so in the terminal: after the command you ran, or
in the window running `lmm serve`. When there is one:

- the browser shows a notice across the top of the page with a link to the
  release notes. Close it with its **✕** button: that hides the notice for
  that version only, and it comes back when an even newer version appears.
- the terminal prints a one-line notice after a command, with the command
  that upgrades lmm when your package manager installed it.

To check yourself, and see how lmm was installed:

```bash
lmm version
lmm version --check
```

`--check` asks GitHub right now instead of using the answer from earlier
today. To turn the daily check off, add this line to `~/.config/lmm/config.yaml`:

```yaml
update_check: false
```

`lmm version --check` still works when the daily check is off.

### Let lmm update itself

If you installed lmm from the `.tar.gz` download, or built it yourself, lmm
can install the new version for you:

```bash
lmm self-update
```

It tells you which version it found and asks before it changes anything.
Add `--yes` to skip the question, or use `--check` to only see whether
there's a new version. Before it installs anything, lmm checks that the
download really is an lmm release: it must carry the lmm release
signature, and the file must match its published checksum. If either check
fails, lmm stops and leaves your copy alone.

If the background web interface is running (see
[Run the web UI in the background](background.md)), lmm restarts it so it
uses the new version straight away. If you started `lmm serve` yourself in a
terminal, it keeps running the old version, and the browser shows a notice
asking you to restart it: stop it with **Ctrl+C** and run `lmm serve` again.

`lmm self-update` doesn't work in two cases, and tells you what to do
instead:

- **Your package manager installed lmm** (a `.deb`, `.rpm` or `.apk`
  package, or the AUR). Update it the way you update other programs; lmm
  prints the command.
- **lmm is in a folder you can't write to**, such as `/usr/local/bin`. lmm
  never asks for your password or uses `sudo`. Move `lmm` into a folder of
  your own, such as `~/.local/bin`, and run `lmm self-update` from there.

### Update lmm automatically

lmm can also install new versions on its own, once a day. This is off unless
you turn it on, in two steps. First, set up the daily timer (this also sets
up the background web interface):

```bash
lmm service install --auto-update
```

Then add this line to `~/.config/lmm/config.yaml`:

```yaml
auto_update: true
```

The timer runs once a day at a random time in the first hour after
midnight, or the next time you log in if your computer was off. To stop
automatic updates, change that line to `auto_update: false` or remove it;
the timer keeps running but does nothing. `lmm service uninstall` removes
the timer along with the background web interface.

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
