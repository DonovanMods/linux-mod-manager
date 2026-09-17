// setupgames.js - the Setup page's Games section (issue 333): the
// configured games table, the same detect/add flows the first-run chooser
// uses (gameadd.js), and default-game set/clear.

import { html, useEffect, useState } from "../render.js";
import {
  ApiError,
  listGames,
  listSources,
  updateGameSources,
  updateGameModPath,
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
import { ModPathEditor, ModPathWarning } from "./modpath.js";
import { AdapterCell } from "./adaptercell.js";

/** setDefaultGame/clearDefaultGame are this section's own two mutations -
 * thin single-step writes (api_games.go, issue 333) with nothing to preview, the
 * same class the profile routes' set-default already is. */
const setDefaultGame = (id) =>
  post(`/api/v1/games/${encodeURIComponent(id)}/set-default`);
const clearDefaultGame = () => del("/api/v1/games/default");

export function SetupGames({
  actions,
  game,
  profile,
  editModPath = "",
  suggestedModPath = "",
}) {
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
  // Which row's MOD PATH is open (issue 460), its draft, and the answer its
  // last save got. A deep link (router.js#modPathEditPath - every "Set mod
  // path…" action outside this table) opens it on arrival, prefilled with
  // core's suggestion when the link carries one.
  const [editingModPath, setEditingModPath] = useState(null); // {id, value}
  const [modPathError, setModPathError] = useState(null); // {id, message, field, details}

  async function reload() {
    try {
      const rows = await listGames();
      setGames(rows);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  useEffect(() => {
    if (!editModPath) return;
    setEditingModPath((current) =>
      current?.id === editModPath
        ? current
        : { id: editModPath, value: suggestedModPath, deepLinked: true },
    );
  }, [editModPath, suggestedModPath]);

  // A deep link with no suggestion prefills the game's current mod_path,
  // once the rows are in - the value the user is about to correct.
  useEffect(() => {
    if (!games || !editingModPath?.deepLinked || editingModPath.value) return;
    const row = games.find((g) => g.id === editingModPath.id);
    if (row) setEditingModPath({ id: row.id, value: row.mod_path ?? "" });
  }, [games, editingModPath]);

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

  function toggleModPathEditor(g) {
    setModPathError(null);
    setEditingModPath(
      editingModPath?.id === g.id
        ? null
        : { id: g.id, value: g.mod_path ?? "" },
    );
  }

  async function saveModPath() {
    if (!editingModPath) return;
    const { id, value } = editingModPath;
    setBusyID(id);
    setModPathError(null);
    try {
      await updateGameModPath(id, value);
      setEditingModPath(null);
      await reload();
      await actions.reloadStatus();
    } catch (err) {
      const api = err instanceof ApiError;
      setModPathError({
        id,
        message: api ? err.message : String(err),
        field: api ? (err.details?.field ?? "") : "",
        details: api ? err.details : null,
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

  // The Adapter column (issue 353, the game-adapter seam) is READ-ONLY in
  // this unit. It shows the adapter the game USES (issue 426):
  // `effective_adapter`, which names a derived adapter too (icarus for
  // deploy_mode: compile, bepinex for a game with BepInEx), where `adapter`
  // is only what games.yaml says. An absent `effective_adapter` IS the
  // generic-files identity, so the cell names it rather than leaving a
  // blank - there is no such thing as a game with no adapter. A game core
  // refuses (`adapter_error`, issue 449) is the exception: it uses none, and
  // the cell says so (adaptercell.js).
  return html`
    <div class="setup-section" data-testid="setup-games">
      <table class="setup-table">
        <thead>
          <tr>
            <th>Name</th>
            <th class="col--path">Install path</th>
            <th class="col--path">Mod path</th>
            <th>Adapter</th>
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
                <td class="col--path" title=${g.mod_path}>
                  <span class="mono">${g.mod_path}</span>${" "}
                  <button
                    type="button"
                    class="button button--small"
                    data-action="edit-mod-path"
                    data-game=${g.id}
                    aria-expanded=${editingModPath?.id === g.id ? "true" : "false"}
                    disabled=${busyID === g.id}
                    onClick=${() => toggleModPathEditor(g)}
                  >
                    ${editingModPath?.id === g.id ? "Cancel" : "Edit mod path…"}
                  </button>
                  <${ModPathWarning}
                    error=${g.mod_path_error}
                    gameID=${g.id}
                    onSetModPath=${() =>
                      editingModPath?.id !== g.id && toggleModPathEditor(g)}
                  />
                </td>
                <td><${AdapterCell} game=${g} /></td>
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
                  <td colspan="7">
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
                editingModPath?.id === g.id &&
                html`<tr key=${`${g.id}-mod-path`} class="setup-table__editor">
                  <td colspan="7">
                    <${ModPathEditor}
                      gameID=${g.id}
                      value=${editingModPath.value}
                      busy=${busyID === g.id}
                      error=${modPathError?.id === g.id ? modPathError : null}
                      onChange=${(value) => setEditingModPath({ id: g.id, value })}
                      onSave=${saveModPath}
                    />
                  </td>
                </tr>`
              }
              ${
                editingLoader?.id === g.id &&
                html`<tr key=${`${g.id}-loader`} class="setup-table__editor">
                  <td colspan="7">
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
                      onSetModPath=${() =>
                        editingModPath?.id !== g.id && toggleModPathEditor(g)}
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
