// setupgames.js - the Setup page's Games section (issue 333): the
// configured games table, the same detect/add flows the first-run chooser
// uses (gameadd.js), and default-game set/clear.

import { html, useEffect, useState } from "../render.js";
import { ApiError, listGames, post, del } from "../api.js";
import { GameDetectSection, GameAddForm } from "./gameadd.js";

/** setDefaultGame/clearDefaultGame are this section's own two mutations -
 * thin single-step writes (api_games.go, issue 333) with nothing to preview, the
 * same class the profile routes' set-default already is. */
const setDefaultGame = (id) =>
  post(`/api/v1/games/${encodeURIComponent(id)}/set-default`);
const clearDefaultGame = () => del("/api/v1/games/default");

export function SetupGames({ actions, game, profile }) {
  const [games, setGames] = useState(null);
  const [error, setError] = useState(null);
  const [showDetect, setShowDetect] = useState(false);
  const [showAdd, setShowAdd] = useState(false);
  const [busyID, setBusyID] = useState(null);
  const [rowError, setRowError] = useState(null);

  async function reload() {
    try {
      setGames(await listGames());
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  useEffect(() => {
    reload();
  }, []);

  async function afterAdd() {
    setShowDetect(false);
    setShowAdd(false);
    await reload();
    await actions.reloadStatus();
  }

  async function toggleDefault(game) {
    setBusyID(game.id);
    setRowError(null);
    try {
      if (game.default) {
        await clearDefaultGame();
      } else {
        await setDefaultGame(game.id);
      }
      await reload();
      await actions.reloadStatus();
    } catch (err) {
      setRowError({
        id: game.id,
        message: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      setBusyID(null);
    }
  }

  if (error) {
    return html`
      <div class="empty-state empty-state--error">
        <p>Couldn't load games: ${error}</p>
        <button type="button" class="button button--small" onClick=${reload}>
          Retry
        </button>
      </div>
    `;
  }
  if (games === null) {
    return html`<p class="app-booting">Loading…</p>`;
  }

  return html`
    <div class="setup-section" data-testid="setup-games">
      <table class="setup-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Install path</th>
            <th>Mod path</th>
            <th>Default</th>
          </tr>
        </thead>
        <tbody>
          ${games.map(
            (g) => html`
              <tr key=${g.id}>
                <td>
                  ${g.name} <span class="mono empty-state__hint">${g.id}</span>
                </td>
                <td class="mono">${g.install_path}</td>
                <td class="mono">${g.mod_path}</td>
                <td>
                  <button
                    type="button"
                    class="button button--small ${g.default ? "button--primary" : ""}"
                    disabled=${busyID === g.id}
                    onClick=${() => toggleDefault(g)}
                  >
                    ${g.default ? "Default" : "Set default"}
                  </button>
                  ${
                    rowError?.id === g.id &&
                    html`<p class="modal__error">${rowError.message}</p>`
                  }
                </td>
              </tr>
            `,
          )}
        </tbody>
      </table>

      <div class="setup-section__actions">
        <button
          type="button"
          class="button button--small"
          onClick=${() => setShowDetect((v) => !v)}
        >
          ${showDetect ? "Hide detect" : "Detect games…"}
        </button>
        <button
          type="button"
          class="button button--small"
          onClick=${() => setShowAdd((v) => !v)}
        >
          ${showAdd ? "Hide add form" : "Add a game manually…"}
        </button>
      </div>

      ${showDetect && html`<${GameDetectSection} onAdded=${afterAdd} />`}
      ${
        showAdd &&
        html`<${GameAddForm}
          onAdded=${afterAdd}
          game=${game}
          profile=${profile}
        />`
      }
    </div>
  `;
}
