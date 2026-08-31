// ui.js - tiny UI-facing constants shared across components, kept out of
// modrows.js (pure library logic, no wording) and out of any one component
// (nothing here is specific to the top bar, the cards or the library - every
// mutation affordance in this unit carries it).

/** The tooltip every disabled mutation affordance carries in this unit: the
 * read surface renders every control the design calls for, but Unit 3 lands
 * the confirm-modal framework they submit through (docs/plans/2026-08-31-
 * webui-impl.md §Pre-flight: "later units consume [it], never fork it"). */
export const NOT_YET = "Not available yet";
