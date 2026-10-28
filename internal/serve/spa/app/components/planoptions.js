// planoptions.js - the "Advanced" disclosure every confirm-plan renderer
// hangs its flag controls off (C-3, epic live review).
//
// The wire has carried these options since the units that landed each kind;
// nothing in the SPA ever set them, so `--show-archived`, `--keep-cache`,
// `--no-hooks`, `--force`, `deploy --method/--purge/<mod-id>` and the rest
// were CLI-only in practice while the design's Scope claims full
// bidirectional parity. They are all here now, in one shape, for one
// reason: an option control that looks different on every screen is an
// option control nobody trusts.
//
// A <details> rather than a row of checkboxes: none of these is the normal
// answer. The plan document above them IS the normal answer, and burying
// the overrides one click down keeps the confirm step readable while making
// them reachable - including by keyboard, which a summary/details pair is
// natively.
//
// THE TWO KINDS OF OPTION, and why they are separate components:
//
//   PLAN-time  changes what the plan SAYS. Setting one has to recompute the
//              plan (actions.replanWith), or the preview on screen would be
//              describing a mutation other than the one Confirm submits.
//   APPLY-time changes only what Apply does with a plan already computed.
//              Setting one is a local patch (actions.setPlanOptions).
//
// Which is which is the backend's decision, not this file's: each kind_*.go
// splits its own request into a plan half and an apply half, and a renderer
// picks the component that matches the half the field lives in.

import { html } from "../render.js";

/** PlanAdvanced is the disclosure itself. */
export function PlanAdvanced({ children }) {
  return html`
    <details class="plan__advanced" data-testid="plan-advanced">
      <summary class="plan__advanced-summary">Advanced</summary>
      <div class="plan__advanced-body">${children}</div>
    </details>
  `;
}

/** Option is the shared label/checkbox/hint shell both flavours render. */
function Option({ name, label, hint, checked, disabled, onChange }) {
  return html`
    <label class="plan__option">
      <input
        type="checkbox"
        name=${name}
        checked=${checked}
        disabled=${disabled}
        onChange=${(e) => onChange(e.currentTarget.checked)}
      />
      <span class="plan__option-label">
        ${label} ${hint && html`<span class="plan__option-hint">${hint}</span>`}
      </span>
    </label>
  `;
}

/**
 * ApplyOption writes one APPLY-time flag into the open modal's applyOptions.
 *
 * Fired from onChange, never from an effect - setPlanOptions calls
 * store.set(), which synchronously re-renders the whole application, and
 * Preact's render() is not safe to re-enter from inside its own effect
 * flush (plan_install.js's header comment records what that cost).
 */
export function ApplyOption({ modal, actions, name, label, hint }) {
  return html`<${Option}
    name=${name}
    label=${label}
    hint=${hint}
    checked=${Boolean(modal?.applyOptions?.[name])}
    disabled=${modal?.status !== "ready"}
    onChange=${(next) => actions.setPlanOptions({ [name]: next })}
  />`;
}

/**
 * PlanOption writes one PLAN-time flag and RE-PLANS.
 *
 * alsoApply names the apply-time field that has to move with it, for the
 * options that live in both halves (uninstall/purge's keep_cache,
 * skip_hooks and uninstall): the plan half is what makes the preview tell
 * the truth, the apply half is what the flow actually reads, and a control
 * that set only one of them would show one thing and do another.
 */
export function PlanOption({ modal, actions, name, label, hint, alsoApply }) {
  return html`<${Option}
    name=${name}
    label=${label}
    hint=${hint}
    checked=${Boolean(modal?.options?.[name])}
    disabled=${modal?.status !== "ready"}
    onChange=${(next) =>
      actions.replanWith({ [name]: next }, alsoApply ? { [name]: next } : null)}
  />`;
}

/**
 * PlanSelect is PlanOption's non-boolean sibling: a plan-time choice from a
 * fixed list (deploy's link method, deploy's single-mod target). An empty
 * value means "leave it to the flow's own default", which is why the
 * caller's first entry is always a blank one.
 */
export function PlanSelect({
  modal,
  actions,
  name,
  label,
  hint,
  value,
  options,
}) {
  return html`
    <label class="plan__option plan__option--select">
      <span class="plan__option-label">
        ${label} ${hint && html`<span class="plan__option-hint">${hint}</span>`}
      </span>
      <select
        name=${name}
        value=${value ?? ""}
        disabled=${modal?.status !== "ready"}
        onChange=${(e) => actions.replanWith(options.patch(e.currentTarget.value))}
      >
        ${options.entries.map(
          (o) =>
            html`<option key=${o.value} value=${o.value}>${o.label}</option>`,
        )}
      </select>
    </label>
  `;
}
