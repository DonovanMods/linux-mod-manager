# Backlog

Features and improvements deferred for future development.

## Completed Features

### Update Management (v0.3.0)

Per-mod update policies, actual update application, rollback support.

- `lmm update` now applies auto-updates and shows policy column
- `lmm update <mod-id>` updates specific mod
- `lmm update --dry-run` previews updates
- `lmm update rollback <mod-id>` rolls back to previous version
- `lmm mod set-update <mod-id> --auto|--notify|--pin` sets policy

## Deferred Features

### Terminal UI (TUI)

Interactive terminal interface using Bubble Tea framework (originally removed
to focus on CLI functionality first, then reimplemented).

**Planned features:**

- Game selector view
- Mod browser with search
- Installed mods view with enable/disable
- Profile management view
- Settings view
- Configurable keybindings (vim and standard modes)

**Status:** Reimplemented and shipped — `lmm tui` (read-only, service-backed)
released as v1.4.0 on 2026-07-13; current code lives in `internal/tui/`.
Search, mutations, and workflows are tracked by the Phase 4-6 sections of
docs/plans/2026-04-28-tui-implementation.md (see its status block and
CLI-parity gap tables); roadmap additions and carry-forwards are in issue #37.
Of the planned features above: mod browser, installed-mods actions, and
profile management are Phases 4-6; the game selector is assigned to Phase 6;
the settings view and configurable keybindings remain deferred post-v1.
