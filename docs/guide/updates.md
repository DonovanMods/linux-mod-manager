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

Checking asks each mod's site in turn, so on a big profile it can take a
few seconds. The rest of the page doesn't wait for it. While the check
runs, a line above the cards says what it is doing and how far it has got,
for example "Checking 42 mods for updates (17/42)…". The Updates card
appears when the check finishes. The verify behind the **Health** card
shows the same kind of line ("Verifying 42 mods…"). Switching game or
profile shows these lines too, under the new profile's own library.

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

Only one update runs at a time. If you start `lmm self-update` while
another is already running (the automatic one, for example), it says so and
stops; run it again once the first has finished.

`lmm self-update` doesn't work in these cases, and tells you what to do
instead:

- **Your package manager installed lmm** (a `.deb`, `.rpm` or `.apk`
  package, or the AUR). Update it the way you update other programs; lmm
  prints the command.
- **lmm is in a folder you can't write to**, such as `/usr/local/bin`. lmm
  never asks for your password or uses `sudo`. Move `lmm` into a folder of
  your own, such as `~/.local/bin`, and run `lmm self-update` from there.
- **lmm can't tell whether your package manager installed it.** This
  happens when the package manager is busy, for example while another
  update is running. lmm leaves your copy alone; try again in a minute.

### Update lmm automatically

If you installed lmm from the `.tar.gz` download, or built it yourself, lmm
updates itself. You don't need to set anything up. When the daily check
finds a new version, lmm installs it by itself, with the same checks as
`lmm self-update`: the download must carry the lmm release signature and
match its published checksum, or nothing changes.

- **In the terminal**, lmm starts the update in the background after the
  command you ran, and says so. Your command isn't slowed down, and closing
  the terminal doesn't stop the update. A script never starts one.
- **In the browser**, the background web interface installs the update and
  restarts itself onto it. If you started `lmm serve` yourself in a
  terminal, it asks you to restart it instead.

The first time lmm updates itself, it tells you, once, in the terminal and
in the browser:

> lmm updated itself to v2.4.0. To turn this off: auto_update: false in
> config.yaml, or Setup > Updates.

Later updates get a one-line notice. Only one update runs at a time. If an
update fails, lmm waits a day before it tries that version again; the
terminal's background update writes what happened to
`~/.local/share/lmm/auto-update.log`.

To turn automatic updates off, add this line to `~/.config/lmm/config.yaml`:

```yaml
auto_update: false
```

Or, in the browser, open **Setup** (the ⚙ in the top bar), choose
**Updates**, and turn off **Update automatically**. That page also shows
the version you have, the newest release, how lmm was installed, and the
last automatic update. lmm still tells you about new versions while
automatic updates are off; you install them with `lmm self-update`.

lmm never updates itself when your package manager installed it, or when
it's a development build. Setup > Updates says so, and the switch is off.
It also can't update itself from a folder you can't write to, such as
`/usr/local/bin` (see above). If you turned off the daily check
(`update_check: false`), lmm never hears about a new version, so it doesn't
update itself either.

{: .note }
lmm 2.2.0 had automatic updates off unless you turned them on. If you have
2.2.0, run `lmm self-update` once by hand to get the version that turns
them on.

#### A daily timer, for computers where you rarely use lmm

lmm only learns about a new version when you use it. On a computer where
you rarely do, set up a daily timer (this also sets up the background web
interface):

```bash
lmm service install --auto-update
```

The timer runs once a day at a random time in the first hour after
midnight, or the next time you log in if your computer was off. It obeys
`auto_update: false` too. If its update fails, for example because your
network isn't up yet, it tries again five minutes later, up to three more
times. `lmm service uninstall` removes the timer along with the background
web interface.

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
