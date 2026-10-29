// plan_mod_copy.js - the confirm modal's renderer for a core.CopyModPlan
// (kind_mod_copy.go, `lmm mod copy`, issue 437): one mod of the selected
// profile put into another profile of the same game, from the cache, with
// no source asked anything.
//
// Like plan_mod_relink.js, this renderer IS the form. The destination is a
// PLAN-time choice (it is what the plan describes: whether the destination
// already lists the mod, whether it is the active profile), so the modal
// opens on a plan for the likeliest destination (copyDestinationFor) and
// the select re-plans through actions.replanWith. What is on screen is
// always a preview of exactly what Confirm submits.
//
// The already-listed case is answered by the Confirm click itself, the way
// an update from a mismatched file is (issue 530): a single-mod copy into a
// profile that lists the mod would be refused by Apply without replace, so
// the plan says so, Confirm reads "Replace" (confirmplan.js) and
// main.js#confirmPlan sends replace for it (copyReplaces, below). An
// all-copy keeps the CLI's choice - skip what is listed, or replace it -
// as an apply-time option.

import { html } from "../render.js";
import { displayVersion } from "../version.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";

/**
 * copyReplaces reports whether confirming plan IS the answer "replace":
 * a single-mod copy whose destination already lists the mod. Shared by
 * confirmplan.js (the "Replace" label) and main.js#confirmPlan (the
 * replace option it sends), so the label and the request cannot disagree.
 */
export function copyReplaces(plan) {
  return Boolean(plan && !plan.all && plan.mods?.[0]?.already_listed);
}

/**
 * copyTargets is every profile of the game a mod can be copied into from
 * profile `from`: the game's OTHER profiles, in the status document's own
 * order (core.GameStatus.profiles).
 */
export function copyTargets(status, from) {
  return (status?.profiles ?? []).filter((p) => p.name !== from);
}

/**
 * copyDestinationFor picks the destination the modal opens on: the active
 * profile when the copy is out of another one (the profile a user plays is
 * the likeliest place for a mod they found elsewhere), else the first
 * other profile. Undefined when there is nowhere to copy to.
 */
export function copyDestinationFor(status, from) {
  const targets = copyTargets(status, from);
  return (targets.find((p) => p.is_default) ?? targets[0])?.name;
}

/**
 * openCopyToProfile opens the confirm-plan modal for "Copy to profile…" on
 * mod ({source_id, id, name}) out of profile `from`. Callers offer the
 * control only when copyTargets is non-empty.
 */
export function openCopyToProfile({ actions, status, from, mod, origin }) {
  const to = copyDestinationFor(status, from);
  if (!to) return;
  actions.openPlan({
    kind: "mod_copy",
    origin,
    title: `Copy ${mod.name ?? mod.id} to another profile`,
    confirmLabel: "Copy",
    options: { to, source_id: mod.source_id, mod_id: mod.id },
  });
}

