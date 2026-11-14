// loadordertab.js - one of the reorder modal's load-order tabs (issue 620):
// a game's file load order, Cyberpunk 2077's archives, the third axis
// beside mod order and a Bethesda game's plugin order.
//
// It renders core.FileLoadOrderReport verbatim - the order, what placed
// each file, the mod that ships it - and never works an order out itself:
// every change is one core load-order edit (POST .../load-orders, the same
// PlanFileLoadOrderEdit/ApplyFileLoadOrderEdit pair `lmm load-order` runs),
// and the tab re-renders the order the answer carries. As on the Plugins
// tab, nothing is buffered for a Save: each change is written when it is
// made, which the tab says.
//
// Parity with the CLI: move (up/down, to the top or the end) and reset,
// with the reset's dry run shown before it applies.

import { html, useState } from "../render.js";
import { editProfileLoadOrder } from "../api.js";

/** placementLabel is a row's "placed by" badge text. */
function placementLabel(entry) {
  switch (entry.placement) {
    case "order":
      return "order";
    case "external":
      return "external";
    default:
      return "default";
  }
}

/** placementTitle explains a row's badge. */
function placementTitle(entry) {
  switch (entry.placement) {
    case "order":
      return "the profile's load order puts it here";
    case "external":
      return "lmm did not deploy it: it keeps the place the list gives it";
    default:
      return "not in the profile's order yet: its alphabetical place, where the game loads it with no list";
  }
}

/**
 * LoadOrderTab renders the panel for one load order of profileName. The
 * report is the modal's, and every edit's answer goes back up through
 * onReport, so the order shown on coming back to the tab is the modal's.
 */
export function LoadOrderTab({ profileName, context, report, onReport }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [resetPlan, setResetPlan] = useState(null);

  async function run(edit) {
    setBusy(true);
    setError(null);
    try {
      const res = await editProfileLoadOrder(
        profileName,
        { id: report.id, ...edit },
        false,
        context,
      );
      onReport(res.order);
      setResetPlan(null);
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  async function previewReset() {
    setBusy(true);
    setError(null);
    try {
      setResetPlan(
        await editProfileLoadOrder(
          profileName,
          { id: report.id, op: "reset" },
          true,
          context,
        ),
      );
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  const rows = report.files ?? [];
  const movable = rows.filter((r) => r.managed);
  function neighbour(row, delta) {
    const i = movable.findIndex((r) => r.name === row.name);
    return movable[i + delta]?.name;
  }
  const listName = report.list_file
    ? report.list_file.split("/").pop()
    : "the list file";

  return html`
    <div
      class="plugins-tab"
      data-testid="load-order-tab"
      data-order=${report.id}
    >
      <p class="plan__note">
        ${report.label} load top to
        bottom${
          report.first_wins
            ? ": one higher in the list wins a conflict with one lower down"
            : ""
        }.
        Each change is saved as you make
        it${report.active ? ` and written to ${listName}` : ""}. Files lmm did
        not deploy keep the place the list gives them.
      </p>
      ${
        report.list_file &&
        html`<p class="mono plugin-list__path">${report.list_file}</p>`
      }
      ${
        report.active &&
        !report.recorded &&
        html`<p class="plan__note" data-testid="load-order-unrecorded">
          The profile orders nothing yet: the game uses its own (alphabetical)
          order, shown here. Moving a file records the order, and lmm writes
          ${" "}${listName} from then on.
        </p>`
      }
      ${
        !report.active &&
        html`<p class="plan__note" data-testid="load-order-inactive">
          ${report.profile} is not the active profile: its files are not
          deployed, so this is its recorded order. It is written when the
          profile is deployed.
        </p>`
      }
      ${error && html`<p class="modal__error" data-testid="load-order-edit-error">${error}</p>`}

      <div class="plugins-tab__toolbar">
        <button
          type="button"
          class="button"
          data-action="load-order-reset"
          disabled=${busy || !report.recorded}
          onClick=${previewReset}
        >
          Reset order…
        </button>
      </div>
      ${
        resetPlan &&
        html`<section class="plan__section" data-testid="load-order-reset-plan">
          <h3 class="plan__heading">Reset</h3>
          <ul>
            ${resetPlan.changes.map((c) => html`<li>${c}</li>`)}
          </ul>
          <div class="plugins-tab__toolbar">
            <button
              type="button"
              class="button"
              disabled=${busy}
              onClick=${() => setResetPlan(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              class="button button--primary"
              data-action="load-order-reset-apply"
              disabled=${busy}
              onClick=${() => run({ op: "reset" })}
            >
              Reset
            </button>
          </div>
        </section>`
      }
      ${
        rows.length === 0
          ? html`<p class="plan__note">
              ${report.active ? "No files deployed." : ""}
            </p>`
          : html`<ul
              class="reorder-list plugins-list"
              data-testid="load-order-list"
            >
              ${rows.map((row) => {
                const up = neighbour(row, -1);
                const down = neighbour(row, 1);
                return html`
                  <li
                    key=${row.name}
                    class="reorder-row plugins-row"
                    data-file=${row.name}
                    data-placement=${row.placement}
                  >
                    <span class="reorder-row__position mono"
                      >${row.position}</span
                    >
                    <span class="reorder-row__name plugins-row__name">
                      <span class="mono">${row.name}</span>
                      <span
                        class="badge badge--policy"
                        data-testid="load-order-placement"
                        title=${placementTitle(row)}
                        >${placementLabel(row)}</span
                      >
                      ${
                        row.mod_name &&
                        html`<span class="plugins-row__detail"
                          >from ${row.mod_name}</span
                        >`
                      }
                    </span>
                    ${
                      row.managed &&
                      html`<span class="reorder-row__controls">
                        <button
                          type="button"
                          class="button button--small"
                          data-action="load-order-up"
                          aria-label=${`Move ${row.name} up`}
                          disabled=${busy || !up}
                          onClick=${() => run({ op: "move", file: row.name, before: up })}
                        >
                          ↑
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          data-action="load-order-down"
                          aria-label=${`Move ${row.name} down`}
                          disabled=${busy || !down}
                          onClick=${() => run({ op: "move", file: row.name, after: down })}
                        >
                          ↓
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          data-action="load-order-first"
                          aria-label=${`Move ${row.name} to the top`}
                          disabled=${busy || !up}
                          onClick=${() => run({ op: "move", file: row.name, to: "first" })}
                        >
                          First
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          data-action="load-order-last"
                          aria-label=${`Move ${row.name} to the end`}
                          disabled=${busy || !down}
                          onClick=${() => run({ op: "move", file: row.name, to: "last" })}
                        >
                          Last
                        </button>
                      </span>`
                    }
                  </li>
                `;
              })}
            </ul>`
      }
      ${
        (report.not_deployed?.length ?? 0) > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">
            ${report.active ? "In the order, not deployed now" : "The profile's order"}
          </h3>
          <ul>
            ${report.not_deployed.map((n) => html`<li class="mono">${n}</li>`)}
          </ul>
        </section>`
      }
      ${
        (report.findings?.length ?? 0) > 0 &&
        html`<ul class="tray__skips" data-testid="load-order-findings">
          ${report.findings.map(
            (f, i) =>
              html`<li
                key=${`${f.code}:${i}`}
                class="tray__skip plugin-list__finding plugin-list__finding--${f.severity === "error" ? "error" : "warning"}"
                data-code=${f.code}
              >
                ${f.severity === "error" ? "Error: " : "Warning: "}${f.message}
              </li>`,
          )}
        </ul>`
      }
    </div>
  `;
}
