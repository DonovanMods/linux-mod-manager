// plan_adopt.js - the confirm modal's renderer for a core.AdoptPlan (issue
// 333, kind_adopt.go): "Import untracked mods already in the game folder",
// driven from the Setup page's Adopt section.
//
// It renders the same facts the CLI prints before its "Import these mods?"
// prompt: the scan totals, one row per untracked entry naming whatever
// source match was found (or the reason none was), and the duplicate
// preview. Read-only, like plan_uninstall.js's reference shape - there is
// nothing to choose here beyond --skip-match, which is a PLAN-time option
// the Setup page's own form collects before this modal opens.

import { html } from "../render.js";

/** AdoptPlanView renders core.AdoptPlan (internal/core/adopt.go). */
export function AdoptPlanView({ plan }) {
  const scan = plan.scan ?? {};
  const tracked = scan.tracked ?? [];
  const untracked = scan.untracked ?? [];
  const backfill = scan.backfill ?? [];
  const matches = plan.matches ?? [];
  const duplicates = plan.duplicates ?? [];

  if (untracked.length === 0) {
    return html`
      <div class="plan plan--adopt">
        <p class="plan__note">
          Nothing untracked was found in the mod directory - there is nothing to
          adopt.
        </p>
        ${
          backfill.length > 0 &&
          html`<p class="plan__note">
            ${backfill.length} already-installed
            mod${backfill.length === 1 ? "" : "s"} will still have its metadata
            refreshed.
          </p>`
        }
      </div>
    `;
  }

  return html`
    <div class="plan plan--adopt">
      <p class="plan__summary">
        ${untracked.length} untracked
        mod${untracked.length === 1 ? "" : "s"}${" "} found (${tracked.length}
        already tracked).
      </p>

      ${
        scan.extract_mode_warning &&
        html`<p class="plan__note plan__note--warn">
          This game is not in copy mode - adopted mods are tracked in place, not
          cached.
        </p>`
      }

      <section class="plan__section">
        <p class="plan__heading">To adopt (${matches.length})</p>
        <ul class="plan__mods">
          ${matches.map(
            (m) => html`
              <li key=${m.untracked.file_name}>
                <span class="plan__mod-name">${m.untracked.file_name}</span
                >${" "}
                <span class="plan__mod-detail">${matchDetail(m)}</span>
              </li>
            `,
          )}
        </ul>
      </section>

      ${
        duplicates.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading plan__heading--warn">
              Likely duplicates (${duplicates.length})
            </p>
            <ul class="plan__paths">
              ${duplicates.map((d) => html`<li key=${d} class="mono">${d}</li>`)}
            </ul>
            <p class="plan__note">
              These match an already-installed mod's name and will be skipped.
            </p>
          </section>
        `
      }
      ${
        backfill.length > 0 &&
        html`<p class="plan__note">
          ${backfill.length} already-installed
          mod${backfill.length === 1 ? "" : "s"} will also have its metadata
          refreshed.
        </p>`
      }
    </div>
  `;
}

/** matchDetail is one AdoptMatch's own status line. */
function matchDetail(m) {
  if (m.error) return `no source matched: ${m.error}`;
  if (!m.mod) return "no source match - will be adopted as a local mod";
  return m.file
    ? `matched ${m.mod.name} (${m.mod.source_id})`
    : `matched ${m.mod.name} (${m.mod.source_id}) - no exact file to link`;
}
