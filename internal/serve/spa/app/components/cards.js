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
 * nothing at all - not even the section - when none has anything to show. */
export function AttentionCards({ updates, health, conflicts }) {
  const updateRows = updates?.updates ?? [];
  const findings = (health?.result?.findings ?? []).filter(
    (f) => f.status !== "ok",
  );
  const conflictRows = conflicts?.conflicts ?? [];

  if (
    updateRows.length === 0 &&
    findings.length === 0 &&
    conflictRows.length === 0
  ) {
    return null;
  }

  return html`
    <section class="attention-cards">
      ${updateRows.length > 0 && html`<${UpdatesCard} rows=${updateRows} />`}
      ${
        findings.length > 0 &&
        html`<${HealthCard} findings=${findings} result=${health.result} />`
      }
      ${conflictRows.length > 0 && html`<${ConflictsCard} rows=${conflictRows} />`}
    </section>
  `;
}

function UpdatesCard({ rows }) {
  return html`
    <div class="card card--updates">
      <p class="card__title">⬆ Updates (${rows.length})</p>
      <ul class="card__list">
        ${rows.map(
          (u) => html`
            <li
              key=${u.installed_mod.source_id + "/" + u.installed_mod.id}
              class="card__row"
            >
              <input type="checkbox" disabled title=${NOT_YET} />
              <span class="card__row-name">${u.installed_mod.name}</span>
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
    </div>
  `;
}

function HealthCard({ findings, result }) {
  return html`
    <div class="card card--health">
      <p class="card__title">⚠ Health (${result.issues + result.warnings})</p>
      <ul class="card__list">
        ${findings.map(
          (f, i) => html`
            <li key=${f.mod_id + "/" + (f.file_id || i)} class="card__row">
              <span class="card__row-name">${f.mod_name || f.mod_id}</span>
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
      <button type="button" class="button" disabled title=${NOT_YET}>
        Repair all
      </button>
    </div>
  `;
}

/** healthLabel prefers the finding's own note (already human-worded, e.g. a
 * repair failure's reason) and falls back to the status verbatim. */
function healthLabel(f) {
  return f.note || f.status.replaceAll("_", " ");
}

function ConflictsCard({ rows }) {
  return html`
    <div class="card card--conflicts">
      <p class="card__title">⇄ Conflicts (${rows.length})</p>
      <ul class="card__list">
        ${rows.map(
          (c) => html`
            <li key=${c.path} class="card__row">
              <span class="card__row-name"
                >${c.owner.name} ↔
                ${c.also_in.map((m) => m.name).join(", ")}</span
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
    </div>
  `;
}
