// gameeditor.js - the Setup page's one editor for a configured game (issue
// 527): every field a Games row edits - name, install path (issue 528), mod
// path, sources, loader and, for a CurseForge game, the CurseForge game
// version types it accepts (issue 544) - in one panel, with one Save and one
// Cancel.
//
// One Save is one request: PUT /api/v1/games/{id} carries every changed
// field, and core (Service.EditGame) checks all of them against the game
// they leave before it writes games.yaml once, or not at all. So a refusal
// names ONE field, and this panel marks that field - the panel never guesses
// which input was at fault from a sentence.

import {
  html,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "../render.js";
import { codeSpans } from "../errortext.js";
import { ApiError, getSourceVersionTypes } from "../api.js";
import { SourcesMapEditor } from "./sourcesmap.js";
import {
  GameLoaderEditor,
  GameLoaderPanel,
  loaderDraft,
  loaderSpec,
} from "./gameloader.js";
import { FolderField } from "./folderpicker.js";
import { ModPathInUse, modPathInUseFor } from "./modpath.js";

/** gameDraft is the editor's local state for one game row: every field as
 * the row has it now. */
export function gameDraft(game) {
  return {
    name: game.name ?? "",
    install_path: game.install_path ?? "",
    mod_path: game.mod_path ?? "",
    sources: { ...(game.source_ids ?? {}) },
    loader: loaderDraft(game.loader),
    curseforge_types: (game.curseforge?.game_version_types ?? []).join(", "),
  };
}

/** curseForgeTypes reads the CurseForge game version types field: ids
 * separated by commas or spaces. Anything that is not a whole number goes as
 * 0, which core refuses on the field - so a typo is marked there, not
 * dropped. */
export function curseForgeTypes(text) {
  return text
    .split(/[\s,]+/)
    .filter((t) => t !== "")
    .map((t) => (/^\d+$/.test(t) ? Number(t) : 0));
}

/** curseForgeGuideURL is the guide's section on picking a flavor: where the
 * id field sends someone when lmm could not load CurseForge's list. */
export const curseForgeGuideURL =
  "https://donovanmods.github.io/linux-mod-manager/add-a-game.html#pick-a-curseforge-game-flavor";

/** pickerRows is the checklist for a game's CurseForge version types (issue
 * 563): every type CurseForge lists, by name, then every id the draft holds
 * that the list does not - shown by id rather than dropped, so a save never
 * loses one silently. */
export function pickerRows(types, ticked) {
  const listed = new Set(types.map((t) => t.id));
  return [
    ...types.map((t) => ({ ...t, checked: ticked.includes(t.id) })),
    ...ticked
      .filter((id) => id > 0 && !listed.has(id))
      .map((id) => ({ id, name: "", slug: "", checked: true })),
  ];
}

/** toggleType is the draft's ids text with id ticked or unticked: kept in
 * the order the user gave them, a newly ticked one last. */
export function toggleType(text, id, checked) {
  const ids = curseForgeTypes(text).filter((t) => t !== id);
  if (checked) ids.push(id);
  return ids.join(", ");
}

const sameMap = (a, b) => {
  const keys = Object.keys(a);
  return (
    keys.length === Object.keys(b).length &&
    keys.every((k) => Object.hasOwn(b, k) && a[k] === b[k])
  );
};

/* The version types are a set: the same ids in another order (an untick and
 * a re-tick moves one last) are no change, so nothing is sent and the stored
 * order stands. */
const sameTypeSet = (a, b) => {
  const set = (text) =>
    [...new Set(curseForgeTypes(text))].sort((x, y) => x - y).join();
  return set(a) === set(b);
};

const sameLoader = (a, b) =>
  ["kind", "version", "runtime", "bootstrap"].every((k) => a[k] === b[k]);

/** gameEditBody is the PUT body for draft against the row it was drawn from:
 * ONLY the fields that changed, since an absent member leaves its key alone
 * - so an edit of the name cannot re-send, and re-validate, a source map the
 * user never touched. An empty body means there is nothing to save. */
export function gameEditBody(game, draft) {
  const was = gameDraft(game);
  const body = {};
  if (draft.name !== was.name) body.name = draft.name;
  if (draft.install_path !== was.install_path) {
    body.install_path = draft.install_path;
  }
  if (draft.mod_path !== was.mod_path) body.mod_path = draft.mod_path;
  if (!sameMap(draft.sources, was.sources)) body.sources = draft.sources;
  if (!sameLoader(draft.loader, was.loader)) {
    body.loader = loaderSpec(draft.loader) ?? undefined;
    body.loader_set = true;
  }
  if (!sameTypeSet(draft.curseforge_types, was.curseforge_types)) {
    body.curseforge = {
      game_version_types: curseForgeTypes(draft.curseforge_types),
    };
  }
  return body;
}

/** gameEditErrorField names the panel field a refused save belongs to, from
 * the error envelope's details - by structure, never by the message:
 *
 *   - core.GameSpecError carries the wire key itself ("name",
 *     "install_path", "mod_path", "sources", "loader.runtime", ...);
 *   - core.GameInstallPathInUseError (issue 528) carries new_install_path;
 *   - core.GameModPathInUseError is modPathInUseFor's shape, and a game whose
 *     files are stranded under another mod_path (core.ModPathMissingError)
 *     carries a mod_path and a reason;
 *   - core.GameSourceInUseError carries source_id, and
 *     core.GameIdentifierError a source.
 *
 * Anything else ("adapter", which this panel does not edit, or a failure
 * with no details) is "", and the panel shows it beside its Save. */
export function gameEditErrorField(details) {
  if (!details || typeof details !== "object") return "";
  if (typeof details.field === "string") {
    if (details.field.startsWith("loader")) return "loader";
    if (details.field.startsWith("curseforge")) return "curseforge_types";
    return ["name", "install_path", "mod_path", "sources"].includes(
      details.field,
    )
      ? details.field
      : "";
  }
  if (typeof details.new_install_path === "string") return "install_path";
  if (modPathInUseFor(details)) return "mod_path";
  if (
    typeof details.mod_path === "string" &&
    typeof details.reason === "string"
  ) {
    return "mod_path";
  }
  if (
    typeof details.source_id === "string" ||
    typeof details.source === "string"
  ) {
    return "sources";
  }
  return "";
}

/** FieldError is one field's refusal, under the field, named by the input's
 * aria-describedby. */
function FieldError({ id, error }) {
  if (!error) return null;
  return html`<p class="modal__error" id=${id} data-testid="game-field-error">
    ${codeSpans(error.message)}
  </p>`;
}

/**
 * CurseForgeTypesPicker reads the game's version types from CurseForge and
 * renders them as a checklist by name (issue 563). The ticks ARE the draft's
 * ids text, so saving is the same PUT as the id field's. Until the list is
 * in, and whenever it cannot be had (no key, offline, the game not saved
 * with a curseforge source yet), `fallback` - the id field - is shown
 * instead, with a link to the guide.
 */
function CurseForgeTypesPicker({
  game,
  draft,
  busy,
  error,
  errorID,
  patch,
  fallback,
}) {
  const mapped = Object.hasOwn(game.source_ids ?? {}, "curseforge");
  // The list belongs to the SAVED CurseForge game: a save that re-points
  // the identifier reads the new game's list, and one that drops the
  // mapping drops the list for the id field.
  const identifier = mapped ? game.source_ids.curseforge : null;
  const [list, setList] = useState(null);
  const [failure, setFailure] = useState(null);

  useEffect(() => {
    setList(null);
    setFailure(null);
    if (!mapped) return undefined;
    let live = true;
    getSourceVersionTypes("curseforge", game.id)
      .then((report) => live && setList(report.types ?? []))
      .catch(
        (err) =>
          live &&
          setFailure(err instanceof ApiError ? err.message : String(err)),
      );
    return () => {
      live = false;
    };
  }, [game.id, mapped, identifier]);

  if (mapped && !list && !failure) {
    return html`<p
      class="empty-state__hint"
      data-testid="curseforge-types-loading"
    >
      Reading CurseForge's version types for this game…
    </p>`;
  }
  if (!list) {
    return html`<div
      class="game-editor__fields"
      data-testid="curseforge-types-editor"
    >
      ${fallback(
        html`${
            failure &&
            html`<span data-testid="curseforge-types-unavailable"
              >Couldn't load CurseForge's list: ${failure}.${" "}</span
            >`
          }Enter
          ids separated by commas, or leave it blank to accept every
          file.${" "}<a
            href=${curseForgeGuideURL}
            target="_blank"
            rel="noopener noreferrer"
            >How to find the ids</a
          >`,
      )}
    </div>`;
  }

  const rows = pickerRows(list, curseForgeTypes(draft.curseforge_types));
  const hintID = `curseforge-types-${game.id}-hint`;
  return html`
    <fieldset
      class="game-editor__group"
      data-testid="curseforge-types-picker"
      aria-describedby=${error ? errorID : hintID}
    >
      <legend class="plan__control">CurseForge game version types</legend>
      <p class="empty-state__hint" id=${hintID}>
        Tick the flavors this game is; lmm then offers only their files. None
        ticked accepts every file.
      </p>
      ${
        rows.length === 0
          ? html`<p class="empty-state__hint">
              CurseForge lists none for this game.
            </p>`
          : html`<div class="version-types">
              ${rows.map(
                (row) => html`
                  <label class="version-types__option" key=${row.id}>
                    <input
                      type="checkbox"
                      name="curseforge-type"
                      value=${row.id}
                      checked=${row.checked}
                      disabled=${busy}
                      onChange=${(e) =>
                        patch({
                          curseforge_types: toggleType(
                            draft.curseforge_types,
                            row.id,
                            e.currentTarget.checked,
                          ),
                        })}
                    />
                    ${
                      row.name
                        ? html`${row.name}${" "}<span
                              class="mono version-types__slug"
                              >${row.slug}</span
                            >`
                        : html`<span class="mono">${row.id}</span>${" "}<span
                              class="version-types__slug"
                              >(not in CurseForge's list)</span
                            >`
                    }
                  </label>
                `,
              )}
            </div>`
      }
      <div class="game-editor__answer" role="status">
        <${FieldError} id=${errorID} error=${error} />
      </div>
    </fieldset>
  `;
}

/**
 * GameEditor is the panel. It is a controlled component: setupgames.js owns
 * the draft, the answer and the request, and hands the panel `error` (the
 * last refusal: {message, field, details}) and `saved` (the last save
 * succeeded and nothing has changed since).
 *
 * `focus` says where keyboard focus goes when the panel opens: "mod_path"
 * when a mod-path warning or deep link opened it, the name otherwise.
 */
export function GameEditor({
  game,
  sources,
  draft,
  error,
  busy,
  saved,
  focus,
  loaderKey,
  onChange,
  onSave,
  onCancel,
}) {
  const ids = {
    name: `game-name-${game.id}`,
    install_path: `install-path-${game.id}`,
    mod_path: `mod-path-${game.id}`,
    curseforge_types: `curseforge-types-${game.id}`,
  };
  const nameRef = useRef(null);
  const modPathRef = useRef(null);
  const fieldError = (field) => (error && error.field === field ? error : null);
  const inUse =
    error?.field === "mod_path" ? modPathInUseFor(error.details) : null;
  const panelError = error && !error.field ? error : null;
  const dirty = Object.keys(gameEditBody(game, draft)).length > 0;
  const patch = (part) => onChange({ ...draft, ...part });

  // Mounting IS "the panel opened" (setupgames.js mounts it only while it
  // is open), so this runs once per opening: a keyboard user lands in the
  // field they came for instead of on <body> (issue 460 review F4).
  //
  // A LAYOUT effect, deliberately: Preact flushes a plain effect after paint
  // (a rAF, or a ~100ms timeout when the page is not painting), which is
  // after the panel is already on screen - so a user who clicked into another
  // field and started typing in that window had focus pulled back to Name
  // after the first keystroke and the rest of the text went nowhere (issue
  // 532). A layout effect runs in the commit itself, before the panel can be
  // interacted with.
  useLayoutEffect(() => {
    (focus === "mod_path" ? modPathRef : nameRef).current?.focus();
  }, []);

  const textField = (field, label, ref, name, hint) => {
    const err = fieldError(field);
    const input = html`
      <input
        id=${ids[field]}
        ref=${ref}
        type="text"
        class=${field === "name" ? "" : "mono"}
        name=${name}
        value=${draft[field]}
        disabled=${busy}
        aria-invalid=${err ? "true" : undefined}
        aria-describedby=${err ? `${ids[field]}-error` : `${ids[field]}-hint`}
        onInput=${(e) => patch({ [field]: e.currentTarget.value })}
      />
    `;
    return html`
      <div class="game-editor__field">
        <label class="plan__control" for=${ids[field]}>${label}</label>
        ${
          field === "install_path"
            ? html`<${FolderField}
                value=${draft.install_path}
                label="install path"
                disabled=${busy}
                onChoose=${(path) => patch({ install_path: path })}
                >${input}<//
              >`
            : input
        }
        ${
          /* One live region per field, mounted with the panel and never
          removed, so a refusal is announced (the issue 442 rule). */ ""
        }
        <div class="game-editor__answer" role="status">
          <${FieldError} id=${`${ids[field]}-error`} error=${err} />
        </div>
        <p class="empty-state__hint" id=${`${ids[field]}-hint`}>${hint}</p>
      </div>
    `;
  };

  // Only a game that maps CurseForge (or still carries the setting) has a
  // CurseForge flavor to pick.
  const showCurseForge =
    Object.hasOwn(draft.sources, "curseforge") ||
    (game.curseforge?.game_version_types ?? []).length > 0;
  const modPathErr = fieldError("mod_path");
  const sourcesErr = fieldError("sources");
  const loaderErr = fieldError("loader");
  return html`
    <div
      class="game-editor"
      data-testid="game-editor"
      data-game=${game.id}
      role="group"
      aria-label=${`Edit ${game.name}`}
    >
      <div class="game-editor__fields">
        ${textField("name", "Name", nameRef, "game-name", "How lmm shows the game.")}
        ${textField(
          "install_path",
          "Install path",
          null,
          "install-path",
          "The game's own folder. A mod path inside it moves with it; with files deployed, lmm accepts a new one only once the game folder itself has moved there.",
        )}
        <div class="game-editor__field" data-testid="mod-path-editor">
          <label class="plan__control" for=${ids.mod_path}>Mod path</label>
          <${FolderField}
            value=${draft.mod_path}
            fallback=${draft.install_path}
            label="mod path"
            disabled=${busy}
            onChoose=${(path) => patch({ mod_path: path })}
          >
            <input
              id=${ids.mod_path}
              ref=${modPathRef}
              type="text"
              class="mono"
              name="mod-path"
              value=${draft.mod_path}
              disabled=${busy}
              aria-invalid=${modPathErr && !inUse ? "true" : undefined}
              aria-describedby=${modPathErr ? `${ids.mod_path}-error` : `${ids.mod_path}-hint`}
              onInput=${(e) => patch({ mod_path: e.currentTarget.value })}
            />
          <//>
          <div
            class="game-editor__answer mod-path-editor__answer"
            role="status"
            id=${`${ids.mod_path}-error`}
          >
            ${
              modPathErr &&
              !inUse &&
              html`<p class="modal__error" data-testid="mod-path-field-error">
                ${codeSpans(modPathErr.message)}
              </p>`
            }
            ${inUse && html`<${ModPathInUse} details=${inUse} message=${error.message} />`}
          </div>
          <p class="empty-state__hint" id=${`${ids.mod_path}-hint`}>
            The directory the game loads mods from; a relative path is relative
            to the install path. lmm refuses a move while files are deployed
            under the old one, and says what to run first.
          </p>
        </div>
      </div>

      ${
        showCurseForge &&
        html`<${CurseForgeTypesPicker}
          game=${game}
          draft=${draft}
          busy=${busy}
          error=${fieldError("curseforge_types")}
          errorID=${`${ids.curseforge_types}-error`}
          patch=${patch}
          fallback=${(hint) =>
            textField(
              "curseforge_types",
              "CurseForge game version types",
              null,
              "curseforge-types",
              hint,
            )}
        />`
      }

      <fieldset class="game-editor__group">
        <legend class="plan__control">Sources</legend>
        <${SourcesMapEditor}
          sources=${sources}
          value=${draft.sources}
          disabled=${busy}
          onChange=${(map) => patch({ sources: map })}
        />
        <div class="game-editor__answer" role="status">
          <${FieldError} id=${`sources-${game.id}-error`} error=${sourcesErr} />
        </div>
      </fieldset>

      <div class="game-editor__group">
        <${GameLoaderEditor}
          value=${draft.loader}
          disabled=${busy}
          onChange=${(loader) => patch({ loader })}
        />
        <div class="game-editor__answer" role="status">
          <${FieldError} id=${`loader-${game.id}-error`} error=${loaderErr} />
        </div>
        <${GameLoaderPanel}
          gameID=${game.id}
          onSetModPath=${() => modPathRef.current?.focus()}
          refreshKey=${loaderKey}
        />
      </div>

      <div class="game-editor__actions">
        <button
          type="button"
          class="button button--primary"
          data-action="save-game"
          data-game=${game.id}
          disabled=${busy || !dirty}
          onClick=${onSave}
        >
          ${busy ? "Saving…" : "Save"}
        </button>
        <button
          type="button"
          class="button"
          data-action="cancel-edit-game"
          data-game=${game.id}
          disabled=${busy}
          onClick=${onCancel}
        >
          ${dirty ? "Cancel" : "Close"}
        </button>
        <div class="game-editor__answer game-editor__status" role="status">
          ${
            panelError &&
            html`<p class="modal__error" data-testid="game-editor-error">
              ${codeSpans(panelError.message)}
            </p>`
          }
          ${
            error?.field &&
            html`<p class="modal__error">
              Nothing was saved: see the field marked above.
            </p>`
          }
          ${saved && !dirty && !error && html`<p class="empty-state__hint" data-testid="game-saved">Saved.</p>`}
        </div>
      </div>
    </div>
  `;
}
