// setupimport.js - the Setup page's Archive import section (issue 333,
// kind_import_archive.go): upload an archive, optionally link it to a
// source/mod id, then hand off to the confirm-plan framework the same way
// every other mutation in this application does.
//
// Upload progress uses XHR (api.js's uploadArchive - fetch has no upload
// events) rather than the plan/job pipeline: staging a file is not a core
// mutation, it is what makes POST /api/v1/plans/import_archive possible to
// call at all (kind_import_archive.go's own doc comment: "there is
// deliberately no path member"). Once staged, the actual import IS a
// Plan/Apply pair like everything else - actions.openPlan opens the same
// modal deploy/install/adopt do, and the button wrapped in InlineJob morphs
// into the job's own progress exactly like the top bar's Deploy control,
// which is this surface's own completion affordance (unit7-carry.md N7:
// "give the setup flows a completion affordance" - here, staying on the
// page and watching the control resolve inline, rather than a modal that
// closes and loses the outcome).

import { html, useEffect, useRef, useState } from "../render.js";
import {
  ApiError,
  listSources,
  maxUploadBytes,
  uploadArchive,
  deleteUpload,
} from "../api.js";
import { InlineJob } from "./jobprogress.js";

const IMPORT_ORIGIN = "setup:import-archive";

// uploadExpiryMinutes mirrors the server's own defaultUploadTTL
// (uploads.go) - a fact to display, not something the wire exposes as a
// queryable field.
const uploadExpiryMinutes = 30;

function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let value = n / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i += 1;
  }
  return `${value.toFixed(1)} ${units[i]}`;
}

export function SetupImportArchive({ state, actions }) {
  const [sources, setSources] = useState([]);
  const [upload, setUpload] = useState(null); // {upload_id, filename, size}
  const [progress, setProgress] = useState(null); // {loaded, total}
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState(null);
  const [sourceID, setSourceID] = useState("");
  const [modID, setModID] = useState("");
  const fileInput = useRef(null);
  const abortRef = useRef(null);

  useEffect(() => {
    listSources()
      .then((rows) => setSources(rows.filter((r) => r.type !== "error")))
      .catch(() => setSources([]));
  }, []);

  async function onPick(e) {
    const file = e.currentTarget.files?.[0];
    e.currentTarget.value = "";
    if (!file) return;
    setError(null);
    if (file.size > maxUploadBytes) {
      setError(
        `${file.name} is ${formatBytes(file.size)}, over the ${formatBytes(maxUploadBytes)} limit.`,
      );
      return;
    }
    setUploading(true);
    setProgress({ loaded: 0, total: file.size });
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const result = await uploadArchive(file, {
        onProgress: (loaded, total) =>
          setProgress({ loaded, total: total || file.size }),
        signal: controller.signal,
      });
      setUpload(result);
    } catch (err) {
      if (err?.name !== "AbortError") {
        setError(err instanceof ApiError ? err.message : String(err));
      }
    } finally {
      setUploading(false);
    }
  }

  async function cancelUpload() {
    if (!upload) return;
    const id = upload.upload_id;
    setUpload(null);
    setSourceID("");
    setModID("");
    try {
      await deleteUpload(id);
    } catch {
      // Best-effort - the upload's own TTL reclaims it either way.
    }
  }

  function startPlan() {
    // Deliberately does NOT clear `upload` on confirm: this panel - and the
    // InlineJob inside it - must stay mounted for the job's own progress
    // and outcome to render HERE rather than only as a toast (unit7-carry.md
    // N7's "completion affordance"; InlineJob unmounting the instant it
    // starts would defeat the whole point of wrapping the button in it).
    actions.openPlan({
      kind: "import_archive",
      origin: IMPORT_ORIGIN,
      title: `Import ${upload.filename}`,
      confirmLabel: "Import",
      options: {
        upload_id: upload.upload_id,
        ...(sourceID ? { source_id: sourceID, mod_id: modID } : {}),
      },
    });
  }

  // jobActive is true from the moment this origin's job starts until its
  // outcome is dismissed (InlineJob's own onDismiss clears it) - Cancel
  // must not delete the staged upload out from under a job that is
  // currently applying it (or has already consumed it on success).
  const jobActive = Boolean(state.origins?.[IMPORT_ORIGIN]);

  return html`
    <div class="setup-section" data-testid="setup-import-archive">
      ${
        !upload &&
        html`
          <label class="button button--small">
            ${uploading ? "Uploading…" : "Choose archive…"}
            <input
              ref=${fileInput}
              type="file"
              accept=".zip,.7z,.rar"
              disabled=${uploading}
              onChange=${onPick}
              hidden
            />
          </label>
          <p class="empty-state__hint">
            .zip, .7z or .rar, up to ${formatBytes(maxUploadBytes)}.
          </p>
        `
      }
      ${
        uploading &&
        progress &&
        html`
          <div class="job-progress">
            <div class="job-progress__bar">
              <div
                class="job-progress__fill"
                style=${`width: ${progress.total ? Math.round((progress.loaded / progress.total) * 100) : 0}%`}
              ></div>
            </div>
            <span class="job-progress__text"
              >${formatBytes(progress.loaded)} /
              ${formatBytes(progress.total)}</span
            >
          </div>
        `
      }
      ${error && html`<p class="modal__error">${error}</p>`}
      ${
        upload &&
        html`
          <div class="setup-import__staged" data-testid="staged-upload">
            <p>
              <span class="mono">${upload.filename}</span>
              (${formatBytes(upload.size)}) staged - discarded automatically
              after ${uploadExpiryMinutes} minutes if not imported.
            </p>

            <label class="plan__control">
              Link to a source (optional)
              <select
                value=${sourceID}
                disabled=${jobActive}
                onChange=${(e) => setSourceID(e.currentTarget.value)}
              >
                <option value="">None - unlinked import</option>
                ${sources.map((s) => html`<option key=${s.id} value=${s.id}>${s.name}</option>`)}
              </select>
            </label>
            ${
              sourceID &&
              html`
                <label class="plan__control">
                  Mod ID on that source
                  <input
                    type="text"
                    value=${modID}
                    disabled=${jobActive}
                    onInput=${(e) => setModID(e.currentTarget.value)}
                  />
                </label>
              `
            }

            <div class="setup-section__actions">
              <${InlineJob}
                origin=${IMPORT_ORIGIN}
                state=${state}
                actions=${actions}
              >
                <button
                  type="button"
                  class="button button--primary button--small"
                  data-action="import-archive"
                  onClick=${startPlan}
                >
                  Import…
                </button>
              <//>
              <button
                type="button"
                class="button button--small"
                disabled=${jobActive}
                title=${jobActive ? "This upload is already being imported" : undefined}
                onClick=${cancelUpload}
              >
                Cancel
              </button>
            </div>
          </div>
        `
      }
    </div>
  `;
}
