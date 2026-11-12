// adaptersuggestion.js - issue 606: the game page's offer of the adapter
// lmm's game catalog would set this game up with.
//
// Core decides whether there is one (core.Service.SuggestedAdapter, carried
// on the game's status as `suggested_adapter`): a game with no `adapter:` key
// that uses the identity, whose Steam app id the catalog names an adapter
// for - a Skyrim SE added before the catalog said `adapter: bethesda`, say.
// lmm never changes a game's adapter by itself; this note says so, and its
// button is the user choosing to, through the same PUT /api/v1/games/{id}
// `lmm game edit --adapter` makes.

import { html, useState } from "../render.js";
import { ApiError, updateGame } from "../api.js";

/**
 * AdapterSuggestion renders nothing for a game with nothing to suggest.
 * `onApplied` re-reads the game's status, which then carries no suggestion,
 * so the note goes away by itself.
 */
export function AdapterSuggestion({ suggested, gameID, onApplied }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!suggested) return null;

  async function apply() {
    setBusy(true);
    setError("");
    try {
      await updateGame(gameID, { adapter: suggested });
      await onApplied?.();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return html`
    <div
      class="adapter-suggestion"
      data-testid="adapter-suggestion"
      data-adapter=${suggested}
      role="note"
    >
      <p class="adapter-suggestion__text">
        <span class="badge badge--policy">Adapter</span> lmm's game catalog sets
        this game up with the <code>${suggested}</code> adapter, which knows how
        it lays out and loads its mods. It uses <code>generic-files</code> now;
        lmm never changes that by itself.
      </p>
      <button
        type="button"
        class="button button--small"
        data-action="use-suggested-adapter"
        disabled=${busy}
        onClick=${apply}
      >
        Use the ${suggested} adapter
      </button>
      ${
        error &&
        html`<p class="adapter-suggestion__error" role="alert">${error}</p>`
      }
    </div>
  `;
}
