// plan_snapshot_restore.js - the confirm modal's renderer for a
// core.SnapshotRestorePlan (kind_snapshot_restore.go, `lmm snapshot
// restore`), issue 350.
//
// It leads with what CANNOT be restored, before anything else. That is not
// a layout preference: the refusals are the only part of this plan a user
// might stop for, and a preview that buried them under the counts would let
// someone confirm a restore believing it would bring back a mod whose
// source can no longer serve the recorded version.
//
// Like purge, this is a destructive multi-stage mutation - it undeploys
// everything currently in the game directory. It is NOT gated behind
// typing a name, though, and the reason is worth writing down: a restore
// takes a snapshot of the CURRENT state first (by default), so unlike a
// purge it is itself reversible. The gate belongs on the irreversible
// click, not on every frightening one.

import { html } from "../render.js";
import { PlanAdvanced, PlanOption, ApplyOption } from "./planoptions.js";
import { relativeTime } from "../relativetime.js";
import { displayVersion } from "../version.js";

/** SnapshotRestorePlanView renders core.SnapshotRestorePlan
 * (internal/core/snapshot_restore.go). */
export function SnapshotRestorePlanView({ plan, modal, actions }) {
  const toPurge = plan.to_purge ?? [];
  const toPurgeActive = plan.to_purge_active ?? [];
  const originals = plan.originals ?? [];
  const mods = plan.mods ?? [];
  const refusals = plan.refusals ?? [];

  const external = plan.external ?? [];
  const restorable = originals.filter((o) => o.status === "restorable");
  const unavailable = originals.filter((o) => o.status !== "restorable");
  const listed = mods.filter((m) => !m.error);
  // issue 269: an external row is LISTED but not restored - Steam owns its
  // files, so the count that says what the restore will bring back must not
  // include it, while the list below still accounts for the whole profile.
  const restoring = listed.filter((m) => !m.external);

  // ONE string, not adjacent interpolations (htm collapses the whitespace
  // between them).
  const taken = relativeTime(plan.created_at);
  const heading = taken
    ? `Snapshot ${plan.snapshot}, taken ${taken}`
    : `Snapshot ${plan.snapshot}`;

  return html`
    <div class="plan plan--snapshot-restore">
      <p class="plan__summary" data-testid="restore-heading">${heading}</p>

      ${
        refusals.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">
              ${`${refusals.length} mod${refusals.length === 1 ? "" : "s"} cannot be restored`}
            </h3>
            <ul class="plan__mods" data-testid="restore-refusals">
              ${refusals.map(
                (r) => html`
                  <li key=${`${r.source_id}/${r.mod_id}`} class="plan__mod">
                    <span class="plan__mod-name"
                      >${r.name || `${r.source_id}:${r.mod_id}`}</span
                    >
                    <span class="plan__mod-detail">${r.reason}</span>
                  </li>
                `,
              )}
            </ul>
          </section>
        `
      }

      <p class="plan__note plan__note--warn">
        ${`This undeploys ${toPurge.length + toPurgeActive.length} mod${toPurge.length + toPurgeActive.length === 1 ? "" : "s"}, puts back ${restorable.length} stored original${restorable.length === 1 ? "" : "s"}, and restores ${restoring.length} mod${restoring.length === 1 ? "" : "s"} at their recorded versions.`}
      </p>

      ${
        // issue 269: named rather than counted above, because lmm undeploys
        // none of them - core.PurgePlan's "Left alone" section, same rule.
        external.length > 0 &&
        html`
          <p class="plan__note" data-testid="restore-external">
            ${`${external.length} Steam Workshop item${external.length === 1 ? "" : "s"} stay${external.length === 1 ? "s" : ""} exactly as Steam has ${external.length === 1 ? "it" : "them"}: ${external.join(", ")}.`}
          </p>
        `
      }
      ${
        // The listing is game-scoped, so the snapshot on screen may have
        // been taken under another profile. A restore puts back which
        // profile was active too (review finding 2), and that is the part
        // of the plan the word "restore" does not lead a reader to expect.
        plan.active_profile &&
        html`
          <p class="plan__note plan__note--warn" data-testid="restore-switch">
            ${`This also switches the active profile from ${plan.active_profile} back to ${plan.profile}, undeploying its ${toPurgeActive.length} mod${toPurgeActive.length === 1 ? "" : "s"}.`}
          </p>
        `
      }
      ${
        unavailable.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">
              ${`${unavailable.length} original${unavailable.length === 1 ? "" : "s"} cannot be put back`}
            </h3>
            <ul class="plan__mods" data-testid="restore-unavailable-originals">
              ${unavailable.map(
                (o) => html`
                  <li key=${`${o.root}/${o.relative_path}`} class="plan__mod">
                    <span class="mono">${o.relative_path}</span>${" "}
                    <span class="plan__mod-detail">${o.reason}</span>
                  </li>
                `,
              )}
            </ul>
          </section>
        `
      }
      ${
        listed.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">${`Mods (${listed.length})`}</h3>
            <ul class="plan__mods">
              ${listed.map(
                (m) => html`
                  <li key=${`${m.source_id}/${m.mod_id}`} class="plan__mod">
                    <span class="plan__mod-name"
                      >${m.name || `${m.source_id}:${m.mod_id}`}</span
                    >
                    <span class="mono">${displayVersion(m)}</span>${" "}
                    ${
                      m.external
                        ? html`<span class="plan__mod-detail"
                            >${
                              m.external_missing
                                ? "Steam Workshop item - Steam no longer has it on disk"
                                : "Steam Workshop item - left as Steam has it"
                            }</span
                          >`
                        : !m.cached &&
                          html`<span class="plan__mod-detail"
                            >will be downloaded again</span
                          >`
                    }
                  </li>
                `,
              )}
            </ul>
          </section>
        `
      }
      ${
        plan.profile_changed &&
        html`<p class="plan__note">
          ${`The profile ${plan.profile} will be rewritten from the snapshot - its load order and locks come back with it.`}
        </p>`
      }

      <${PlanAdvanced}>
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="no_safety_snapshot"
          label="Don't snapshot the current state first"
          hint="lmm snapshot restore --no-safety-snapshot. By default a restore records where you are now, so it is itself reversible."
        />
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="skip_hooks"
          alsoApply
          label="Skip hooks"
          hint="lmm --no-hooks."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="force"
          label="Force"
          hint="lmm snapshot restore --force. Continue even if hooks fail."
        />
      <//>
    </div>
  `;
}
