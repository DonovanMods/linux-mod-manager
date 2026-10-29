---
title: Run the web UI in the background
nav_order: 9
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

That writes the service file to `~/.config/systemd/user/`, pointing at the
`lmm` program you ran, then turns the service on and starts it. It prints
the address the web interface is on.

To use a different address, give it the same `--addr` you would give
`lmm serve`:

```bash
lmm service install --addr 127.0.0.1:8080
```

Running `lmm service install` again, say with a new address, or after you
move the `lmm` program, rewrites the service file and restarts the service.

To take the service out again:

```bash
lmm service uninstall
```

{: .note }
If a package already installed the service, `lmm service install` doesn't
make a second copy. It tells you the `systemctl` command that turns the
packaged one on instead.

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
- If your computer doesn't use systemd, `lmm service` says so and changes
  nothing. Run `lmm serve` in a terminal instead.
