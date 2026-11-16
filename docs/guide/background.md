---
title: Run the web UI in the background
nav_order: 12
---

# Run the web UI in the background

`lmm serve` stops when you close the terminal it runs in. If you'd rather
have the web interface there all the time, lmm can run it as a background
service that starts when you log in. Then you just open
<http://127.0.0.1:7420/> in your browser whenever you want it.

lmm uses systemd for this, which almost every Linux distribution has. The
service is called `lmm-serve`, and it runs as you, not as root, so it uses
the same games, mods and logins as your `lmm` commands.

## If you installed lmm from a package

The `.deb`, `.rpm` and `.apk` packages and the Arch `PKGBUILD` already
include the service, turned off. Turn it on with:

```bash
systemctl --user enable --now lmm-serve
```

To turn it off again:

```bash
systemctl --user disable --now lmm-serve
```

## If you installed lmm any other way

If you unpacked the `.tar.gz` file, or built lmm yourself, let lmm set the
service up for you:

```bash
lmm service install
```

That writes the service file to `~/.config/systemd/user/` (or wherever
systemd looks for your own services, if you've moved your config folder),
pointing at the `lmm` program you ran, then turns the service on and starts
it. A moment later it checks the service is still running, and prints the
address the web interface is on, or tells you to look at its log if it
stopped.

The service uses the same games, mods and settings as the `lmm` you ran it
from. If you gave `lmm service install` the `--config` or `--data` options,
or you set `XDG_CONFIG_HOME` or `XDG_DATA_HOME` in your shell, the service
gets those too.

To use a different address, give it the same `--addr` you would give
`lmm serve`:

```bash
lmm service install --addr 127.0.0.1:8080
```

Running `lmm service install` again, say with a new address, or after you
move the `lmm` program, rewrites the service file and restarts the service,
and says what changed:

```text
Updated lmm-serve.service to run /…/go/1.27.4/bin/lmm (was /…/go/1.27.3/bin/lmm); restarted.
```

When there was nothing to change, it says "Already installed".

lmm already installs new versions of itself, after a command or from the
background web interface (see
[Update lmm automatically](updates.md#update-lmm-automatically)). Whatever
installed the new version only replaces the `lmm` program; the service
notices it within a minute and restarts itself onto it once nothing is
running in the web interface. On a machine where you rarely use lmm, you can
add a daily timer that installs updates too, with `--auto-update`:

```bash
lmm service install --auto-update
```

To take the service out again:

```bash
lmm service uninstall
```

{: .note }
If a package already installed the service, `lmm service install` doesn't
make a second copy. It tells you the `systemctl` command that turns the
packaged one on instead. If you have both, `lmm service uninstall` turns the
packaged one off as well, because the two share the setting that turns the
service on; it prints the command that turns the packaged one back on.

lmm only changes or removes a service file it wrote itself. If you wrote
your own `lmm-serve.service` in `~/.config/systemd/user/`, or put a link
there, lmm leaves it alone and tells you to change or remove it yourself.

## Is it running?

```bash
lmm service status
```

This shows whether the service is installed (and whether lmm or a package
put it there), whether it starts when you log in, whether it is running
now, and whether it starts at boot. Add `--json` for a version a script can
read.

It also shows which `lmm` program the service runs, and that program's
version:

```text
lmm-serve.service
  installed:  yes, by lmm service install (/home/you/.config/systemd/user/lmm-serve.service)
  enabled:    yes (enabled)
  running:    yes (active)
  binary:     /home/you/go/bin/lmm (2.3.0)
```

If that isn't the `lmm` you are running, it says so, and how to fix it.
This happens when a new build of lmm goes somewhere else than the old one
did, for example after an update of the Go toolchain that `make install`
or `go install` builds with: the service keeps running the old program
until you point it at the new one.

```text
! lmm-serve.service runs /…/go/1.27.3/bin/lmm (2.3.0-39); you are running /…/go/1.27.4/bin/lmm (2.3.0-484).
  Run 'lmm service restart --switch' to point the service at this binary and restart it (or 'lmm service install' with the options you installed it with).
```

The web interface shows the same thing under **Setup** > **Updates**, as
**Background service**.

A service a package installed always runs the package's program, which lmm
never changes. If you run another `lmm` (one you built yourself, say), the
warning says how to run that one instead: with `lmm serve`, or by
overriding the service's `ExecStart` with
`systemctl --user edit lmm-serve`.

If it isn't running, its log says why:

```bash
journalctl --user -u lmm-serve
```

## Restarting it

```bash
lmm service restart
```

This stops the service and starts it again, or just starts it if it was
stopped, then tells you whether it is running. You don't need to uninstall
and install it again to restart it. Restart it after you change something
the service only reads when it starts, such as an API key you stored or its
`PATH` (see below). It works the same whether lmm or a package installed
the service, and it is the same as `systemctl --user restart lmm-serve`.

A restart stops anything the web interface is in the middle of, as closing
`lmm serve` would, so wait until it has finished. It doesn't turn the
service on: if you turned it off, it won't start again the next time you log
in.

If the service runs a different `lmm` program from the one you ran
`lmm service restart` with, the restart warns about it (see
[Is it running?](#is-it-running)) and restarts the service on its old
program all the same. To move it to the program you are running, add
`--switch`:

```bash
lmm service restart --switch
```

That changes only which program the service file runs. Its address, and
the `--config` and `--data` it was installed with, stay as they are. lmm
only does this for a service file it wrote; it never rewrites a package's.
If you changed the service's `ExecStart` with `systemctl --user edit
lmm-serve`, that change still wins after the switch, and `--switch` says
so and fails rather than claim it worked: `systemctl --user cat lmm-serve`
shows the change, and `systemctl --user edit lmm-serve` removes it.

## Starting at boot, without logging in

The service starts when you log in. If you want it running from the moment
the computer starts, for example to reach it from another device on your
network, turn on "lingering" for your user once:

```bash
loginctl enable-linger $USER
```

This is a setting for your whole user account, so lmm only tells you about
it and never changes it for you. `lmm service status` shows whether it is on.

{: .warning }
The web interface has no password. It only listens on your own computer
(`127.0.0.1`) unless you give it another address. Only do that on a network
you trust: anyone who can reach the address can manage your mods.

## Things to know

- The service can write anywhere `lmm` can, so mods deploy into your game
  folders as usual, wherever they are.
- A hook script (a script you set lmm to run around a deploy) can't use
  `sudo` when the service runs it.
- The service doesn't see your shell's `PATH`: it gets systemd's, which
  `systemctl --user show-environment` shows. Programs lmm runs, such as
  `7z`, `unrar` and `steamcmd`, must be somewhere on that one. If one is
  only on your shell's `PATH` (say, added in `~/.bashrc`), set `PATH` in a
  file under `~/.config/environment.d/`, which takes effect the next time
  you log in; or, for now, `systemctl --user set-environment PATH=...`
  followed by `lmm service restart`.
- The service doesn't see API keys you `export` in your shell, such as
  `CURSEFORGE_API_KEY` or `NEXUSMODS_API_KEY`. It sees keys you stored with
  `lmm auth login <source>` or in the web interface (**Setup** >
  **Authentication**), and variables set in a file under
  `~/.config/environment.d/`. Without a key, a source such as CurseForge
  can't check for updates or verify versions. `lmm service install` and
  `lmm service status` warn about each key that comes only from your shell;
  a variable the service already gets from systemd (an `environment.d`
  file, or `systemctl --user import-environment`) doesn't count. The web
  interface offers **Add API key…** wherever a source asks for
  one. After storing a key, run `lmm service restart`. lmm
  never copies a key into the service file, which anyone who can read your
  files could read.
- A game you start with **Play** in the web interface runs as its own
  systemd user service, not as part of lmm's. Restarting `lmm-serve` (an
  update does) leaves the game running. The game needs your desktop's
  display settings, which the service may not have: see
  ["No display to launch the game on"](launch.md#no-display-to-launch-the-game-on).
- If your computer doesn't use systemd, `lmm service` says so and changes
  nothing. Run `lmm serve` in a terminal instead.
