// ui.js - tiny UI-facing constants shared across components, kept out of
// modrows.js (pure library logic, no wording) and out of any one component
// (nothing here is specific to the top bar, the cards or the library - every
// mutation affordance in this unit carries it).

/** The tooltip a disabled mutation affordance carries.
 *
 * Unit 3 landed the confirm-plan framework every mutation submits through
 * (components/confirmplan.js) and wired the first control to it, Deploy.
 * What is still disabled is waiting on its own unit's work, not on the
 * framework: the per-mod actions and the enabled toggle (Unit 4), install
 * and the conflict round trip (Unit 5), profiles/reorder/health repair and
 * the update batch (Unit 6). Each of those registers a plan renderer and
 * wires a control - the pre-flight's "later units consume it, never fork
 * it" (docs/plans/2026-08-31-webui-impl.md §Pre-flight). */
export const NOT_YET = "Not available yet";
