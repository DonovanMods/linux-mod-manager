---
title: Run the web UI in the background
nav_order: 10
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
move the `lmm` program, rewrites the service file and restarts the service.

To have lmm also install new versions of itself once a day, add
`--auto-update` (see [Update lmm automatically](updates.md#update-lmm-automatically)):

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

If it isn't running, its log says why:

```bash
journalctl --user -u lmm-serve
```

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
  followed by `systemctl --user restart lmm-serve`.
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
  one. After storing a key, run `systemctl --user restart lmm-serve`. lmm
  never copies a key into the service file, which anyone who can read your
  files could read.
- If your computer doesn't use systemd, `lmm service` says so and changes
  nothing. Run `lmm serve` in a terminal instead.
