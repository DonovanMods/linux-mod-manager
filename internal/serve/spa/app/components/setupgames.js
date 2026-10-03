// setupgames.js - the Setup page's Games section (issue 333): the
// configured games table, the same detect/add flows the first-run chooser
// uses (gameadd.js), and default-game set/clear.

import { html, useEffect, useRef, useState } from "../render.js";
import {
  ApiError,
  listGames,
  listSources,
  updateGame,
  post,
  del,
} from "../api.js";
import { GameDetectSection, GameAddForm } from "./gameadd.js";
import { ModPathWarning } from "./modpath.js";
import { AdapterCell } from "./adaptercell.js";
import { EditButton } from "./pencil.js";
import {
  GameEditor,
  gameDraft,
  gameEditBody,
  gameEditErrorField,
} from "./gameeditor.js";

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
  // Which row's editor is open (issue 527), the draft it holds, and where
  // focus goes when it opens. One at a time: the panel is the one place a
  // configured game changes, and two open over the same game is two ways to
  // lose an edit. A deep link (router.js#modPathEditPath - every "Set mod
  // path…" action outside this table) opens it on arrival, focused on the
  // mod path and prefilled with core's suggestion when the link carries one.
  const [editingGame, setEditingGame] = useState(null); // {id, draft, focus}
  const [gameError, setGameError] = useState(null); // {id, message, field, details}
  // The row whose last save succeeded, so the panel can say so until the
  // next change.
  const [savedID, setSavedID] = useState(null);
  // Bumped after a save so the open loader panel re-reads the game
  // directory instead of guessing what changed.
  const [loaderKey, setLoaderKey] = useState(0);
  // The deep link already acted on, so a reload of the rows does not reopen
  // a panel the user has closed.
  const deepLinked = useRef("");

  async function reload() {
    try {
      const rows = await listGames();
      setGames(rows);
      setError(null);
      return rows;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      return null;
    }
  }

  useEffect(() => {
    const key = `${editModPath}\u0000${suggestedModPath}`;
    if (!editModPath || !games || deepLinked.current === key) return;
    const row = games.find((g) => g.id === editModPath);
    if (!row) return;
    deepLinked.current = key;
    const draft = gameDraft(row);
    if (suggestedModPath) draft.mod_path = suggestedModPath;
    setGameError(null);
    setSavedID(null);
    setEditingGame({ id: row.id, draft, focus: "mod_path" });
  }, [editModPath, suggestedModPath, games]);

  useEffect(() => {
    reload();
    // The registered sources are what the mapping editor offers; a failure
    // there degrades that ONE control (no editor) rather than the table.
    listSources()
      .then((rows) => setSources(rows.filter((r) => r.type !== "error")))
      .catch(() => setSources([]));
  }, []);

  // The row's pencil opens the panel, and closes it - discarding the draft -
  // when it is already open.
  function toggleEditGame(g) {
    setGameError(null);
    setRowError(null);
    setSavedID(null);
    setEditingGame(
      editingGame?.id === g.id
        ? null
        : { id: g.id, draft: gameDraft(g), focus: "name" },
    );
  }

  function cancelEditGame(id) {
    setEditingGame(null);
    setGameError(null);
    setSavedID(null);
    // The panel's controls are going away; focus goes back to the pencil
    // that opened it rather than to <body>.
    document
      .querySelector(`[data-action="edit-game"][data-game="${CSS.escape(id)}"]`)
      ?.focus();
  }

  // openOrFocusModPath is the row warning's own "Set mod path…" action
  // (review F4): it opens the row's panel on the mod path, or - when that
  // panel is ALREADY open - moves focus into its mod path input, never a
  // silent no-op for a click the user just made.
  function openOrFocusModPath(g) {
    if (editingGame?.id === g.id) {
      document.getElementById(`mod-path-${g.id}`)?.focus();
      return;
    }
    setGameError(null);
    setSavedID(null);
    setEditingGame({ id: g.id, draft: gameDraft(g), focus: "mod_path" });
  }

  // saveGame is the panel's one Save: every changed field in ONE request,
  // which core checks as a whole and writes once, or not at all. The panel
  // stays open on the saved values - the loader panel under it re-reads the
  // game directory, which is where the Steam launch option comes from.
  async function saveGame() {
    if (!editingGame) return;
    const { id, draft } = editingGame;
    const row = games.find((g) => g.id === id);
    const body = row ? gameEditBody(row, draft) : {};
    if (Object.keys(body).length === 0) return;
    setBusyID(id);
    setGameError(null);
    setSavedID(null);
    try {
      const entry = await updateGame(id, body);
      await reload();
      setEditingGame((current) =>
        current?.id === id ? { ...current, draft: gameDraft(entry) } : current,
      );
      setSavedID(id);
      setLoaderKey((k) => k + 1);
      await actions.reloadStatus();
    } catch (err) {
      const api = err instanceof ApiError;
      const details = api ? (err.details ?? null) : null;
      setGameError({
        id,
        message: api ? err.message : String(err),
        field: gameEditErrorField(details),
        details,
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
        <button type="button" class="button" onClick=${reload}>Retry</button>
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
      <table class="setup-table setup-table--games">
        <thead>
          <tr>
            <th>Name</th>
            <th class="col--path">Install path</th>
            <th class="col--path">Mod path</th>
            <th>Adapter</th>
            <th>Sources</th>
            <th>Loader</th>
            <th>Default</th>
            <th class="setup-table__row-actions">
              <span class="visually-hidden">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          ${games.map(
            (g) => html`
              <tr key=${g.id}>
                <td class="setup-table__name">
                  <span class="setup-table__name-text">
                    <span>${g.name}</span>${" "}
                    <span class="mono empty-state__hint">${g.id}</span>
                  </span>
                </td>
                <td class="col--path" title=${g.install_path}>
                  <span class="mono setup-table__path">${g.install_path}</span>
                </td>
                <td class="col--path">
                  <span class="mono setup-table__path" title=${g.mod_path}
                    >${g.mod_path}</span
                  >
                  <${ModPathWarning}
                    error=${g.mod_path_error}
                    gameID=${g.id}
                    onSetModPath=${() => openOrFocusModPath(g)}
                  />
                </td>
                <td><${AdapterCell} game=${g} /></td>
                <td>
                  <span class="mono"
                    >${Object.keys(g.source_ids ?? {}).join(", ") || "—"}</span
                  >
                </td>
                <td>
                  <span class="mono" data-testid="loader-cell"
                    >${g.loader?.kind ?? "—"}</span
                  >
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
                <td class="setup-table__row-actions">
                  <${EditButton}
                    label=${`Edit ${g.name}`}
                    tip=${`Edit ${g.name}`}
                    data-action="edit-game"
                    data-game=${g.id}
                    expanded=${editingGame?.id === g.id}
                    disabled=${busyID === g.id || sources === null}
                    onClick=${() => toggleEditGame(g)}
                  />
                </td>
              </tr>
              ${
                editingGame?.id === g.id &&
                html`<tr key=${`${g.id}-editor`} class="setup-table__editor">
                  <td colspan="8">
                    <${GameEditor}
                      game=${g}
                      sources=${sources}
                      draft=${editingGame.draft}
                      focus=${editingGame.focus}
                      busy=${busyID === g.id}
                      error=${gameError?.id === g.id ? gameError : null}
                      saved=${savedID === g.id}
                      loaderKey=${loaderKey}
                      onChange=${(draft) => {
                        setSavedID(null);
                        setEditingGame({ ...editingGame, draft });
                      }}
                      onSave=${saveGame}
                      onCancel=${() => cancelEditGame(g.id)}
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
          class="button"
          onClick=${() => setShowDetect((v) => !v)}
        >
          ${showDetect ? "Hide detect" : "Detect games…"}
        </button>
        <button
          type="button"
          class="button"
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
