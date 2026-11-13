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
import { ApiError, getGameStatus, updateGame } from "../api.js";

/**
 * AdapterSuggestion renders nothing for a game with nothing to suggest.
 * `onApplied` re-reads the game's status, which then carries no suggestion,
 * so the note goes away by itself.
 *
 * `warning` is core's sentence for a game with mods already installed
 * (core.Service.AdapterSwitchWarning): they keep the layout they were
 * installed with. When there is one, the button asks first - the warning,
 * then "Switch anyway" or "Cancel" - because the switch cannot re-lay out
 * what is already in the cache. The button re-reads the game's status before
 * it decides, so the question is asked about the mods installed NOW, not
 * when the page loaded; if that read fails, the page's own warning stands.
 */
export function AdapterSuggestion({ suggested, warning, gameID, onApplied }) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [confirmText, setConfirmText] = useState("");
  const [error, setError] = useState("");
  if (!suggested) return null;

  async function apply() {
    setBusy(true);
    setError("");
    try {
      await updateGame(gameID, { adapter: suggested });
      setConfirming(false);
      await onApplied?.();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function offer() {
    setBusy(true);
    setError("");
    let current = warning;
    try {
      const status = await getGameStatus(gameID);
      if (status?.suggested_adapter !== suggested) {
        // Taken, or no longer offered, since the page loaded: show the
        // game as it is rather than switching it.
        setBusy(false);
        await onApplied?.();
        return;
      }
      current = status.suggested_adapter_warning ?? "";
    } catch {
      // The page's own warning is the best answer left; asking on it is
      // the safe side.
    }
    setBusy(false);
    if (current) {
      setConfirmText(current);
      setConfirming(true);
      return;
    }
    await apply();
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
      ${
        confirming
          ? html`
              <div
                class="adapter-suggestion__confirm"
                data-testid="adapter-suggestion-confirm"
                role="alertdialog"
                aria-label="Switch adapter?"
              >
                <p class="adapter-suggestion__text">${confirmText}.</p>
                <button
                  type="button"
                  class="button button--small"
                  data-action="confirm-suggested-adapter"
                  disabled=${busy}
                  onClick=${apply}
                >
                  Switch anyway
                </button>
                <button
                  type="button"
                  class="button button--small"
                  data-action="cancel-suggested-adapter"
                  disabled=${busy}
                  onClick=${() => setConfirming(false)}
                >
                  Cancel
                </button>
              </div>
            `
          : html`
              <button
                type="button"
                class="button button--small"
                data-action="use-suggested-adapter"
                disabled=${busy}
                onClick=${offer}
              >
                Use the ${suggested} adapter
              </button>
            `
      }
      ${
        error &&
        html`<p class="adapter-suggestion__error" role="alert">${error}</p>`
      }
    </div>
  `;
}
