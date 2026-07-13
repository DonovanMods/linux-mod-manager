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

Interactive terminal interface using Bubble Tea framework. Removed to focus on CLI functionality first.

**Planned features:**

- Game selector view
- Mod browser with search
- Installed mods view with enable/disable
- Profile management view
- Settings view
- Configurable keybindings (vim and standard modes)

**Status:** Reimplemented. Prototype shell (v1.3.x, #31/#32) and read-only
service-backed TUI (v1.4.0) are done; search, mutations, and workflows are
tracked by the Phase 4-6 sections of docs/plans/2026-04-28-tui-implementation.md.

**Original implementation:** See git history before commit that removed TUI.
