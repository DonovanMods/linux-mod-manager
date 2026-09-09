// shortcutsmodal.js - the keyboard-shortcuts help the design's modal
// inventory names (docs/plans/2026-08-31-serve-spa-design.md §Modals), and
// the last entry in it with nothing behind it before issue 334's gate
// review (Important 3).
//
// It renders shortcuts.js's rows - the same rows the README's Keyboard table
// documents, pinned to each other by a Go test - in the shared modal shell,
// so it gets Escape, the scrim click, the focus trap and focus-return for
// free like every other modal.

import { html } from "../render.js";
import { Modal } from "./modal.js";
import { shortcutRows } from "../shortcuts.js";

/** ShortcutsModal is a read-only modal: there is nothing to confirm, so its
 * only footer action is the one that closes it.
 *
 * Self-guarded on the shared slot's shape (C1, unit 6 fix wave) even though
 * app.js already mounts it conditionally. */
export function ShortcutsModal({ modal, actions }) {
  if (modal?.type !== "shortcuts") return null;

  return html`
    <${Modal}
      kind="shortcuts"
      title="Keyboard shortcuts"
      onClose=${actions.closeModal}
      openerSelector=${modal.openerSelector}
      footer=${html`
        <button
          type="button"
          class="button button--primary"
          data-action="close-shortcuts"
          onClick=${actions.closeModal}
        >
          Close
        </button>
      `}
    >
      <table class="shortcuts">
        <thead>
          <tr>
            <th scope="col">Key</th>
            <th scope="col">Where</th>
            <th scope="col">What it does</th>
          </tr>
        </thead>
        <tbody>
          ${shortcutRows.map(
            (row) => html`
              <tr key=${row.keys + row.where}>
                <td><kbd class="shortcuts__keys">${row.keys}</kbd></td>
                <td>${row.where}</td>
                <td>${row.what}</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
    <//>
  `;
}
