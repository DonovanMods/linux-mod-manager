// gameloader.js - the web UI's half of issue 359's mod-loader declaration: the
// editor that records one, and the panel that tells you what to paste into
// Steam.
//
// The panel is the reason this exists. BepInEx on Linux needs a Steam LAUNCH
// OPTION, and lmm deliberately does not write it - those live in a Steam file
// that must be edited with the client closed, in an undocumented format,
// where a bad write loses every launch option for every game in the account.
// So the server works out which bootstrap the game needs and hands the exact
// string over as data (GET /api/v1/games/{id}), and this renders it beside a
// Copy control. Every sentence here comes from the server; nothing about
// which build or which option is decided in JavaScript.

import { html, useEffect, useState } from "../render.js";
import { ApiError, getGameDetail, updateGameLoader } from "../api.js";

/** The two closed vocabularies, matching domain.ValidLoaderRuntimes and
 * domain.ValidLoaderBootstraps. "" is a real value in both - "not answered
 * yet" - which the server fills in from the game's own install directory. */
const RUNTIMES = [
  ["", "Detect from the game directory"],
  ["mono", "Unity Mono"],
  ["il2cpp", "Unity IL2CPP"],
];
const BOOTSTRAPS = [
  ["", "Detect from the game directory"],
  ["native", "Native Linux build (run_bepinex.sh)"],
  ["proton", "Proton / Wine (winhttp proxy)"],
];

/** loaderDraft is the editor's local state for one game: the four unparsed
 * strings POST/PUT take, seeded from whatever the game already declares. */
export function loaderDraft(loader) {
  return {
    kind: loader?.kind ?? "",
    version: loader?.version ?? "",
    runtime: loader?.runtime ?? "",
    bootstrap: loader?.bootstrap ?? "",
  };
}

/** loaderSpec turns a draft into the wire member, or null for "no loader" -
 * which is what clears a declaration. Nothing is validated here: core owns
 * the vocabulary and names the offending field in its rejection, and a second
 * copy of those rules in JavaScript is a copy that drifts. */
export function loaderSpec(draft) {
  if (!draft?.kind) return null;
  return {
    kind: draft.kind,
    version: draft.version || undefined,
    runtime: draft.runtime || undefined,
    bootstrap: draft.bootstrap || undefined,
  };
}

/** GameLoaderEditor is the four-field form. It is a controlled component with
 * no submit of its own: the row that owns it decides when to save, the same
 * way SourcesMapEditor works. */
export function GameLoaderEditor({ value, onChange, disabled }) {
  const patch = (part) => onChange({ ...value, ...part });
  return html`
    <div class="loader-editor" data-testid="loader-editor">
      <label class="plan__control">
        Mod loader
        <select
          name="loader-kind"
          value=${value.kind}
          disabled=${disabled}
          onChange=${(e) => patch({ kind: e.currentTarget.value })}
        >
          <option value="">None</option>
          <option value="bepinex">BepInEx</option>
        </select>
      </label>
      ${
        value.kind &&
        html`
          <label class="plan__control">
            Installed version
            <input
              type="text"
              name="loader-version"
              value=${value.version}
              placeholder="5.4.23.5"
              disabled=${disabled}
              onInput=${(e) => patch({ version: e.currentTarget.value })}
            />
          </label>
          <label class="plan__control">
            Unity runtime
            <select
              name="loader-runtime"
              value=${value.runtime}
              disabled=${disabled}
              onChange=${(e) => patch({ runtime: e.currentTarget.value })}
            >
              ${RUNTIMES.map(
                ([v, label]) => html`<option value=${v}>${label}</option>`,
              )}
            </select>
          </label>
          <label class="plan__control">
            Bootstrap
            <select
              name="loader-bootstrap"
              value=${value.bootstrap}
              disabled=${disabled}
              onChange=${(e) => patch({ bootstrap: e.currentTarget.value })}
            >
              ${BOOTSTRAPS.map(
                ([v, label]) => html`<option value=${v}>${label}</option>`,
              )}
            </select>
          </label>
          <p class="empty-state__hint">
            lmm does not install the loader and never writes a Steam launch
            option. Record what you installed here, and lmm will tell you the
            option to paste and check afterwards that it loaded.
          </p>
        `
      }
    </div>
  `;
}

/** GameLoaderPanel renders one game's loader status: what the game
 * directory says, the exact launch option, and the server's own warnings.
 *
 * It fetches on mount and on every refreshKey change, so a save in the row
 * above it re-reads rather than guessing what changed. */
export function GameLoaderPanel({ gameID, refreshKey }) {
  const [status, setStatus] = useState(null);
  const [error, setError] = useState(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let live = true;
    setCopied(false);
    getGameDetail(gameID)
      .then((detail) => live && setStatus(detail.loader_status ?? null))
      .catch(
        (err) =>
          live && setError(err instanceof ApiError ? err.message : String(err)),
      );
    return () => {
      live = false;
    };
  }, [gameID, refreshKey]);

  if (error) {
    return html`<p class="modal__error" data-testid="loader-panel-error">
      Couldn't read this game's loader status: ${error}
    </p>`;
  }
  if (!status) {
    return html`<p class="empty-state__hint">Reading the game directory…</p>`;
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(status.launch_option);
      setCopied(true);
    } catch {
      // A browser that refuses clipboard access is not an error worth a
      // banner: the string is on screen and selectable either way.
      setCopied(false);
    }
  }

  return html`
    <div class="loader-panel" data-testid="loader-panel">
      <dl class="loader-panel__facts">
        <dt>Installed</dt>
        <dd data-testid="loader-installed">
          ${status.installed ? "yes" : "no - the preloader is not there"}
        </dd>
        <dt>Runtime</dt>
        <dd>${status.effective_runtime || "unknown"}</dd>
        <dt>Bootstrap</dt>
        <dd>${status.effective_bootstrap || "unknown"}</dd>
        ${
          status.loaded_at &&
          html`<dt>Last loaded</dt>
            <dd>${status.loaded_at}</dd>`
        }
      </dl>
      ${
        status.launch_option &&
        html`
          <p class="loader-panel__label">Steam launch options for this game:</p>
          <code class="mono loader-panel__option" data-testid="launch-option"
            >${status.launch_option}</code
          >
          <button
            type="button"
            class="button button--small"
            data-action="copy-launch-option"
            onClick=${copy}
          >
            ${copied ? "Copied" : "Copy"}
          </button>
          <p class="empty-state__hint">
            Paste it into Steam → Properties → Launch Options. lmm never writes
            it for you.
          </p>
        `
      }
      ${(status.warnings ?? []).map(
        (w) => html`<p class="modal__error" key=${w}>${w}</p>`,
      )}
    </div>
  `;
}

export { updateGameLoader };
