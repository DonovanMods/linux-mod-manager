---
title: When something goes wrong
nav_order: 13
---

# When something goes wrong

## Every change shows you a preview first

Before lmm changes anything, it shows exactly what it's about to do: which
mods, which files, and where. In the browser you confirm that preview. In the
terminal, most commands that change things take `--dry-run`, which prints
the preview without changing anything:

```bash
lmm uninstall <mod-id> --dry-run
lmm profile switch coop --dry-run
lmm purge --dry-run
```

## Check your mods: verify

**Verify** checks that every mod lmm installed is still complete and
unchanged, and that the game folder holds what it should. If anything is
wrong, a **Health** card appears on the main screen, with a **Repair**
button for each problem lmm can fix by itself.

If a file was deleted from lmm's download cache by hand, verify says the
mod's cache is incomplete and names the file; **Repair** (or
`lmm verify --fix`) downloads it again. The copy in the game folder is
left where it is until then.

A mod you imported from an archive has nothing to download it from. If
the game deploys by copy or hard link, the file in the game folder is then
the only copy left: verify keeps it, names it, and offers no **Repair**.
Import the mod's archive again to rebuild its cache. If the file really
is stale (you removed it from the mod on purpose), verify cannot tell that
from a lost file: import the archive again, or delete the file from the
game folder by hand - verify then lists only its leftover record, which
**Repair** (or `lmm verify --fix`) drops.

A mod's cache should hold only the mod's own files. If it holds a
symbolic link, or a special file such as a named pipe, verify names the
mod and the path (**LINK IN CACHE** in the terminal). An archive unpacked
by an older lmm could leave one there. lmm never deploys or follows one,
and **Repair** (or `lmm verify --fix`) deletes the link itself, never what
it points to. If the link stood in place of one of the mod's own files,
verify also reports that file as missing from the cache, and the same
repair downloads it again. A mod imported from an archive has to be
imported again instead.

The **Verify** button is in the library toolbar. In the terminal:

```bash
lmm verify
lmm verify --fix
```

While a job is changing the game's files (an install, an update, a
deploy, or the automatic updates), the Health card doesn't check them
half-written: it says **Checking after the current job finishes…**, or
shows the last check and says a job is changing the game, and checks again
once the job is done. Until then the mod panel and the full mod page head
those findings **Last check**, and the library doesn't offer **Repair…**
or count them under the **Unhealthy** filter. `lmm verify` run while
another lmm, such as `lmm serve`, is changing the files stops with a
message naming that process instead of a report; run it again when that
finishes. If the other lmm only changed the files during the check and has
already finished, `lmm verify` says so and checks them again by itself.

If a source needs an API key that lmm doesn't have, the Health card says so
for each of its mods, with an **Add API key…** button that opens
**Setup** > **Authentication** at that source. In the terminal, `lmm verify`
names the command that stores the key, such as
`lmm auth login curseforge`. If the source turned down a key you gave it,
the line says the key was rejected; when that key comes from a variable
such as `CURSEFORGE_API_KEY`, fix or unset the variable, since it wins over
a key stored with `lmm auth login`.

## Turn one mod off

Not sure which mod is causing trouble? Turn mods off one at a time with the
switch at the start of each row in the library. A switched-off mod is taken
out of the game but kept, so switching it back on doesn't download anything.

```bash
lmm mod disable <mod-id>
lmm mod enable <mod-id>
```

## Two mods change the same file

When two mods both include the same file, only one can win. A **Conflicts**
card on the main screen lists these, and **Resolve…** lets you change the
load order so the right mod wins: the mod lower in the list wins. In the
terminal, `lmm conflicts` lists them and `lmm profile reorder` changes the
order.

### A conflict you meant

Often one mod is meant to replace another's files: a patch over the mod it
fixes, or a collection whose order exists so that one of its mods wins.
**Mark as intended** on a Conflicts card row says so. The conflict leaves
the card, and the mods' library rows show a muted ⇄ in place of the warning.
**Mark all as intended** accepts every conflict on the card as it stands.
Marked conflicts are listed under **N known conflicts**, below the cards,
each with **Unmark**.

