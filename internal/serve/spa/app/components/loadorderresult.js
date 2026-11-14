// loadorderresult.js - what a finished deploy or purge did to the game's
// file load-order lists (issue 620): core.FileLoadOrderResult, each entry of
// the result's `load_orders` field - Cyberpunk 2077's archive/pc/mod/
// modlist.txt. One line each saying whether the list was written, put back
// or left alone, then its findings. The CLI prints the same lines
// (cmd/lmm/loadorder.go renderLoadOrderResults).

import { html } from "../render.js";

/** loadOrderLine is one result's one-line summary, in the CLI's words. */
export function loadOrderLine(result) {
  if (!result || typeof result.status !== "string") return "";
  const id = typeof result.id === "string" ? result.id : "";
  const files = Number.isSafeInteger(result.files) ? result.files : 0;
  const external = Number.isSafeInteger(result.external) ? result.external : 0;
  switch (result.status) {
    case "written":
    case "unchanged": {
      const kept =
        external > 0 ? ` (${external} kept that lmm did not deploy)` : "";
      return `Load order ${id} ${result.status}: ${files} file(s)${kept}`;
    }
    case "restored":
      return `Load order ${id}: the list you had before lmm wrote it is back`;
    case "removed":
      return `Load order ${id}: list removed (there was none before lmm wrote it)`;
    default:
      return `Load order ${id} not written`;
  }
}

/** shownLoadOrders is the results worth a line: every one but an
 * unchanged list with nothing to say, the common case. */
export function shownLoadOrders(results) {
  if (!Array.isArray(results)) return [];
  return results.filter(
    (r) =>
      loadOrderLine(r) !== "" &&
      (r.status !== "unchanged" || (r.findings?.length ?? 0) > 0),
  );
}

/** LoadOrderResults renders a result's load-order lists, or nothing. */
export function LoadOrderResults({ results }) {
  const shown = shownLoadOrders(results);
  if (shown.length === 0) return null;
  return html`
    <div class="job-progress__explainer" data-testid="load-order-result">
      ${shown.map(
        (r) => html`
          <p key=${r.id} data-load-order-status=${r.status}>
            ${loadOrderLine(r)}
          </p>
          ${
            r.list_file &&
            html`<p class="mono plugin-list__path">${r.list_file}</p>`
          }
          ${
            (r.findings?.length ?? 0) > 0 &&
            html`<ul class="tray__skips">
              ${r.findings
                .filter((f) => typeof f?.message === "string")
                .map(
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
        `,
      )}
    </div>
  `;
}
