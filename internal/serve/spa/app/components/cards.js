// cards.js - the attention cards: Updates, Health, Conflicts
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Attention
// cards"). Each renders only when it has something to say - a card absent
// entirely is itself the "nothing needs you here" signal - and every
// per-row and batch action is present but disabled: Unit 3 wires the
// confirm-modal framework they submit through.

import { html } from "../render.js";
import { NOT_YET } from "../ui.js";

/** AttentionCards reads the three already-fetched report documents (core.
 * UpdateCheckReport, core.VerifyReport, core.ConflictReport) and renders
 * nothing at all - not even the section - when none has anything to show
 * AND none of the three fetches failed. A failed fetch is never swallowed
 * into that same "nothing needs you here" silence (design doc §Search:
 * "Source failures surface as a warning row, never swallowed") - its card
 * renders with an explicit error and a retry, even though the document
 * behind it is null. */
export function AttentionCards({
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
          rows=${updateRows}
          error=${errors.updates}
          onRetry=${actions.reloadUpdates}
        />`
      }
      ${
        (findings.length > 0 || errors.health) &&
        html`<${HealthCard}
          findings=${findings}
          result=${health?.result}
          error=${errors.health}
          onReverify=${actions.reloadHealth}
        />`
      }
      ${
        (conflictRows.length > 0 || errors.conflicts) &&
        html`<${ConflictsCard}
          rows=${conflictRows}
          error=${errors.conflicts}
          onRetry=${actions.reloadConflicts}
        />`
      }
    </section>
  `;
}

function UpdatesCard({ rows, error, onRetry }) {
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
                ${rows.map(
                  (u) => html`
                    <li
                      key=${u.installed_mod.source_id + "/" + u.installed_mod.id}
                      class="card__row"
                    >
                      <input type="checkbox" disabled title=${NOT_YET} />
                      <span class="card__row-name"
                        >${u.installed_mod.name}</span
                      >
                      <span class="mono card__row-detail"
                        >${u.installed_mod.version} → ${u.new_version}</span
                      >
                    </li>
                  `,
                )}
              </ul>
              <button type="button" class="button" disabled title=${NOT_YET}>
                Update selected
              </button>
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
function HealthCard({ findings, result, error, onReverify }) {
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
                      <span class="card__row-detail">${healthLabel(f)}</span>
                      <button
                        type="button"
                        class="button button--small"
                        disabled
                        title=${NOT_YET}
                      >
                        Repair
                      </button>
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
                <button type="button" class="button" disabled title=${NOT_YET}>
                  Repair all
                </button>
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

/** healthLabel prefers the finding's own note (already human-worded, e.g. a
 * repair failure's reason) and falls back to the status verbatim. */
function healthLabel(f) {
  return f.note || f.status.replaceAll("_", " ");
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

function ConflictsCard({ rows, error, onRetry }) {
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
                        disabled
                        title=${NOT_YET}
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
