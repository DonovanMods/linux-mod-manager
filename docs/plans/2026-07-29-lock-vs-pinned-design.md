# Design Note: Lock vs. Pinned Policy (EPIC #98, pre-#97)

**Status**: Approved design note. Settles the epic's open "lock vs. `pinned` policy" question ahead of #96/#97 implementation.
**Issues**: #98 (epic), #96 (version→file resolution), #97 (lock/unlock surface), #93 (stays open until #96's resolver replaces the interim guard).
**Decisions made with the user (2026-07-29)**: locks and update policy remain orthogonal concepts with the lock winning at every conflict point; locking is policy-neutral (never touches `UpdatePolicy`); "locked but informed" is the default experience; "locked and silent" is expressed by composing a lock with the existing `pinned` policy, not by a new directive; `pinned` is reframed in docs as a check-mute, not a version freeze.

## The distinction

The two concepts act at different stages, different scopes, and different persistence layers — they are not two flavors of one thing:

| | `pinned` (exists) | lock (this epic) |
| --- | --- | --- |
| Statement about | **conversation** — "stop asking the source about this mod" | **state** — "this profile deploys exactly version X" |
| Enforced at | check time only: `UpdateCheckable` (`internal/core/updater.go:97`) filters before any network call | deploy time: deploy paths converge the mod to the named version, in either direction (downgrades included) |
| Target version | none — "wherever this mod happens to be", which silently re-anchors after rollbacks/reinstalls | exact, named, does not drift |
| Scope | per-install (`installed_mods.update_policy`, SQLite) | per-profile (`ModReference.Version`, profile YAML) |
| Travels with `profile export`/`import` | no | yes — imports reproduce the exact build |
| Works on version-less sources (directory, version-less manifest, api without files endpoint) | yes | no — fails with a clear capability error |

Shorthand: **pinned mutes a mod's release notifications; a lock is a lockfile entry.** One controls what you hear about, the other controls what gets built.

## Decision: orthogonal, lock wins

Lock and `UpdatePolicy` stay independent knobs. They never merge, neither implies the other, and wherever they would conflict the lock wins and the UI names the lock.

Rejected alternatives, for the record:

- **Lock implies `pinned`** — scope mismatch: a lock is per-profile, policy is per-install, so locking a mod in a testing profile would silently stop update checks in the stable profile too. Also forfeits "locked but informed", the primary intended experience.
- **Collapse `pinned` into "locked to current"** — breaks the only available freeze on sources that cannot resolve versions, forces a migration of per-install pins into per-profile locks with no principled answer for which profiles receive them, and churns the pin-visibility work (#92 phase). Nothing in the orthogonal model forecloses revisiting this later if lock adoption makes `pinned` feel vestigial.

## Interaction matrix

| State | Check behavior | Deploy/apply behavior |
| --- | --- | --- |
| lock + `notify` (the default, "locked but informed") | update check runs; reports "newer available (locked at X)" | holds/converges to X |
| lock + `auto` | auto-apply skips the mod, counted as a new `Locked` category in `UpdateSkips` alongside `Pinned`/`Local` | holds/converges to X |
| lock + `pinned` ("locked and silent") | not checked; output says so, as pinned does today | holds/converges to X |
| `lmm update <locked-id>` (explicit single-mod apply) | — | refused: "locked at X; `lmm mod lock <id> <version>` to move the lock, or unlock first" |

The only way a locked mod changes version is an explicit re-lock to another named version (`lmm mod lock <id> <new-version>` or the TUI picker) or an unlock followed by the normal update flow.

## Consequences

- **Locking is policy-neutral.** `lmm mod lock` and the TUI lock action never modify `UpdatePolicy`. `unlock` removes the version from the profile ref, returning the mod to whatever its policy already says. No hidden state changes in either direction.
- **"Locked and silent" is composition, not mechanism.** A user who wants neither updates nor notifications locks the mod and pins it. No new YAML directive: the lock belongs in profile YAML because it defines the build (`profile export` hands someone the exact versions); whether *this user* wants update noise is a personal preference and stays in the DB with the rest of the policy. If the two-step proves clunky, a sugar flag (`lmm mod lock <id> --quiet` setting both) is a cheap additive follow-up — noted, not built (YAGNI).
- **`pinned` is reframed in docs, not retired.** Docs (README, command help) should say: `pinned` mutes update checks for a mod; to hold a version, lock it. Pinned remains the only freeze on version-less sources, which is a real but smaller job than its name currently implies.
- **Per-source capability.** Locks require version→file resolution (#96). Sources that cannot resolve versions reject `lock` with a clear "this source cannot resolve versions" error behind a capability flag, per the epic's existing lean — never a silent fallback.

## Notes carried into #96

- **`Update.NewVersion` recording (from #94's close-out).** The update-apply path deliberately still records `Update.NewVersion`. Under this design, locked mods are refused at the apply gate and never reach that path, so the resolver only reconciles `NewVersion` recording for unlocked mods. The #94 verify↔update interaction note is thereby scoped down, not eliminated — #96 should still revisit the recording site with the resolver in hand.
- **`verify` becomes lock-aware eventually.** `lmm verify`'s version-record check is already the deploy-state checker; it is policy-blind today and reads no lock. Once `ModReference.Version` is authoritative, verify should treat a locked mod's recorded-vs-locked mismatch as a finding. Scope this inside #96/#97 as fits; it must not silently "fix" a mod to latest.
- **Version matching**: exact string equality (the epic's lean stands). Anything smarter is per-source capability work, later.
- **Unavailable targets**: deploy from cache when the locked version is cached; hard error naming the version when it is gone upstream and uncached. Never degrade to latest (#95's rule applies to locks doubly).
