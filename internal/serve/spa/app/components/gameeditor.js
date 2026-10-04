// gameeditor.js - the Setup page's one editor for a configured game (issue
// 527): every field a Games row edits - name, install path (issue 528), mod
// path, sources and loader - in one panel, with one Save and one Cancel.
//
// One Save is one request: PUT /api/v1/games/{id} carries every changed
// field, and core (Service.EditGame) checks all of them against the game
// they leave before it writes games.yaml once, or not at all. So a refusal
// names ONE field, and this panel marks that field - the panel never guesses
// which input was at fault from a sentence.

import { html, useLayoutEffect, useRef } from "../render.js";
import { codeSpans } from "../errortext.js";
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
  };
}

const sameMap = (a, b) => {
  const keys = Object.keys(a);
  return (
    keys.length === Object.keys(b).length &&
    keys.every((k) => Object.hasOwn(b, k) && a[k] === b[k])
  );
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
