// plan_profile_sync.js - the confirm modal's renderer for a
// core.ProfileSyncPlan (kind_profile_sync.go, `lmm profile sync`), C-3 of
// the epic live review: profile sync had no plan kind, no route and no
// control at all.
//
// Sync is the mirror image of `profile apply`. Apply converges the INSTALL
// SET onto what the profile lists; sync converges the PROFILE onto what is
// actually installed. So this preview is three buckets in the CLI's own
// words - add, remove, update file IDs - and nothing else: the plan already
// answers exactly which refs move, and a renderer's job is to put that on
// screen in the order a user decides by.
//
// plan.names maps "source:id" to a display name for the add/update buckets
// only; a missing key means render the bare "source:id", which is what
// `lmm profile sync` itself does.

import { html } from "../render.js";

/** refKey is the plan's own map key for a ModReference. */
function refKey(ref) {
  return `${ref.source_id}:${ref.mod_id}`;
}

/** RefList renders one bucket, or nothing when the bucket is empty. */
function RefList({ heading, refs, names, testID }) {
  if (!refs || refs.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${refs.length})</h3>
      <ul class="plan__mods" data-testid=${testID}>
        ${refs.map((ref) => {
          const key = refKey(ref);
          return html`
            <li key=${key} class="plan__mod">
              <span class="plan__mod-name">${names?.[key] ?? key}</span>${" "}
              ${
                // htm-ws-ok: the ${" "} above already carries the gap.
                ref.version && html`<span class="mono">${ref.version}</span>`
              }
            </li>
          `;
        })}
      </ul>
    </section>
  `;
}

/** ProfileSyncPlanView renders core.ProfileSyncPlan. */
export function ProfileSyncPlanView({ plan }) {
  const names = plan.names ?? {};

  return html`
    <div class="plan plan--profile-sync">
      <p class="plan__summary">
        Bringing profile <span class="mono">${plan.profile}</span> into line
        with what is actually installed.
      </p>

      ${
        plan.missing &&
        html`<p class="plan__note">
          This profile has no file on disk yet - syncing creates it.
        </p>`
      }
      ${
        plan.no_changes &&
        html`<p class="plan__note" data-testid="sync-no-changes">
          Nothing to do: the profile already matches the installed set.
        </p>`
      }

      <${RefList}
        heading="Add to the profile"
        refs=${plan.to_add}
        names=${names}
        testID="sync-to-add"
      />
      <${RefList}
        heading="Remove from the profile"
        refs=${plan.to_remove}
        names=${names}
        testID="sync-to-remove"
      />
      <${RefList}
        heading="Update file IDs"
        refs=${plan.to_update}
        names=${names}
        testID="sync-to-update"
      />
    </div>
  `;
}
