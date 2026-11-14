// plan_install.js - the confirm modal's renderer for a core.InstallPlan
// (issue 331, kind_install.go), the first renderer in this application that is
// more than a read-only preview: install is the one mutation whose plan
// document offers something to CHOOSE (issue 225's version/file selection), not
// merely something to read before confirming.
//
// The version ▾ groups InstallPlan.FilePool by each file's own Version -
// the candidate-pool disclosure kind_install.go's own doc comment promises
// (issue 331 carry-in) - and a FILE picker appears beside it only when the
// chosen version itself resolves to more than one file (a main file and its
// patch, a merge-capable source's alternate-format files, say). The file
// picker is checkboxes (issue 637): several files of one mod install in one
// job, as `lmm install --file a,b` does, each FOMOD installer among them
// asked about in turn (fomodwizard.js labels each page with its file). The
// default file starts checked; unchecking every file holds Confirm
// (confirmplan.js) rather than falling back to the default. The picker
// offers the chosen version's files only, as `--version` with `--file`
// does; a pick core refuses (issue 211's alternate forms of one mod) fails
// the job with core's own message, the one the CLI prints. A selection is
// written into the
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
// defaults already resolve to the exact file Files names. Once touched, the
// picker always sends the checked files by id, so what it shows checked is
// exactly what Confirm installs.
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

import { deployedKey, deployedPath } from "../deploypath.js";
import { html, useState } from "../render.js";
import { PlanAdvanced, PlanOption, ApplyOption } from "./planoptions.js";
import { absoluteTime, updatedPhrase } from "../relativetime.js";

/** fileOptionLabel is one file entry of the File picker. */
function fileOptionLabel(f) {
  return withUpdated(f.name || f.file_name, f.uploaded_at);
}

/** withUpdated appends a picker entry's date (issue 433) - the file's own
 * uploaded_at, where the source reports one - as ONE string: an <option>
 * holds text only, so the age rides in the label and the exact moment in
 * its title. An undated entry reads exactly as before. */
function withUpdated(label, uploadedAt) {
  const updated = updatedPhrase(uploadedAt);
  return updated ? `${label} · ${updated}` : label;
}

/** fileHint is a file's archive name beside its display name, when the two
 * differ - the CLI's displayFileLabel shows both. */
function fileHint(f) {
  if (!f.name || !f.file_name || f.name === f.file_name) return null;
  return html`${" "}<span class="mono plan__option-hint">${f.file_name}</span>`;
}

/** filesAt is the pool's files at version v. */
function filesAt(pool, v) {
  return pool.filter((f) => f.version === v);
}

/** defaultFileIDs is what an install of version v picks with no file named:
 * the plan's own Files at the plan's version (PlanInstall's pick, which
 * ApplyInstall installs verbatim), else that version's primary file, else
 * its first - core's primaryFile rule. */
function defaultFileIDs(plan, pool, v, planVersion) {
  const atVersion = filesAt(pool, v);
  if (v === planVersion) {
    const planned = (plan.files ?? [])
      .map((f) => f.id)
      .filter((id) => atVersion.some((f) => f.id === id));
    if (planned.length > 0) return planned;
  }
  const pick = atVersion.find((f) => f.is_primary) ?? atVersion[0];
  return pick ? [pick.id] : [];
}

/** versionUploadedAt is a version's date: the first pool file carrying
 * that version and a date. */
