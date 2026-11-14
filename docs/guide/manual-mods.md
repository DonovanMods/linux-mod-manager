---
title: Mods you downloaded yourself
nav_order: 7
---

# Mods you downloaded yourself

Sometimes lmm can't download a mod for you:

- the mod's author has turned off downloads through apps,
- your NexusMods account isn't Premium (for a whole collection at once, see
  [below](#a-nexusmods-collection-on-a-free-account)), or
- the mod isn't on any site lmm connects to.

In every case you download the file in your web browser yourself, and then
give it to lmm. lmm still installs it, keeps track of its files, and can take
it out again later.

## When an install or update fails

If lmm can't download a mod you asked for, it tells you, with a link to the
mod's page. In the browser, an **Install from file…** button (or **Update
from file…** for an update) appears right next to that link:

1. Click the link and download the file from the mod's page.
2. Click **Install from file…** and choose the file you downloaded.

If the reason is a free NexusMods account, the message says your account
isn't Premium. Nothing about the mod itself is wrong, so lmm keeps trying
to download its updates, and they work as soon as the account (or the API
key) is Premium.

lmm already knows which mod it is, so it can still tell you about updates
for it later. In the terminal, the error message prints the full command to
run, with everything filled in except the file. It looks like this:

```bash
lmm install --id 12345 -s curseforge --file 67890 --from-file ~/Downloads/SomeMod-2.0.zip
lmm update 12345 --from-file ~/Downloads/SomeMod-2.1.zip
```

## A NexusMods collection on a free account

A free NexusMods account can [import a collection](profiles.md#importing-a-nexusmods-collection),
but NexusMods only serves downloads to Premium accounts, so the import
saves the profile with every mod **pending**. Download each mod's file from
the page the import linked, then hand lmm the whole folder at once:

```bash
lmm profile fill "Essential Mods" --game skyrim-se
```

With no folder named, lmm looks in your downloads folder (usually
`~/Downloads`); name another folder, or the files themselves, after the
profile.

lmm recognises each file by its size and checksum, which the collection
records for every file, so the names don't matter (a file your browser
renamed `SkyUI (1).7z` is still recognised). Each match is installed as if
lmm had downloaded it: with the installer choices and the mod type the
collection records, locked when the curator pinned it exactly, and set to
update automatically when the curator asked for the newest file. The plan
lists what will be installed, what is still pending, and every file it
didn't use and why; `--dry-run` shows it without installing anything, and
`-y` skips the question. Run it again as you download more: each run
installs what it finds.

lmm reads only the top level of the folder, never follows a link (a link
you name on the command line is refused too, unless it's the folder
itself), and only reads a file whose size is one of the pending files'
sizes, so pointing it at a big Downloads folder is quick. Your files are
copied, never moved or deleted. You can also name the files themselves
instead of a folder: `lmm profile fill "Essential Mods"
~/Downloads/skse64_2_02_06.7z`.

In the browser, use the import's entry in the activity tray (the bell), or
open **Manage profiles** and click **Fill from downloads…** on the profile.
Then either:

- click **Choose a folder…**, which opens at your downloads folder, and
  pick the folder the files are in. lmm reads the folder exactly as the
  terminal command does, and nothing is uploaded, so there's no limit on
  size or number; or
- drop the files there (or click **Choose files…**). Only files of a
  pending file's size are uploaded, up to four at a time, and none over
  2 GiB (choose the folder for those); the plan says how many more to drop
  once those are installed.

lmm needs the collection's recipe, which the import kept in its cache. If
it's gone, lmm downloads it again (a free account may), and if it can't,
the plan says so and matches nothing. A profile that didn't come from a
NexusMods collection has no checksums to match against: install its mods one
at a time from their files, as described above.

Use `lmm profile fill` rather than [adopting](#mods-already-in-the-game-folder)
a collection's files with `lmm import`: adoption takes files over where
they are and records no mod type, because lmm won't move files it has no
copy of in its cache, while a fill installs each mod with the type the
collection gives it (a script extender into the game folder, for example).

## When the author turns downloads back on

Once a mod's site refuses to let lmm download it, lmm marks the mod
**Manual** and stops trying: update checks still tell you about new
versions, but **Update all** and automatic updates leave it out.

Mod authors can change their minds. On CurseForge, an author can turn
downloads through apps back on. lmm notices this the next time it checks
for updates, from the terminal or in the browser, at no extra cost: the
check already asks the site about every mod. It takes the **Manual** mark
off, and says so once:

```text
SomeMod: automatic downloads are available again - CurseForge serves it through its API now
```

In the browser the same line appears on the **Updates** card until you
next click **Check for updates**. From then on the mod is updated like any
other, including automatically if its update setting is **Auto**. If the
site refuses a download again later, lmm marks the mod **Manual** again.

lmm only removes the mark when the site says plainly that it allows the
download. A site that says nothing either way leaves the mark in place.

## A mod that isn't on any site

To add an archive (a `.zip`, `.7z` or `.rar` file) lmm has no record of, use
**Add mods ▾ → Import an archive…** in the library, or **⚙ Setup → Archive
import**. In the terminal:

```bash
lmm import ~/Downloads/CoolMod-1.0.zip
```

lmm names the mod after the file. It's installed and managed like any other
mod, but since lmm doesn't know where it came from, it can't check it for
updates.

{: .tip }
If you later find the mod on NexusMods or CurseForge, you can tell lmm where
it came from with **Re-link…** in the mod's **⋯** menu (`lmm mod edit` in the
terminal). After that, lmm checks it for updates like any other mod.

## Mods already in the game folder

If you copied mods into the game by hand before using lmm, lmm can find them
and take them over. Use **Add mods ▾ → Adopt untracked mods…** or **⚙ Setup
→ Adopt**. In the terminal:

```bash
lmm import
```

lmm looks up each mod on your connected sites by name. It only matches a mod
when it's confident, so a mod with a similar name is never mistaken for
yours. Whatever it can't match is still adopted and managed, just without
update checks.

## Steam Workshop items

Mods you subscribe to on the Steam Workshop are installed by Steam, and Steam
keeps them up to date. lmm can **track** them, so they show up in your
library and lmm tells you when they've changed. Use **Add mods ▾ → Track
Steam Workshop items…**, or:

```bash
lmm import --workshop
```

lmm never moves or deletes the files Steam manages. To remove a Workshop mod,
unsubscribe in Steam.
