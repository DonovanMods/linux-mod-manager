---
title: Mods you downloaded yourself
nav_order: 7
---

# Mods you downloaded yourself

Sometimes lmm can't download a mod for you:

- the mod's author has turned off downloads through apps,
- your NexusMods account isn't Premium, or
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

lmm already knows which mod it is, so it can still tell you about updates
for it later. In the terminal, the error message prints the full command to
run, with everything filled in except the file. It looks like this:

```bash
lmm install --id 12345 -s curseforge --file 67890 --from-file ~/Downloads/SomeMod-2.0.zip
lmm update 12345 --from-file ~/Downloads/SomeMod-2.1.zip
```

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
