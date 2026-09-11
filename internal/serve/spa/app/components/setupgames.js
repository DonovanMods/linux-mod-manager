// setupgames.js - the Setup page's Games section (issue 333): the
// configured games table, the same detect/add flows the first-run chooser
// uses (gameadd.js), and default-game set/clear.

import { html, useEffect, useState } from "../render.js";
import {
  ApiError,
  listGames,
  listSources,
  updateGameSources,
  post,
  del,
} from "../api.js";
import { SourcesMapEditor } from "./sourcesmap.js";
import {
  GameLoaderEditor,
  GameLoaderPanel,
  loaderDraft,
  loaderSpec,
  updateGameLoader,
} from "./gameloader.js";
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
  // detected (issue 206) is the row an uncurated "Add with details…" click, or
  // the add form's own "Pick an installed game…" picker, hands up here -
  // null opens a blank manual form.
  const [detected, setDetected] = useState(null);
  const [busyID, setBusyID] = useState(null);
  const [rowError, setRowError] = useState(null);
  const [sources, setSources] = useState(null);
  // Which row's source map is open for editing, and the draft it holds.
  // One at a time: two open editors over the same replacement-shaped PUT is
  // two ways to lose an edit.
  const [editing, setEditing] = useState(null); // {id, map}
  // Which row's LOADER is open, and its draft. Separate from `editing`
  // because the two are separate requests (api_games.go refuses a body
  // carrying both), so they are separate controls rather than one editor
  // whose Save means two different writes.
  const [editingLoader, setEditingLoader] = useState(null); // {id, draft}
  // Bumped after a loader save so the open panel re-reads the game directory
  // instead of guessing what changed.
  const [loaderKey, setLoaderKey] = useState(0);

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
    // The registered sources are what the mapping editor offers; a failure
    // there degrades that ONE control (no editor) rather than the table.
    listSources()
      .then((rows) => setSources(rows.filter((r) => r.type !== "error")))
      .catch(() => setSources([]));
  }, []);

  async function saveSources() {
    if (!editing) return;
    setBusyID(editing.id);
    setRowError(null);
    try {
      await updateGameSources(editing.id, editing.map);
      setEditing(null);
      await reload();
      await actions.reloadStatus();
    } catch (err) {
      // The 409 a removal that would orphan installed mods answers with
      // names the mods; the envelope's own message already says so, so it
      // is rendered verbatim rather than re-worded here.
      setRowError({
        id: editing.id,
        message: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      setBusyID(null);
    }
  }

  async function saveLoader() {
    if (!editingLoader) return;
    setBusyID(editingLoader.id);
    setRowError(null);
    try {
      await updateGameLoader(editingLoader.id, loaderSpec(editingLoader.draft));
      setLoaderKey((k) => k + 1);
      await reload();
      await actions.reloadStatus();
    } catch (err) {
      // A rejected value's envelope already names the field and the valid
      // set, so it is rendered verbatim rather than re-worded here.
      setRowError({
        id: editingLoader.id,
        message: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      setBusyID(null);
    }
  }

  async function afterAdd() {
    setShowDetect(false);
    setShowAdd(false);
    setDetected(null);
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
    return html`<p class="app-booting">Loading your games…</p>`;
  }

  return html`
    <div class="setup-section" data-testid="setup-games">
      <table class="setup-table">
        <thead>
          <tr>
            <th>Name</th>
            <th class="col--path">Install path</th>
            <th class="col--path">Mod path</th>
            <th>Sources</th>
            <th>Loader</th>
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
                <td class="col--path mono" title=${g.install_path}>
                  ${g.install_path}
                </td>
                <td class="col--path mono" title=${g.mod_path}>
                  ${g.mod_path}
                </td>
                <td>
                  <span class="mono"
                    >${Object.keys(g.source_ids ?? {}).join(", ") || "—"}</span
                  >${" "}
                  <button
                    type="button"
                    class="button button--small"
                    data-action="edit-sources"
                    data-game=${g.id}
                    disabled=${busyID === g.id || sources === null}
                    onClick=${() =>
                      setEditing(
                        editing?.id === g.id
                          ? null
                          : { id: g.id, map: { ...(g.source_ids ?? {}) } },
                      )}
                  >
                    ${editing?.id === g.id ? "Cancel" : "Edit sources…"}
                  </button>
                </td>
                <td>
                  <span class="mono" data-testid="loader-cell"
                    >${g.loader?.kind ?? "—"}</span
                  >${" "}
                  <button
                    type="button"
                    class="button button--small"
                    data-action="edit-loader"
                    data-game=${g.id}
                    disabled=${busyID === g.id}
                    onClick=${() =>
                      setEditingLoader(
                        editingLoader?.id === g.id
                          ? null
                          : { id: g.id, draft: loaderDraft(g.loader) },
                      )}
                  >
                    ${editingLoader?.id === g.id ? "Cancel" : "Edit loader…"}
                  </button>
                </td>
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
              ${
                editing?.id === g.id &&
                html`<tr key=${`${g.id}-sources`} class="setup-table__editor">
                  <td colspan="6">
                    <${SourcesMapEditor}
                      sources=${sources}
                      value=${editing.map}
                      disabled=${busyID === g.id}
                      onChange=${(map) => setEditing({ id: g.id, map })}
                    />
                    <button
                      type="button"
                      class="button button--small button--primary"
                      data-action="save-sources"
                      disabled=${busyID === g.id}
                      onClick=${saveSources}
                    >
                      ${busyID === g.id ? "Saving…" : "Save sources"}
                    </button>
                  </td>
                </tr>`
              }
              ${
                editingLoader?.id === g.id &&
                html`<tr key=${`${g.id}-loader`} class="setup-table__editor">
                  <td colspan="6">
                    <${GameLoaderEditor}
                      value=${editingLoader.draft}
                      disabled=${busyID === g.id}
                      onChange=${(draft) => setEditingLoader({ id: g.id, draft })}
                    />
                    <button
                      type="button"
                      class="button button--small button--primary"
                      data-action="save-loader"
                      disabled=${busyID === g.id}
                      onClick=${saveLoader}
                    >
                      ${busyID === g.id ? "Saving…" : "Save loader"}
                    </button>
                    <${GameLoaderPanel}
                      gameID=${g.id}
                      refreshKey=${loaderKey}
                    />
                  </td>
                </tr>`
              }
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
          onClick=${() => {
            setDetected(null);
            setShowAdd((v) => !v);
          }}
        >
          ${showAdd ? "Hide add form" : "Add a game manually…"}
        </button>
      </div>

      ${
        showDetect &&
        html`<${GameDetectSection}
          actions=${actions}
          onAdded=${afterAdd}
          onAddWithDetails=${(row) => {
            setDetected(row);
            setShowAdd(true);
          }}
        />`
      }
      ${
        showAdd &&
        html`<${GameAddForm}
          onAdded=${afterAdd}
          game=${game}
          profile=${profile}
          detected=${detected}
          onClearDetected=${() => setDetected(null)}
        />`
      }
    </div>
  `;
}
