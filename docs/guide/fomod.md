---
title: Mods with installer options (FOMOD)
nav_order: 8
---

# Mods with installer options (FOMOD)

Some mods come with a small installer that asks which parts you want: a
texture size, a version for a particular game edition, optional patches for
other mods. On NexusMods these are called FOMOD installers, and they are
common for Skyrim, Fallout, Cyberpunk 2077, The Witcher 3 and Baldur's
Gate 3.

lmm runs these installers itself, on every game. You answer the questions,
lmm installs only what you chose, and it remembers your answers in the
profile. Next time (a reinstall, a profile import on another machine, a
switch back to the profile) it installs the same files without asking
again.

lmm never runs installers that contain a script (`fomod/script.cs`) or
OMOD installers: those are programs, and lmm refuses to install them.

## In the web interface

Install the mod as usual. lmm downloads it, sees the installer, and opens a
window with the installer's first page of questions instead of installing
straight away.

- Each page is one step of the installer. Pick your options and press
  **Next**, or **Finish** on the last page.
- Some groups take one option (round buttons), some take any number
  (check boxes). Options marked _required_ are always installed; options
  marked _not usable_ can't be picked.
- Each option shows its description and, if the installer has one, its
  picture.
- **Use the installer's defaults** answers with what the installer itself
  suggests, without going through the steps (for one mod at a time when
  several ask). It is the same as `--fomod-defaults` in the terminal.
- If one of your answers changes which steps come next, lmm checks the
  installer again and opens the window once more at the next step. This
  doesn't download the mod again.

If you close the window, nothing is installed. The failed install in the
activity tray keeps an **Answer installer…** button, so you can come back
to it.

## In the terminal

`lmm install` asks the same questions in the terminal, one step at a time.
Each group lists its options with their descriptions. Press Enter to keep
the options already marked with `[x]`, or type the numbers you want
(`2`, or `1,3` where a group takes several). Type `none` to pick nothing in
a group that allows it, and `q` to stop without installing anything.

The same happens in every command that can install a mod: `lmm install`,
`lmm profile import`, `lmm import <archive>`, `lmm update` and
`lmm update --from-file`.

### Answering without questions

When there is no terminal to ask in (a script, a cron job, `--json`), give
the answers some other way:

- `--fomod-defaults` takes the options the installer itself suggests, even
  when you are changing choices you made before.
- `--fomod-choices <file.json>` takes your answers from a file.

If neither is given, lmm stops with an error that lists the installer's
questions (with `--json`, the error's `details` holds the whole question
document) and exits with a non-zero status. It never picks options for you
without being asked to.

A choices file is the `fomod:` block lmm records in the profile, written as
JSON:

```json
{
  "steps": [
    {
      "name": "Main Files",
      "groups": [{ "name": "Texture Resolution", "choices": [{ "name": "2K" }] }]
    }
  ]
}
```

A NexusMods collection's `choices` list for one mod works too. When several
mods ask at once, key the answers by mod as `source:id`:

```json
{
  "nexusmods:12604": { "steps": [ ... ] },
  "nexusmods:3863": { "steps": [ ... ] }
}
```

## Mods with an installer in each file

Some mods are installed from more than one file, and each file has its
own installer: a NexusMods collection might pin a mod's main file and a
patch file, each a FOMOD. lmm runs each file's installer and remembers the
answers per file (under `fomod_files:` in the profile, by file id).

- To install several files of a mod yourself, check each one in the web
  interface's install preview, or name them with
  `lmm install <mod> --file <id>,<id>` in the terminal.
- The questions come one installer at a time, each headed with the file it
  belongs to, in the same window (or terminal walk) as any other.
- In a choices file, key each file's answers as `source:id#file-id`, as
  the error that lists the questions shows them:

  ```json
  {
    "nexusmods:43532#100": { "steps": [ ... ] },
    "nexusmods:43532#101": { "steps": [ ... ] }
  }
  ```

- To change one file's answers, pick its installer: in the web interface,
  **Reconfigure installer…** lists them; in the terminal, run
  `lmm mod fomod <mod-id> --file <file-id>`, or leave `--file` out to pick
  from a list (with no terminal, every installer is asked). The other
  files keep their answers.
