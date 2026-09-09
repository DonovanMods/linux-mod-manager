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
// one candidate there is. The VERSION select specifically is gated on more
// than one distinct version (M6, unit 5 fix wave): a pool of several files
// sharing one version has nothing for a version picker to decide, only the
// file picker beneath it.

import { html, useState } from "../render.js";
import { PlanAdvanced, PlanOption, ApplyOption } from "./planoptions.js";

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
export function InstallPlanView({ plan, modal, actions }) {
  const pool = plan.file_pool ?? [];
  const versions = distinctVersions(pool);
  // The version SELECT only earns its place once there is more than one
  // version to choose between (M6): a pool of several files at the SAME
  // version (the file sub-picker's own case, below) has nothing for it to
  // decide, and a one-option dropdown just above the file picker that
  // actually matters was confusing rather than informative.
  const showVersionPicker = versions.length > 1;
  const defaultVersion = plan.files?.[0]?.version ?? plan.mod.version;

  // Seeded from applyOptions, not merely from the plan (C-3): a PLAN-time
  // option (show_archived, no_deps) re-plans, which remounts this view with
  // a fresh plan document - and a picked version already written into
  // applyOptions would otherwise be silently replaced on screen by the
  // default while Confirm still submitted the pick.
  const [version, setVersion] = useState(
    modal?.applyOptions?.version ?? defaultVersion,
  );
  const filesForVersion = pool.filter((f) => f.version === version);
  const showFilePicker = filesForVersion.length > 1;
  const showPicker = showVersionPicker || showFilePicker;
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
        Installing <span class="mono">${plan.mod.name}</span>${" "}
        <span class="mono">${version}</span>
        ${
          // htm-ws-ok: the nested template already opens with its own
          // leading space when it renders.
          plan.replaces && html` (replacing the installed version)`
        }.
      </p>

      ${
        showPicker &&
        html`
          <section class="plan__section plan__section--picker">
            ${
              showVersionPicker &&
              html`
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
              `
            }
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
            <h3 class="plan__heading">Dependencies (${deps.length})</h3>
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
            <h3 class="plan__heading plan__heading--warn">
              Conflicts (${conflicts.length})
            </h3>
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

      <${PlanAdvanced}>
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="show_archived"
          label="Include archived files"
          hint="lmm install --show-archived. Widens the candidate pool above."
        />
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="no_deps"
          label="Skip dependencies"
          hint="lmm install --no-deps. The dependency lists above clear to match."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="skip_hooks"
          label="Skip hooks"
          hint="lmm --no-hooks."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="force"
          label="Force"
          hint="lmm install --force. Carry on past a failure that would otherwise stop the flow."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="skip_verify"
          label="Skip checksum recording"
          hint="lmm install --skip-verify. Apply-time only: nothing about the plan above changes."
        />
      <//>
    </div>
  `;
}
