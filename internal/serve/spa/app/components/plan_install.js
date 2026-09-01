// plan_install.js - the confirm modal's renderer for a core.InstallPlan
// (issue 331, kind_install.go), the first renderer in this application that is
// more than a read-only preview: install is the one mutation whose plan
// document offers something to CHOOSE (issue 225's version/file selection), not
// merely something to read before confirming.
//
// The version ▾ groups InstallPlan.FilePool by each file's own Version -
// the candidate-pool disclosure kind_install.go's own doc comment promises
// (issue 331 carry-in) - and a FILE picker appears beneath it only when the
// chosen version itself resolves to more than one file (a merge-capable
// source's alternate-format files, say). A selection is written into the
// confirm-plan framework's modal.applyOptions via actions.setPlanOptions
// (main.js) DIRECTLY FROM the select's onChange - never from a useEffect:
// setPlanOptions calls store.set(), which synchronously re-renders the
// whole app (store.js has no external state library, no batching of its
// own), and Preact's render() is not safe to re-enter from inside its own
// effect-flush for the SAME root - an earlier version called it from a
// mount-time effect and corrupted the DOM the instant a re-render landed
// mid-effect-flush (observed live: a Preact "insertBefore" DOM exception
// followed by a stack-overflowing render loop, chromedp catching both).
// An onChange handler runs OUTSIDE Preact's render/effect cycle entirely,
// same as every other mutation this application already fires from an
// event handler (modpanel.js's ModSettingsControls, say) - so nothing here
// EVER calls setPlanOptions except in response to an actual user pick, and
// an untouched picker submits no options at all: ApplyInstall's own
// defaults already resolve to the exact file Files names.
//
// ApplyInstall resolves TargetVersion/TargetFileIDs from applyOptions
// itself (issue 96/issue 140), so this renderer never touches plan.Files:
// it only says what was chosen, the same way DeployPlanView only ever reads
// what core already decided.
//
// A pool of length <= 1 renders no picker at all (kind_install.go's own
// words): there is nothing to choose between, and Files already names the
// one candidate there is.

import { html, useState } from "../render.js";

/** distinctVersions returns pool's Version values, first-seen order (the
 * pool's own FilterAndSortFiles order - newest/primary-favoring - so the
 * version ▾ lists the likely pick first). */
function distinctVersions(pool) {
  const seen = new Set();
  const out = [];
  for (const f of pool) {
    if (!seen.has(f.version)) {
      seen.add(f.version);
      out.push(f.version);
    }
  }
  return out;
}

/** InstallPlanView renders core.InstallPlan (internal/core/install.go). */
export function InstallPlanView({ plan, actions }) {
  const pool = plan.file_pool ?? [];
  const showPicker = pool.length > 1;
  const versions = distinctVersions(pool);
  const defaultVersion = plan.files?.[0]?.version ?? plan.mod.version;

  const [version, setVersion] = useState(defaultVersion);
  const filesForVersion = pool.filter((f) => f.version === version);
  const showFilePicker = filesForVersion.length > 1;
  const [fileID, setFileID] = useState("");

  /** pickVersion applies a version change: local display state, AND the
   * confirm-plan framework's own applyOptions - fired together, from the
   * event handler, never from an effect (see this file's header comment). */
  function pickVersion(v) {
    setVersion(v);
    setFileID("");
    actions.setPlanOptions({ version: v, file_ids: undefined });
  }

  function pickFile(id) {
    setFileID(id);
    actions.setPlanOptions({ version, file_ids: id ? [id] : undefined });
  }

  const deps = plan.dependencies ?? [];
  const missing = plan.missing_dependencies ?? [];
  const warnings = plan.dependency_warnings ?? [];
  const conflicts = plan.conflicts ?? [];

  return html`
    <div class="plan plan--install">
      <p class="plan__summary">
        Installing <span class="mono">${plan.mod.name}</span>
        <span class="mono">${version}</span>
        ${plan.replaces && html` (replacing the installed version)`}.
      </p>

      ${
        showPicker &&
        html`
          <section class="plan__section plan__section--picker">
            <label class="plan__control">
              Version
              <select
                name="install-version"
                value=${version}
                onChange=${(e) => pickVersion(e.currentTarget.value)}
              >
                ${versions.map(
                  (v) => html`<option key=${v} value=${v}>${v}</option>`,
                )}
              </select>
            </label>
            ${
              showFilePicker &&
              html`
                <label class="plan__control">
                  File
                  <select
                    name="install-file"
                    value=${fileID}
                    onChange=${(e) => pickFile(e.currentTarget.value)}
                  >
                    <option value="">Default</option>
                    ${filesForVersion.map(
                      (f) => html`
                        <option key=${f.id} value=${f.id}>
                          ${f.name || f.file_name}
                        </option>
                      `,
                    )}
                  </select>
                </label>
              `
            }
          </section>
        `
      }
      ${
        deps.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading">Dependencies (${deps.length})</p>
            <ul class="plan__mods">
              ${deps.map(
                (d) => html`<li key=${`${d.source_id}/${d.id}`}>${d.name}</li>`,
              )}
            </ul>
          </section>
        `
      }
      ${
        missing.length > 0 &&
        html`<p class="plan__note plan__note--warn">
          ${missing.length} dependenc${missing.length === 1 ? "y" : "ies"} could
          not be resolved.
        </p>`
      }
      ${
        plan.cycle_detected &&
        html`<p class="plan__note plan__note--warn">
          A circular dependency was detected; install order is best-effort.
        </p>`
      }
      ${
        warnings.length > 0 &&
        html`<p class="plan__note plan__note--warn">
          ${warnings.length} dependency
          lookup${warnings.length === 1 ? "" : "s"} failed.
        </p>`
      }
      ${
        conflicts.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading plan__heading--warn">
              Conflicts (${conflicts.length})
            </p>
            <ul class="plan__paths">
              ${conflicts.map(
                (c) =>
                  html`<li key=${c.relative_path} class="mono">
                    ${c.relative_path}
                  </li>`,
              )}
            </ul>
          </section>
        `
      }
    </div>
  `;
}