A mark is for the two mods, not one file: "Mod B overrides Mod A" covers
every file they share, including one they come to share after an update. It
records which mod wins. If you reorder so the other mod wins, the conflict
is listed again, because that is not what you marked. The reorder preview
shows this before you save: a known winner is labelled **known**, and an
order that makes it lose says **no longer intended**. A file that three
mods share is known only once its winner is marked over both of the
others.

In the terminal:

```bash
lmm conflicts                          # known conflicts are counted, not listed
lmm conflicts --all                    # list them too
lmm conflicts ack <winner> <loser>     # Mod B overrides Mod A: intended
lmm conflicts ack --all-current        # accept every conflict as it stands
lmm conflicts unack <winner> <loser>   # list it again
```

A collection's own conflicts are marked for you: see
[A collection's own conflicts](profiles.md#a-collections-own-conflicts).
Uninstalling a mod removes its marks.

Removing the winning mod (uninstalling it, switching it off, or updating it
to a version without the file) puts the next mod's copy in its place
straight away, and lmm says which mod has the file now. The game's own file
comes back only once no switched-on mod includes it.

The preview says so before you confirm. The web interface's plan for an
uninstall, an update from a file, a rollback, a copy that replaces a mod,
`profile apply` or a profile switch lists the shared files and the mod each
one is expected to pass to, for example "Data/foo.dll passes to Mod B (from
Mod A)". It is a forecast: lmm decides again when it runs, from what is
there then, and says which mod has each file afterwards. In
the terminal, `lmm uninstall --dry-run`, `lmm update rollback`,
`lmm update --from-file`, `lmm mod copy --replace`, `lmm profile apply` and
`lmm profile switch` print the same list, grouped by the mod that takes the
files (`--dry-run --json` has it as the plan's `handoffs`). An update from a
mod site doesn't list them: lmm can't tell which files the new version drops
until it has downloaded it. Switching a mod off with `lmm mod disable` (or the
library's switch) has no preview, and says which mod has the file afterwards.

## Save a restore point: snapshots

A **snapshot** records your whole setup for a game: which mods, which
versions, which are switched on. If an experiment goes badly, restore the
snapshot to get back to exactly that setup.

Take one before big changes, with **Snapshot now** in the **Snapshots**
card below the library, or:

```bash
lmm snapshot create --name before-big-update
lmm snapshot list
lmm snapshot restore before-big-update
```

Snapshots are small: they record what you had, and lmm keeps the mod files
themselves anyway. Restoring takes a snapshot of your current setup first,
so a restore can be undone too.

{: .tip }
To have lmm take a snapshot automatically before every deploy, profile
switch and update, add `auto_snapshot: true` to `~/.config/lmm/config.yaml`.
It keeps the last 10.

## Back to an unmodded game

To take every mod out of the game, use **Purge** in the Profiles window, or:

```bash
lmm purge
```

The game folder goes back to how it was before lmm touched it. If a mod
replaced one of the game's own files, lmm kept the original and puts it back.

Your mods stay in lmm's library, so getting them back is one step:

```bash
lmm deploy
```

## "Written by a newer lmm"

If you go back to an older lmm, for example by downgrading the package, it
may refuse to start and say the database was written by a newer lmm. A
newer lmm changed the database in a way the older one can't read, so the
older one stops instead of getting your mods wrong. Install the newer lmm
again; your mods and profiles are still there.

An lmm that is already running can meet the same thing: a web UI (`lmm
serve`) started before you upgraded, or a command waiting for another to
finish, while a newer lmm opens the database and upgrades it. The running
one then refuses every change, saying another, newer lmm upgraded the
database. The web UI shows a banner saying the same and stops answering
until it is restarted. Restart it on the newer lmm: if it runs as a
service, run `lmm service install` from the newer lmm; otherwise stop
`lmm serve` and start it again. Restarting the old one won't help, since it
would refuse again.

## Still stuck?

Search the [issue tracker](https://github.com/DonovanMods/linux-mod-manager/issues),
or open a new issue. It helps to include your lmm version (`lmm --version`),
the game, and what you were doing when it went wrong.