- An update asks only about a file whose installer changed.
- When two of the installers install the same file, one of them wins, the
  same one every time: the files are laid out in file id order, the ids
  compared as text (so `10` comes before `9`), and the last one laid out
  wins, unless the profile's `fomod_order:` list names an order. A
  NexusMods collection sets that list from its rules between the mod's
  files.
- An update gives each file's answers to the file that replaces it,
  matched by NexusMods' own replacement, else by the file's name or
  category. A file it cannot match is asked about again.

A collection or a shared profile that records answers per file needs an
lmm that understands them; an older lmm refuses to import it rather than
install it wrong.

## Collections: the curator's answers

A collection (a NexusMods collection, or an lmm collection someone shared)
can record the curator's answers for a mod's installer. lmm replays them
instead of asking, with three checks first:

- **The archive must be the curator's.** A collection records each file's
  md5, the fingerprint of the archive the answers were chosen for. lmm
  compares it with the archive it downloaded (the download already computes
  it, so this costs nothing extra). If they differ, for example because the
  author replaced the file on NexusMods, lmm doesn't replay answers chosen
  for another archive. It asks the installer's questions instead, with the
  curator's answers selected, and says why: the terminal prints both md5s,
  and the web interface's installer window shows them in its note. A
  collection that records no md5 replays as before.

  The check also covers mods installed later, for example after
  `lmm profile import --no-install`: until a mod's answers have been
  installed once, lmm reads the md5 from the recipe of the NexusMods
  collection the profile came from. If that recipe can't be read (offline,
  say), lmm installs with the curator's answers and warns that it could not
  check the archive. Once a mod is installed, lmm records the installer it
  answered, and doesn't check the archive again. An lmm collection file
  that isn't from NexusMods has no recipe to read back, so after a
  `--no-install` import its md5s are not checked.
- **Install phases.** A NexusMods collection installs its mods in phases.
  When an installer asks whether another mod's file is there (a patch that
  installs only when its target is), it sees the mods of earlier phases,
  and never those of its own phase or a later one, exactly as Vortex
  installs a collection.
- **Plugin state.** See the next section.

## Installers that check for other files

Some installers install a file only when another mod's file is there, or
only when a plugin is active. lmm answers from the profile you are
installing into: a file is there when one of the profile's mods provides it
or the game itself has it.

For Skyrim, Fallout 4, Starfield and the other games whose plugin list lmm
manages, whether a plugin is _active_ comes from that list. A plugin you
turned off (`lmm plugins disable`, or the Plugins tab in the web
interface) is inactive, so an installer that adds a patch only for an
active plugin leaves the patch out. A plugin the game's own `Plugins.txt`
leaves off is inactive too, and the game's own master files are always
active. For other games, a file that is there counts as active.

lmm records what the installer checked with your answers, so reinstalling
the mod later gives the same files even if the profile changed since.

## Changing your choices later

To change what an installed mod's installer installed:

- In the web interface, open the mod's page and press **Reconfigure
  installer…**.
- In the terminal, run `lmm mod fomod <mod-id>`.

lmm fetches the same version of the mod again, asks the installer's
questions with your current answers already selected, and replaces the
mod's files in the game folder with the new selection. The mod keeps its
version; only the chosen files change.

A mod you installed before lmm ran FOMOD installers went in as the whole
archive, installer and all. Reconfiguring it is how you run its installer
now: the questions start from the installer's own suggestions, and your
answers replace the files the old install put in the game folder.

## When an update changes the installer

If a new version of a mod changes its installer so that your recorded
answers no longer fit (an option was renamed or removed), lmm doesn't guess:

- `lmm update <mod-id>`, and updating one mod in the web interface, ask the
  installer's questions again, with your old answers selected where they
  still apply.
- Updating several mods at once skips that mod and says its installer
  needs answering. In the terminal, `lmm update --all` then asks (or takes
  `--fomod-choices`/`--fomod-defaults`) and updates it; in the web
  interface the installer window opens.
- Automatic updates never ask. The Updates card says the mod was not
  updated and why.

An update whose installer still accepts your answers installs without
asking.
