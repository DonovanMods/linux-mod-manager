// cards.js - the attention cards: Updates, Health, Conflicts
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Attention
// cards"). Each renders only when it has something to say - a card absent
// entirely is itself the "nothing needs you here" signal. Unit 3 landed the
// confirm-modal framework every mutation below submits through; this unit
// (issue 332) wires the per-row and batch actions themselves.

import { html, useState } from "../render.js";
import { findingLabel } from "../verify.js";
import { InlineJob } from "./jobprogress.js";
import { modKey } from "../modrows.js";

// UPDATES_BATCH_ORIGIN is the Updates card's own "Update selected" control -
// distinct from a single-mod update's own "mod:{source}/{id}:update"
// (modpanel.js/fullmodpage.js), which this card's own checkboxes never use.
const UPDATES_BATCH_ORIGIN = "updates:batch";

// HEALTH_REPAIR_ALL_ORIGIN is the Health card's "Repair all" control.
const HEALTH_REPAIR_ALL_ORIGIN = "health:repair-all";

/** notFixableReason names why a finding's own Repair is absent.
 *
 * Since issue 334 this is the ENGINE's own sentence, read straight off the
 * finding (core.VerifyFinding.FixableReason), which replaced the
 * hand-maintained status->sentence table and the locked/unlocked guess that
 * used to live here. That guess read the lock off a separate library fetch
 * and could not see the local-source case at all, so a version_mismatch on
 * a locally imported mod got the generic sentence; the engine knows which
 * of its own decision points refused the repair and says so.
 *
 * The generic fallback survives for a row the engine had nothing to add
 * about - and for a finding decoded from an older document that carries no
 * such key. */
function notFixableReason(f) {
  return (
    f.fixable_reason ||
    "A verify --fix run would not attempt a repair for this finding"
  );
}

/** repairOrigin is one finding's own per-mod "Repair" control - shared by
 * every finding ROW for the same mod (a mod can carry more than one
 * finding), which is deliberate: they name the same repair operation, so
 * clicking any of them and having every one of that mod's rows morph
 * together is the honest picture of what a mod_filter repair actually
 * does. */
function repairOrigin(modID) {
  return `health:${modID}:repair`;
}

/** AttentionCards reads the three already-fetched report documents (core.
 * UpdateCheckReport, core.VerifyReport, core.ConflictReport) and renders
 * nothing at all - not even the section - when none has anything to show
 * AND none of the three fetches failed. A failed fetch is never swallowed
 * into that same "nothing needs you here" silence (design doc §Search:
 * "Source failures surface as a warning row, never swallowed") - its card
 * renders with an explicit error and a retry, even though the document
 * behind it is null. */
export function AttentionCards({
  state,
  updates,
  health,
  conflicts,
  errors = {},
  actions,
}) {
  const updateRows = updates?.updates ?? [];
  const findings = (health?.result?.findings ?? []).filter(
    (f) => f.status !== "ok",
  );
  const conflictRows = conflicts?.conflicts ?? [];
  const hasError = Boolean(errors.updates || errors.health || errors.conflicts);

  if (
    updateRows.length === 0 &&
    findings.length === 0 &&
    conflictRows.length === 0 &&
    !hasError
  ) {
    return null;
  }

  return html`
    <section class="attention-cards">
      ${
        (updateRows.length > 0 || errors.updates) &&
        html`<${UpdatesCard}
          state=${state}
          rows=${updateRows}
          error=${errors.updates}
          onRetry=${actions.reloadUpdates}
          actions=${actions}
        />`
      }
      ${
        (findings.length > 0 || errors.health) &&
        html`<${HealthCard}
          state=${state}
          findings=${findings}
          result=${health?.result}
          error=${errors.health}
          onReverify=${actions.reloadHealth}
          actions=${actions}
        />`
      }
      ${
        (conflictRows.length > 0 || errors.conflicts) &&
        html`<${ConflictsCard}
          state=${state}
          rows=${conflictRows}
          error=${errors.conflicts}
          onRetry=${actions.reloadConflicts}
          actions=${actions}
        />`
      }
    </section>
  `;
}

function UpdatesCard({ state, rows, error, onRetry, actions }) {
  const [selected, setSelected] = useState(() => new Set());

  function toggle(key) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function updateSelected() {
    if (selected.size === 0) return;
    actions.openPlan({
      kind: "updates",
      origin: UPDATES_BATCH_ORIGIN,
      title: `Update ${selected.size} mod${selected.size === 1 ? "" : "s"}`,
      confirmLabel: "Update",
      options: { mods: [...selected] },
    });
  }

  return html`
    <div class="card card--updates">
      <p class="card__title">⬆ Updates (${rows.length})</p>
      ${
        error
          ? html`<${CardError}
              message="Couldn't check for updates"
              detail=${error}
              onRetry=${onRetry}
            />`
          : html`
              <ul class="card__list">
                ${rows.map((u) => {
                  const key = modKey(u.installed_mod);
                  return html`
                    <li key=${key} class="card__row">
                      <input
                        type="checkbox"
                        aria-label=${`Select ${u.installed_mod.name} for update`}
                        checked=${selected.has(key)}
                        onChange=${() => toggle(key)}
                      />
                      <span class="card__row-name"
                        >${u.installed_mod.name}</span
                      >
                      <span class="mono card__row-detail"
                        >${u.installed_mod.version} → ${u.new_version}</span
                      >
                    </li>
                  `;
                })}
              </ul>
              <${InlineJob}
                origin=${UPDATES_BATCH_ORIGIN}
                state=${state}
                actions=${actions}
              >
                <button
                  type="button"
                  class="button"
                  data-action="update-selected"
                  disabled=${selected.size === 0}
                  onClick=${updateSelected}
                >
                  Update selected
                </button>
              <//>
            `
      }
    </div>
  `;
}

