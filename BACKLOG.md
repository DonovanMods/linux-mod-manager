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
