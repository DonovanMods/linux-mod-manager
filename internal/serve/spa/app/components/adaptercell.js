// adaptercell.js - a game's adapter, as the Games table and the loader
// panel show it (issues 353, 426, 449).
//
// It names the adapter the game USES - `effective_adapter`, absent for the
// generic-files identity - unless core refuses every flow on the game
// (`adapter_error`): such a game uses no adapter at all, so the cell names
// the CONFIGURED one, marks it refused, and carries core's sentence and
// where to fix it, exactly as `lmm game list`/`game show` do.

import { html } from "../render.js";
import { codeSpans } from "../errortext.js";

/** AdapterCell renders game's adapter for a table cell. */
export function AdapterCell({ game }) {
  if (!game.adapter_error) {
    return html`<span class="mono" data-testid="adapter-cell"
      >${game.effective_adapter || "generic-files"}</span
    >`;
  }
  return html`
    <div data-testid="adapter-cell" data-refused="true">
      <span class="mono">${game.adapter || "generic-files"}</span>${" "}
      <span class="badge badge--danger">refused</span>
      <${AdapterError} game=${game} />
    </div>
  `;
}

/** AdapterError is a refused game's sentence and its remedy, or nothing. */
export function AdapterError({ game }) {
  if (!game?.adapter_error) return null;
  return html`
    <div class="adapter-refusal" data-testid="adapter-error">
      <p class="modal__error">${codeSpans(game.adapter_error)}</p>
      <p>
        Nothing deploys to this game until its adapter can run: change it in its
        games.yaml entry, or run${" "}
        <code>lmm game edit ${game.id} --adapter ${"<name>"}</code>.
      </p>
    </div>
  `;
}