function versionUploadedAt(pool, v) {
  return pool.find((f) => f.version === v && f.uploaded_at)?.uploaded_at;
}

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
  const filesForVersion = filesAt(pool, version);
  const showFilePicker = filesForVersion.length > 1;
  const showPicker = showVersionPicker || showFilePicker;
  // The checked files (issue 637). Seeded from applyOptions for the same
  // re-plan reason as version above, else from what an untouched picker
  // installs: plan.files at the plan's own version (PlanInstall's default
  // pick, which ApplyInstall installs as is), the primary-or-first file at
  // any other.
  const [fileIDs, setFileIDs] = useState(
    () =>
      modal?.applyOptions?.file_ids ??
      defaultFileIDs(plan, pool, version, defaultVersion),
  );
  const selectedFiles = filesForVersion.filter((f) => fileIDs.includes(f.id));
  // The files this install names, for the preview: the checked ones when
  // there is a picker, else the plan's own pick.
  const installFiles = showFilePicker
    ? selectedFiles
    : filesForVersion.length > 0
      ? filesForVersion
      : (plan.files ?? []);

  /** pickVersion applies a version change: local display state, AND the
   * confirm-plan framework's own applyOptions - fired together, from the
   * event handler, never from an effect (see this file's header comment).
   * The version's default file is sent by id, so what the picker shows
   * checked is exactly what Confirm installs. */
  function pickVersion(v) {
    const ids = defaultFileIDs(plan, pool, v, defaultVersion);
    setVersion(v);
    setFileIDs(ids);
    actions.setPlanOptions({ version: v, file_ids: ids });
  }

  /** toggleFile checks or unchecks one file, keeping the pool's order (the
   * order `lmm install --file` would be given them in). An empty selection
   * is kept as an empty list: confirmplan.js holds Confirm until a file is
   * checked, rather than letting "nothing" fall back to the default. */
  function toggleFile(id, checked) {
    const next = filesForVersion
      .map((f) => f.id)
      .filter((fid) => (fid === id ? checked : fileIDs.includes(fid)));
    setFileIDs(next);
    actions.setPlanOptions({ version, file_ids: next });
  }

  const deps = plan.dependencies ?? [];
  const missing = plan.missing_dependencies ?? [];
  const warnings = plan.dependency_warnings ?? [];
  // Issue 448 (core issue 431): dependencies the profile marks off that this
  // install switches back on - the user did not name them, so they are
  // said out loud before Confirm, as the CLI does. Identity only; the name
  // is on the matching dependencies entry.
  const reenabled = plan.reenabled_dependencies ?? [];
  const depName = (ref) =>
    deps.find((d) => d.source_id === ref.source_id && d.id === ref.mod_id)
      ?.name || `${ref.source_id}:${ref.mod_id}`;
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
                    ${versions.map((v) => {
                      const at = versionUploadedAt(pool, v);
                      return html`<option
                        key=${v}
                        value=${v}
                        title=${absoluteTime(at) || undefined}
                      >
                        ${withUpdated(v, at)}
                      </option>`;
                    })}
                  </select>
                </label>
              `
            }
            ${
              showFilePicker &&
              html`
                <fieldset
                  class="plan__control plan__files-picker"
                  data-testid="install-files-picker"
                >
                  <legend class="plan__files-legend">
                    Files
                    <span class="plan__option-hint"
                      >Check each file to install, like lmm install --file
                      a,b.</span
                    >
                  </legend>
                  ${filesForVersion.map(
                    (f) => html`
                      <label
                        class="plan__option"
                        key=${f.id}
                        title=${absoluteTime(f.uploaded_at) || undefined}
                      >
                        <input
                          type="checkbox"
                          name="install-file"
                          value=${f.id}
                          checked=${fileIDs.includes(f.id)}
                          disabled=${modal?.status !== "ready"}
                          onChange=${(e) =>
                            toggleFile(f.id, e.currentTarget.checked)}
                        />
                        <span class="plan__option-label">
                          ${fileOptionLabel(f)} ${fileHint(f)}
                        </span>
                      </label>
                    `,
                  )}
                </fieldset>
              `
            }
          </section>
        `
      }
      ${
        showFilePicker && selectedFiles.length === 0
          ? html`<p
              class="plan__note plan__note--warn"
              data-testid="install-files-none"
              role="status"
            >
              Select at least one file to install.
            </p>`
          : installFiles.length > 0 &&
            html`
              <section class="plan__section" data-testid="install-files">
                <h3 class="plan__heading">
                  ${
                    installFiles.length === 1
                      ? "File"
                      : `Files (${installFiles.length})`
                  }
                </h3>
                <ul class="plan__mods">
                  ${installFiles.map(
                    (f) =>
                      html`<li key=${f.id}>
                        ${f.name || f.file_name}${fileHint(f)}
                      </li>`,
                  )}
                </ul>
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
        reenabled.length > 0 &&
        html`
          <section class="plan__section" data-testid="install-reenabled">
            <h3 class="plan__heading plan__heading--warn">
              Switched back on (${reenabled.length})
            </h3>
            <p class="plan__note plan__note--warn">
              ${
                // One string, for htm's whitespace rule
                // (cards.js#conflictLabel).
                reenabled.length === 1
                  ? `Profile ${plan.profile} has this dependency switched off. ${plan.mod?.name ?? "This mod"} can't work without it, so installing turns it back on.`
                  : `Profile ${plan.profile} has these dependencies switched off. ${plan.mod?.name ?? "This mod"} can't work without them, so installing turns them back on.`
              }
            </p>
            <ul class="plan__mods">
              ${reenabled.map(
                (r) =>
                  html`<li key=${`${r.source_id}/${r.mod_id}`}>
                    ${depName(r)}
                  </li>`,
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
                  html`<li
                    key=${deployedKey(c.relative_path, c.root)}
                    class="mono"
                  >
                    ${deployedPath(c.relative_path, c.root)}
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
          hint="lmm install --force: install without conflict prompts. Bypasses the conflict refusal outright and overwrites without the Overwrite round-trip - not just carrying on past an ordinary failure."
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
