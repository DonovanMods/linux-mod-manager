---
title: Installing lmm
nav_order: 2
---

# Installing lmm

lmm is a single program with nothing else to set up. Every release on the
[Releases page](https://github.com/DonovanMods/linux-mod-manager/releases/latest)
has packages for the common Linux distributions, for both regular 64-bit PCs
(`amd64`) and ARM machines (`arm64`). Most people want `amd64`.

In the commands below, replace `<version>` with the version number shown on
the Releases page, for example `2.0.0`.

## Debian, Ubuntu, Linux Mint, Pop!_OS

Download the `.deb` file for your machine, then:

```bash
sudo apt install ./lmm_<version>_linux_amd64.deb
```

## Fedora, Nobara, RHEL

Download the `.rpm` file, then:

```bash
sudo dnf install ./lmm_<version>_linux_amd64.rpm
```

## openSUSE

```bash
sudo zypper install ./lmm_<version>_linux_amd64.rpm
```

## Arch, CachyOS, EndeavourOS, Manjaro

lmm isn't on the AUR yet, but every release includes a `PKGBUILD` you can
build with `makepkg`:

```bash
mkdir lmm-bin && cd lmm-bin
curl -LO https://github.com/DonovanMods/linux-mod-manager/releases/latest/download/PKGBUILD
makepkg -si
```

## Alpine

```bash
sudo apk add --allow-untrusted lmm_<version>_linux_amd64.apk
```

## Any other distribution

Download the `.tar.gz` file, unpack it, and put the `lmm` program somewhere
on your `PATH`:

```bash
tar -xzf lmm_<version>_linux_amd64.tar.gz
sudo mv lmm /usr/local/bin/
```

## Optional: 7-Zip

lmm opens `.zip` archives by itself. Some mods are packed as `.7z` or `.rar`
instead, and for those lmm uses the `7z` program. Most distributions call
the package `p7zip` or `7zip`. The `.deb` and `.rpm` packages suggest it for
you; otherwise, install it if a mod ever fails to unpack.

## Check that it worked

```bash
lmm --version
```

It prints the version you installed, for example **lmm version 2.0.0**. Next:
[set it up and install your first mods](first-mods.md).

{: .note }
The distribution packages also install shell tab-completion and the manual
pages, so `man lmm` works too.