// HealthCard carries the design doc's "re-run" (onReverify, a plain
// re-fetch of /api/v1/health - it also doubles as the I3 CardError retry
// above). Its sibling "last-verify timestamp" is NOT implemented:
// core.VerifyResult carries no timestamp field, and adding one is a wire
// change (core/testdata JSON goldens, the serve JSON-contract ratchet) this
// unit's gate explicitly keeps frozen - filed as a follow-up core change
// rather than silently dropped.
function HealthCard({ state, findings, result, error, onReverify, actions }) {
  function repair(modID, name) {
    actions.openPlan({
      kind: "verify_fix",
      origin: repairOrigin(modID),
      title: `Repair ${name || modID}`,
      confirmLabel: "Repair",
      options: { mod_filter: modID },
    });
  }

  function repairAll() {
    actions.openPlan({
      kind: "verify_fix",
      origin: HEALTH_REPAIR_ALL_ORIGIN,
      title: "Repair all findings",
      confirmLabel: "Repair all",
      options: {},
    });
  }

  return html`
    <div class="card card--health">
      <p class="card__title">
        ⚠ Health${result ? ` (${result.issues + result.warnings})` : ""}
      </p>
      ${
        error
          ? html`<${CardError}
              message="Couldn't check health"
              detail=${error}
              onRetry=${onReverify}
            />`
          : html`
              <ul class="card__list">
                ${findings.map(
                  (f, i) => html`
                    <li
                      key=${f.mod_id + "/" + (f.file_id || i)}
                      class="card__row"
                    >
                      <span class="card__row-name"
                        >${f.mod_name || f.mod_id}</span
                      >
                      <span class="card__row-detail">${findingLabel(f)}</span>
                      ${
                        f.fixable
                          ? html`<${InlineJob}
                              origin=${repairOrigin(f.mod_id)}
                              state=${state}
                              actions=${actions}
                            >
                              <button
                                type="button"
                                class="button button--small"
                                data-action="repair"
                                onClick=${() => repair(f.mod_id, f.mod_name)}
                              >
                                Repair
                              </button>
                            <//>`
                          : html`<span
                              class="card__row-detail"
                              title=${notFixableReason(f)}
                              >Not fixable: ${notFixableReason(f)}</span
                            >`
                      }
                    </li>
                  `,
                )}
              </ul>
              <div class="card__actions">
                <button
                  type="button"
                  class="button button--small"
                  onClick=${onReverify}
                >
                  Re-verify
                </button>
                <${InlineJob}
                  origin=${HEALTH_REPAIR_ALL_ORIGIN}
                  state=${state}
                  actions=${actions}
                >
                  <button
                    type="button"
                    class="button"
                    data-action="repair-all"
                    disabled=${!findings.some((f) => f.fixable)}
                    onClick=${repairAll}
                  >
                    Repair all
                  </button>
                <//>
              </div>
            `
      }
    </div>
  `;
}

/** The per-card explicit error state: what failed, and a retry that
 * re-fetches just this card's document (main.js's reload actions) - never
 * the whole-page hydrate, so a retry after a slow health check doesn't cost
 * the mods/updates/conflicts reads that already succeeded. */
function CardError({ message, detail, onRetry }) {
  return html`
    <div>
      <p class="card__error">${message}: ${detail}</p>
      <button type="button" class="button button--small" onClick=${onRetry}>
        Retry
      </button>
    </div>
  `;
}

/** conflictLabel names the contenders AND the winning rule (design doc:
 * "each conflict names the contenders and the winning rule") - built as one
 * plain string rather than split across template-literal lines, which
 * htm's JSX-style whitespace collapsing would otherwise eat between two
 * adjacent interpolations (a real trap: a `trunk fmt` reflow silently
 * dropped the space that used to separate "wins:" from the name here). */
function conflictLabel(c) {
  const also = c.also_in.map((m) => m.name).join(", ");
  const label = `${c.owner.name} ↔ ${also} · wins: ${c.load_order_winner.name}`;
  return c.stale ? `${label} (stale)` : label;
}

function ConflictsCard({ state, rows, error, onRetry, actions }) {
  // Demo item 9 (unit 6 gate review): "Resolve…" used to always land the
  // reorder modal at the top of the list, same as the library's own plain
  // "Reorder…" - on a long profile the owner had to hunt for the two mods
  // they just clicked about. c.owner is the file's CURRENT deployed
  // provider (this row's own label lists it first, "owner ↔ also_in"),
  // which is what "Reorder here" already scrolls to for a single mod's own
  // row menu (library.js) - the same focusKey here.
  function resolve(c) {
    actions.openReorderModal({
      profileName: state.route.profile,
      focusKey: c.owner.key,
    });
  }

  return html`
    <div class="card card--conflicts">
      <p class="card__title">⇄ Conflicts (${rows.length})</p>
      ${
        error
          ? html`<${CardError}
              message="Couldn't check for conflicts"
              detail=${error}
              onRetry=${onRetry}
            />`
          : html`
              <ul class="card__list">
                ${rows.map(
                  (c) => html`
                    <li key=${c.path} class="card__row">
                      <span
                        class="card__row-name"
                        title=${c.stale ? "A redeploy would change which file wins" : undefined}
                        >${conflictLabel(c)}</span
                      >
                      <span class="mono card__row-detail">${c.path}</span>
                      <button
                        type="button"
                        class="button button--small"
                        data-action="resolve"
                        onClick=${() => resolve(c)}
                      >
                        Resolve…
                      </button>
                    </li>
                  `,
                )}
              </ul>
            `
      }
    </div>
  `;
}
