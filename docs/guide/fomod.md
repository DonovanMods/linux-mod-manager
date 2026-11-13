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

- `--fomod-defaults` takes the options the installer itself suggests.
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
- Updating several mods at once (`lmm update --all`, **Update all**) skips
  that mod and says its installer needs answering. Update it on its own to
  answer.
- Automatic updates never ask. The Updates card says the mod was not
  updated and why.

An update whose installer still accepts your answers installs without
asking.
