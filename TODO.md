# TODO

All current items have been completed.

## Completed

1. ~~We need a way to set a default game from the CLI, so we don't have to provide it for every instance of the command~~ - Added `lmm game set-default`, `show-default`, and `clear-default` commands
2. ~~The install tool displays the file results for the mods (which is what we want), but the MAIN file(s) should always be first, followed by the OPTIONAL file(s). Let's skip showing the ARCHIVED files unless the user explicitly asks for them via a flag~~ - Added `--show-archived` flag and file sorting by category priority
