// plan_mod_relink.js - the confirm modal's renderer for a core.RelinkPlan
// (kind_mod_relink.go, `lmm mod edit`), C-3 of the epic live review: mod
// edit had no plan kind, no route and no control at all.
//
// This renderer IS the form. Re-linking needs the user to say WHERE to
// re-link to before there is anything to preview, and those fields
// (new_source_id, new_mod_id) are PLAN-time - so rather than inventing a
// second dialog in front of the confirm modal, the modal opens on the
// metadata-only plan (a legal, meaningful edit in its own right: leave both
// blank and it just re-reads the mod's metadata) and each change re-plans
// through actions.replanWith. What the user sees is always a preview of
// exactly what Confirm will submit, which is the whole point of the
// Plan/Apply split.
//
// plan.refusal is the one that has to be loud: a LOCKED ref cannot be
// re-linked at all, and the plan says so before the user commits to a job
// that would only fail. target_installed is informational - core does not
// refuse on it - but it is the case where a re-link would collide with a
// mod already installed under the new identity, so it is a warning.

import { html, useState } from "../render.js";
import { PlanAdvanced } from "./planoptions.js";

/** RelinkPlanView renders core.RelinkPlan (internal/core/mod_edit.go). */
export function RelinkPlanView({ plan, modal, actions, state }) {
  const [newSource, setNewSource] = useState(
    modal?.options?.new_source_id ?? "",
  );
  const [newModID, setNewModID] = useState(modal?.options?.new_mod_id ?? "");
  const busy = modal?.status !== "ready";

  // The sources this GAME maps, which is the only set a re-link can
  // legally target: ApplyRelinkMod fails with `source "x" is not configured
  // for <game>` otherwise. domain.Game.source_ids rides along on the scoped
  // status document the top bar already reads.
  const sourceIDs = Object.keys(state?.status?.source_ids ?? {}).sort();

  function replan(patch) {
    actions.replanWith({
      new_source_id: patch.new_source_id ?? newSource,
      new_mod_id: patch.new_mod_id ?? newModID,
    });
  }

  return html`
    <div class="plan plan--mod-relink">
      <p class="plan__summary">
        <span class="mono">${plan.mod.name}</span> in profile${" "}
        <span class="mono">${plan.profile}</span>.
      </p>

      ${
        plan.refusal &&
        html`<p
          class="plan__note plan__note--warn"
          data-testid="relink-refusal"
        >
          ${plan.refusal}
        </p>`
      }
      ${
        plan.locked &&
        !plan.refusal &&
        html`<p class="plan__note plan__note--warn">
          Locked${plan.locked_version ? ` at ${plan.locked_version}` : ""}.
        </p>`
      }
      ${
        plan.target_installed &&
        html`<p class="plan__note plan__note--warn">
          A mod is already installed under that identity - re-linking will
          collide with it.
        </p>`
      }
      ${
        plan.merged_pak_affected &&
        html`<p class="plan__note">
          This game's merged artifact will need re-syncing afterwards.
        </p>`
      }

      <section class="plan__section">
        <h3 class="plan__heading">${plan.relink ? "Re-link" : "No re-link"}</h3>
        <p class="mono" data-testid="relink-from-to">
          ${plan.from.source_id}:${plan.from.mod_id} →${" "}
          ${plan.to.source_id}:${plan.to.mod_id}
        </p>
        ${
          !plan.relink &&
          html`<p class="plan__note">
            Nothing to re-link yet - this is a metadata-only edit until you name
            a different source or mod id below.
          </p>`
        }
      </section>

      <div class="plan__form">
        <label class="plan__control">
          New source
          <select
            name="relink-source"
            value=${newSource}
            disabled=${busy}
            onChange=${(e) => {
              setNewSource(e.currentTarget.value);
              replan({ new_source_id: e.currentTarget.value });
            }}
          >
            <option value="">Leave as ${plan.from.source_id}</option>
            ${sourceIDs.map(
              (id) => html`<option key=${id} value=${id}>${id}</option>`,
            )}
          </select>
        </label>
        <label class="plan__control">
          New mod id
          <input
            type="text"
            name="relink-mod-id"
            autocomplete="off"
            placeholder=${plan.from.mod_id}
            value=${newModID}
            disabled=${busy}
            onInput=${(e) => setNewModID(e.currentTarget.value)}
            onChange=${(e) => replan({ new_mod_id: e.currentTarget.value })}
          />
        </label>
      </div>

      <${PlanAdvanced}>
        <${MetadataOverride}
          modal=${modal}
          actions=${actions}
          name="name"
          label="Name"
        />
        <${MetadataOverride}
          modal=${modal}
          actions=${actions}
          name="version"
          label="Version"
        />
        <${MetadataOverride}
          modal=${modal}
          actions=${actions}
          name="author"
          label="Author"
        />
      <//>
    </div>
  `;
}

/**
 * MetadataOverride is one of `lmm mod edit`'s three apply-time text
 * overrides (core.RelinkOptions). Each is applied only when non-empty; the
 * rest are filled from the target source's own metadata on a re-link, which
 * is why an untouched field submits nothing at all rather than an empty
 * string.
 */
function MetadataOverride({ modal, actions, name, label }) {
  return html`
    <label class="plan__control">
      ${label}
      <input
        type="text"
        name=${`relink-${name}`}
        autocomplete="off"
        disabled=${modal?.status !== "ready"}
        value=${modal?.applyOptions?.[name] ?? ""}
        onInput=${(e) => actions.setPlanOptions({ [name]: e.currentTarget.value })}
      />
    </label>
  `;
}