/** CopyModPlanView renders core.CopyModPlan (internal/core/mod_copy.go). */
export function CopyModPlanView({ plan, modal, actions, state }) {
  const mods = plan.mods ?? [];
  const skipped = plan.skipped ?? [];
  const targets = copyTargets(state?.status, plan.from);
  const busy = modal?.status !== "ready";
  const listedAny = mods.some((m) => m.already_listed);

  return html`
    <div class="plan plan--mod-copy">
      <p class="plan__summary">
        ${
          plan.all
            ? html`Copying every mod profile${" "}
                <span class="mono">${plan.from}</span> lists into${" "}
                <span class="mono">${plan.to}</span>.`
            : html`Copying${" "}
                <span class="mono">${mods[0]?.name ?? ""}</span>${" "} from
                profile${" "} <span class="mono">${plan.from}</span>${" "}
                into${" "} <span class="mono">${plan.to}</span>.`
        }
      </p>

      <div class="plan__form">
        <label class="plan__control">
          Copy to
          <select
            name="copy-to"
            value=${plan.to}
            disabled=${busy}
            onChange=${(e) => actions.replanWith({ to: e.currentTarget.value })}
          >
            ${targets.map(
              (p) =>
                html`<option key=${p.name} value=${p.name}>
                  ${p.is_default ? `${p.name} (active)` : p.name}
                </option>`,
            )}
          </select>
        </label>
      </div>

      ${
        plan.destination_active
          ? html`<p class="plan__note" data-testid="copy-destination">
              <span class="mono">${plan.to}</span> is the active profile: the
              copy is deployed into the game directory now, by${" "}
              <span class="mono">${plan.link_method}</span>.
            </p>`
          : html`<p class="plan__note" data-testid="copy-destination">
              <span class="mono">${plan.to}</span> is not the active profile:
              the copy is recorded only, and its next deploy or switch puts the
              files in place.
            </p>`
      }
      ${
        plan.link_method_differs &&
        html`<p
          class="plan__note plan__note--warn"
          data-testid="copy-link-method"
        >
          <span class="mono">${plan.to}</span> deploys by${" "}
          <span class="mono">${plan.link_method}</span>, not${" "}
          <span class="mono">${plan.source_link_method}</span> as${" "}
          <span class="mono">${plan.from}</span> does. The copy follows${" "}
          <span class="mono">${plan.to}</span>.
        </p>`
      }
      ${
        plan.no_changes &&
        html`<p class="plan__note" data-testid="copy-no-changes">
          Nothing to copy.
        </p>`
      }
      ${
        mods.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">Copied (${mods.length})</h3>
          <ul class="plan__mods" data-testid="copy-mods">
            ${mods.map(
              (m) =>
                html`<${CopyEntry}
                  key=${`${m.source_id}:${m.mod_id}`}
                  entry=${m}
                  plan=${plan}
                />`,
            )}
          </ul>
        </section>`
      }
      ${
        skipped.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">Not copied (${skipped.length})</h3>
          <ul class="plan__mods" data-testid="copy-skipped">
            ${skipped.map(
              (s) =>
                html`<li
                  key=${`${s.source_id}:${s.mod_id}`}
                  class="plan__mod plan__mod--skipped"
                >
                  <span class="plan__mod-name"
                    >${s.name || `${s.source_id}:${s.mod_id}`}</span
                  >${" "}
                  <span class="plan__mod-detail">${s.reason}</span>
                </li>`,
            )}
          </ul>
        </section>`
      }
      ${
        plan.all &&
        listedAny &&
        html`<${PlanAdvanced}>
          <${ApplyOption}
            modal=${modal}
            actions=${actions}
            name="replace"
            label="Replace what the destination already lists"
            hint="Without it, those mods are skipped."
          />
        <//>`
      }
    </div>
  `;
}

/** alreadyListedText says what the destination has for entry, and what
 * the copy does about it. */
function alreadyListedText(entry, plan) {
  const at = entry.existing_version ? ` at ${entry.existing_version}` : "";
  const then = plan.all
    ? "skipped unless you replace it"
    : "Replace overwrites it";
  return `${plan.to} already lists it${at} - ${then}.`;
}

/** CopyEntry is one mod the copy adds: its pinned version, and what it
 * carries with it - a lock, an off marker, a collision with the
 * destination's own entry. */
function CopyEntry({ entry, plan }) {
  return html`
    <li
      class=${entry.already_listed ? "plan__mod plan__mod--warn" : "plan__mod"}
      data-mod=${`${entry.source_id}:${entry.mod_id}`}
    >
      <span class="plan__mod-name">${entry.name}</span>${" "}
      <span class="plan__mod-version mono">${displayVersion(entry)}</span>${" "}
      ${
        // htm-ws-ok: the ${" "} above already carries the gap.
        entry.locked &&
        html`<span class="plan__mod-detail" data-testid="copy-locked">
          Locked - the copy is locked at this version too.
        </span>`
      }
      ${
        entry.disabled &&
        html`<span class="plan__mod-detail" data-testid="copy-disabled">
          Switched off - the copy is recorded off and not deployed.
        </span>`
      }
      ${
        entry.already_listed &&
        html`<span
          class="plan__mod-detail plan__mod-detail--warn"
          data-testid="copy-already-listed"
        >
          ${alreadyListedText(entry, plan)}
        </span>`
      }
    </li>
  `;
}
