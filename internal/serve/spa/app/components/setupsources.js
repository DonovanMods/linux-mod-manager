// setupsources.js - the Setup page's Custom sources section (issue 333):
// list every source the process knows about (including load errors,
// task-A2's own wire note), a YAML editor for a new or existing definition
// (validate -> save), delete with an inline confirm, and a download of the
// raw definition.
//
// "In use" is the SPA's own computation (A2's report: "the SPA already
// holds each game's configured source ids from GET /api/v1/games") - it is
// advisory here (a plain annotation on the row); the DELETE route's own 409
// is still what actually refuses a removal a game maps, naming the games.

import { html, useEffect, useState } from "../render.js";
import {
  ApiError,
  listSources,
  listGames,
  getSourceDefinition,
  sourceDefinitionURL,
  validateSource,
  saveSource,
  deleteSource,
} from "../api.js";

const newSourceTemplate = `id: my-source
name: My Source
type: directory
path: /path/to/mods
`;

export function SetupSources() {
  const [sources, setSources] = useState(null);
  const [inUseBy, setInUseBy] = useState({}); // {sourceID: [gameID, ...]}
  const [error, setError] = useState(null);
  const [editing, setEditing] = useState(null); // "" = new, or an id

  async function reload() {
    try {
      const [rows, games] = await Promise.all([listSources(), listGames()]);
      setSources(rows);
      const byID = {};
      for (const g of games) {
        for (const sourceID of Object.keys(g.source_ids ?? {})) {
          (byID[sourceID] ??= []).push(g.id);
        }
      }
      setInUseBy(byID);
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  useEffect(() => {
    reload();
  }, []);

  if (error) {
    return html`
      <div class="empty-state empty-state--error">
        <p>Couldn't load sources: ${error}</p>
        <button type="button" class="button button--small" onClick=${reload}>
          Retry
        </button>
      </div>
    `;
  }
  if (sources === null) {
    return html`<p class="app-booting">Loading&#8230;</p>`;
  }

  async function afterSave() {
    setEditing(null);
    await reload();
  }

  return html`
    <div class="setup-section" data-testid="setup-sources">
      <table class="setup-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Name</th>
            <th>Type</th>
            <th>In use</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          ${sources.map(
            (s) => html`
              <${SourceRow}
                key=${s.id}
                source=${s}
                inUseBy=${inUseBy[s.id] ?? []}
                onEdit=${() => setEditing(s.id)}
                onChanged=${reload}
              />
            `,
          )}
        </tbody>
      </table>

      <div class="setup-section__actions">
        <button
          type="button"
          class="button button--small"
          onClick=${() => setEditing((v) => (v === "" ? null : ""))}
        >
          ${editing === "" ? "Cancel" : "New source…"}
        </button>
      </div>

      ${
        editing !== null &&
        html`<${SourceEditor}
          key=${editing}
          id=${editing}
          onSaved=${afterSave}
          onCancel=${() => setEditing(null)}
        />`
      }
    </div>
  `;
}

// customSourceTypes are the three user-definable source types
// (source.SourceDefinition's own "type" enum) - anything else on this wire
// is either a built-in (no definition file to edit/download/delete: 404 for
// all three, api_sources.go) or an "error" row of its own.
const customSourceTypes = new Set(["directory", "manifest", "api"]);

function SourceRow({ source, inUseBy, onEdit, onChanged }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const isError = source.type === "error";
  const isCustom = customSourceTypes.has(source.type);

  async function confirmDelete() {
    setBusy(true);
    setError(null);
    try {
      await deleteSource(source.id);
      await onChanged();
    } catch (err) {
      if (err instanceof ApiError && err.details?.games) {
        setError(`Configured for: ${err.details.games.join(", ")}`);
      } else {
        setError(err instanceof ApiError ? err.message : String(err));
      }
      setBusy(false);
    }
  }

  if (confirming) {
    return html`
      <tr data-source=${source.id}>
        <td colspan="5">
          Delete <span class="mono">${source.id}</span>?
          ${error && html`<span class="modal__error">${error}</span>`}
          <button
            type="button"
            class="button button--danger button--small"
            disabled=${busy}
            onClick=${confirmDelete}
          >
            ${busy ? "Deleting…" : "Yes, delete"}
          </button>
          <button
            type="button"
            class="button button--small"
            disabled=${busy}
            onClick=${() => setConfirming(false)}
          >
            Cancel
          </button>
        </td>
      </tr>
    `;
  }

  return html`
    <tr data-source=${source.id}>
      <td class="mono">${source.id}</td>
      <td>
        ${isError ? html`<span class="badge badge--danger">error</span> ${source.error}` : source.name}
      </td>
      <td>${source.type}</td>
      <td>${inUseBy.length > 0 ? inUseBy.join(", ") : "—"}</td>
      <td class="setup-table__actions">
        ${
          (isCustom || isError) &&
          html`
            <a
              class="button button--small"
              href=${sourceDefinitionURL(source.id)}
              download=${`${source.id}.yaml`}
            >
              Download
            </a>
            <button
              type="button"
              class="button button--small"
              onClick=${onEdit}
            >
              Edit
            </button>
            <button
              type="button"
              class="button button--small button--danger"
              onClick=${() => setConfirming(true)}
            >
              Delete
            </button>
          `
        }
      </td>
    </tr>
  `;
}

/** SourceEditor is the YAML textarea for a new source (id="") or an
 * existing one's definition (id set). Line numbers via CSS counters
 * (one <span> per line, no editor library - the task brief's own call). */
function SourceEditor({ id, onSaved, onCancel }) {
  const [yaml, setYaml] = useState(id ? "" : newSourceTemplate);
  const [loadError, setLoadError] = useState(null);
  const [report, setReport] = useState(null);
  const [validated, setValidated] = useState(false);
  const [busy, setBusy] = useState(false);
  const [probe, setProbe] = useState(false);
  const [saveError, setSaveError] = useState(null);

  useEffect(() => {
    if (!id) return;
    getSourceDefinition(id)
      .then((text) => setYaml(text))
      .catch((err) =>
        setLoadError(err instanceof ApiError ? err.message : String(err)),
      );
  }, [id]);

  function onEdit(e) {
    setYaml(e.currentTarget.value);
    setValidated(false);
    setReport(null);
  }

  async function runValidate() {
    setBusy(true);
    setSaveError(null);
    try {
      const r = await validateSource(yaml, { probe });
      setReport(r);
      setValidated(r.valid);
    } catch (err) {
      if (err instanceof ApiError && err.details) {
        setReport(err.details);
      } else {
        setSaveError(err instanceof ApiError ? err.message : String(err));
      }
      setValidated(false);
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    const targetID = id || report?.id;
    if (!targetID) return;
    setBusy(true);
    setSaveError(null);
    try {
      await saveSource(targetID, yaml);
      onSaved();
    } catch (err) {
      if (err instanceof ApiError && err.details) {
        setReport(err.details);
      }
      setSaveError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const lines = yaml.split("\n").length;

  return html`
    <div class="source-editor" data-testid="source-editor">
      ${loadError && html`<p class="modal__error">${loadError}</p>`}
      <div class="source-editor__frame">
        <div class="source-editor__gutter" aria-hidden="true">
          ${Array.from({ length: lines }, (_, i) => html`<span key=${i}>${i + 1}</span>`)}
        </div>
        <textarea
          class="source-editor__textarea mono"
          spellcheck="false"
          value=${yaml}
          onInput=${onEdit}
        ></textarea>
      </div>

      <label class="plan__control plan__control--inline">
        <input
          type="checkbox"
          checked=${probe}
          onChange=${(e) => setProbe(e.currentTarget.checked)}
        />
        Probe live after validating
      </label>

      <div class="setup-section__actions">
        <button
          type="button"
          class="button button--small"
          disabled=${busy}
          onClick=${runValidate}
        >
          ${busy ? "Working…" : "Validate"}
        </button>
        <button
          type="button"
          class="button button--small button--primary"
          disabled=${busy || !validated}
          onClick=${save}
        >
          Save
        </button>
        <button type="button" class="button button--small" onClick=${onCancel}>
          Cancel
        </button>
      </div>

      ${saveError && html`<p class="modal__error">${saveError}</p>`}
      ${report && html`<${ValidationReport} report=${report} />`}
    </div>
  `;
}

/** ValidationReport renders app.SourceValidationReport's findings. `path`
 * is deliberately never shown - it is "" for a draft, per task-A2's own
 * note not to render it. */
function ValidationReport({ report }) {
  const errors = report.errors ?? [];
  const warnings = report.warnings ?? [];
  return html`
    <div class="source-editor__report">
      <p class=${report.valid ? "plan__note" : "modal__error"}>
        ${report.valid ? "Valid definition." : "Invalid definition."}
      </p>
      ${
        errors.length > 0 &&
        html`<ul class="plan__paths">
          ${errors.map((e, i) => html`<li key=${i}>${e}</li>`)}
        </ul>`
      }
      ${
        warnings.length > 0 &&
        html`<ul class="plan__paths">
          ${warnings.map((w, i) => html`<li key=${i} class="plan__note--warn">${w}</li>`)}
        </ul>`
      }
      ${
        report.probe &&
        html`<p class=${report.probe.ok ? "plan__note" : "modal__error"}>
          Probe:
          ${report.probe.ok ? report.probe.summary || "ok" : report.probe.error}
        </p>`
      }
    </div>
  `;
}
