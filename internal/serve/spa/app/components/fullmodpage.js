// fullmodpage.js - the full mod page at
// /g/{game}/{profile}/mod/{source}/{id}, the slide-over's "More info ->"
// (docs/plans/2026-08-31-serve-spa-design.md §Full mod page): full
// description, complete changelog, files table, versions table (install/
// rollback per version honoring lock rules), dependency info, per-mod job
// history. Back returns to home exactly as left - a plain link to
// contextPath, not history.back(), so the guarantee holds regardless of how
// this page was reached (the slide-over's "More info", a bookmark, a
// dependency cross-reference).
//
// Enable/Disable are wired here too (issue 330's own task brief names both
// surfaces explicitly), morphing the same way the slide-over's own does -
// a direct deep link into this page must be able to toggle the mod without
// a detour back through the slide-over. Uninstall/Update stay the slide-
// over's own (the design doc lists "key actions" under §Slide-over only);
// this page's own per-mod mutations are the versions table's Update-to/
// Rollback pair instead.
//
// HEADING LEVELS. Every one of this page's sections is an <h2> under the
// mod's own <h1> (MIN-3, the closing wave's gate review). They used to be
// a mix: Findings and Conflicts were h2 while Description, Changelog,
// Dependencies, Files, Versions and Job history were h3, because both
// classes had been mapped mechanically in I-3 (section-header -> h2,
// plan__heading -> h3) and this is the one page where both appear as
// SIBLINGS. The h3s came after the h2s, so heading navigation read them as
// children of "Conflicts". The class is unchanged and .plan__heading sets
// its own size and weight, so nothing about the page looks different.
//
// Unlike the slide-over, this page owns its own reads (main.js's
// hydrateModPage): core.ModFilesReport (primary, fatal on failure),
// core.ModDetail and the versions document (both optional - see that
// function's own doc comment for why the split runs this way round), none
// of which Mission Control's four documents carry. This is the design's
// "full path", the one-click-deeper surface that can afford what the
// slide-over's zero-fetch "quick path" cannot (§Why).
//
// VERSIONS TABLE SCOPE. core gives an installed mod exactly two ways to
// land on a version other than the one it already carries: ApplyRollback
// (hard-coded to InstalledMod.PreviousVersion, no other target) and the
// "updates" plan kind (the version CheckGameUpdates found, if any). There
// is no core primitive for "re-point this installed mod at an arbitrary
// THIRD version" - that would be a new core flow, not a thin wiring job,
// so the table shows every version the source reports but only offers an
// action on the two rows core can actually reach: rollback on
// PreviousVersion, update on the checked NewVersion. Every other row is
// informational. Both actions honor lock rules by being disabled (not
// hidden - the row still names why) whenever the mod is locked, mirroring
// what ApplyRollback/ApplyUpdate would refuse server-side anyway.

import { html, useEffect, useMemo, useState } from "../render.js";
import { contextPath } from "../router.js";
import { loadModJobHistory, candidateJobKey } from "../jobhistory.js";
import { mutationLabel, jobStateLabel } from "../progress.js";
import { InlineJob } from "./jobprogress.js";
import { AwayBar } from "./awaybar.js";
import { findingLabel } from "../verify.js";
import { displayVersion } from "../version.js";
import {
  ModSettingsControls,
  ManagedBySteam,
  findingsFor,
  conflictsFor,
} from "./modpanel.js";

/** BackLink is this page's one route out - always to Mission Control as it
 * stood, never the browser's own history stack. */
export function FullModPage({ state, route, onThemeChange, actions }) {
  const home = contextPath(route.game, route.profile);
  const modPage = state.modPage;
  const key = `${route.sourceID}/${route.modID}`;

  const header = html`
    <${AwayBar}
      state=${state}
      route=${route}
      home=${home}
      onThemeChange=${onThemeChange}
      actions=${actions}
    />
  `;

  // Loading until the PRIMARY read has landed one way or the other:
  // hydrateModPage's first write stamps key immediately (before the fetch
  // even starts, so a stale write from a mod the user has since navigated
  // away from can be fenced by key alone) but leaves filesReport null
  // until the fetch resolves - checking key alone here previously let a
  // still-loading modPage reach the filesReport.mod access below with
  // filesReport still null.
  if (
    !modPage ||
    modPage.key !== key ||
    (!modPage.filesReport && !modPage.error)
  ) {
    return html`${header}
      <main id="main" class="app-main">
        <p class="app-booting">Loading this mod…</p>
      </main>`;
  }

  if (modPage.error) {
    return html`
      ${header}
      <main id="main" class="app-main">
        <p class="app-error">${modPage.error}</p>
      </main>
    `;
  }

  // The INSTALLED record - core.ModFilesReport.Mod - is the primary,
  // always-present identity source (see hydrateModPage's own doc comment).
  // detail (core.ModDetail, optional) adds the live description/changelog
  // and, because ModDetail composes it alongside its own live GetMod call,
  // the lock/policy state the versions table gates on.
  const installedMod = modPage.filesReport.mod ?? {};
  const detailMod = modPage.detail?.mod;
  // core.ModDetail.description_text, NOT mod.description (issue 342): the
  // raw field carries the source's own markup by design (issue 86), and
  // Preact renders it as the text it is - the reader saw "<p>Adds bigger
  // backpacks.</p>", angle brackets and all. dangerouslySetInnerHTML is
  // forbidden here (no_unsafe_dom_test.go), and re-implementing core's
  // cleaner in a frontend would put core logic in an adapter, so core
  // hands over the cleaned prose and this splits its blank-line-separated
  // paragraphs into real <p>s.
  const descriptionParagraphs = splitParagraphs(
    modPage.detail?.description_text,
  );
  const installed = modPage.detail?.installed;
  const sourceID = route.sourceID;
  const modID = route.modID;
  const origin = (action) => `mod:${sourceID}/${modID}:${action}`;

  // The lock/policy pair reads the LIBRARY listing first and the live
  // ModDetail only as a fallback (I-5): core.ModListing carries locked,
  // locked_version and update_policy without asking the source anything,
  // so a mod whose source is offline still gets working controls - the same
  // degradation rule the identity/files half of this page already follows.
  const listing = (state.mods?.mods ?? []).find(
    (m) => m.source_id === sourceID && m.id === modID,
  );
  const settingsSource = listing ?? installed;
  const settingsRow = settingsSource && {
    source_id: sourceID,
    id: modID,
    locked: Boolean(settingsSource.locked),
    locked_version: settingsSource.locked_version,
    update_policy: settingsSource.update_policy,
    // issue 269: ModSettingsControls hides the "auto" policy on row.external,
    // which the slide-over gets for free from its modrows.js row. This page
    // builds its own literal, so the flag has to be carried across
    // explicitly - without it the two surfaces disagreed about the same mod,
    // and this one offered a policy SetModUpdatePolicy refuses server-side.
    external: Boolean(settingsSource.external),
  };

  const findings = findingsFor(state.health, modID);
  const conflicts = conflictsFor(state.conflicts, `${sourceID}:${modID}`);
  // issue 394: `lmm mod edit --source/--source-id` on a locked mod is
  // refused ("unlock with 'lmm mod unlock …' first"), so Re-link… below is
  // gated the way the Versions section's rollback button already was.
  //
  // Off settingsSource, not off `installed` (P2 review Minor 4): the lock
  // rule twenty lines above is that the LIBRARY listing answers first and
  // the live ModDetail is only the fallback, precisely so a mod whose
  // source is offline still gets working controls. Reading the ModDetail
  // half alone put Re-link… back on a locked mod in exactly the case the
  // rule is written for. The Versions section's two gates below take the
  // same value as props for the same reason.
  const lockedActions = Boolean(settingsSource?.locked);
  const lockedVersion = settingsSource?.locked_version;

  return html`
    ${header}
    <main id="main" class="app-main mod-page">
      <h1 class="mod-page__title">${installedMod.name}</h1>
      <p class="mod-page__meta">
        ${installedMod.author ? html`by ${installedMod.author} · ` : ""}
        <span class="mono">${sourceID}/${modID}</span>${" "}
        <span>·</span>${" "}
        <span class="mono"
          >${
            // version.js#displayVersion, issue 269's version DISPLAY rule.
            // The design names this page by hand - "the mod page show the
            // date and, beneath it, the manifest labelled as such" - and it
            // builds its meta line off core.ModFilesReport.Mod rather than a
            // library row, which is exactly why the rule is a function every
            // surface calls rather than a field one document carries.
            displayVersion(installedMod)
          }</span
        >${" "}installed ${installed?.locked && " · locked"}
      </p>

      ${
        installedMod.external &&
        html`<div class="mod-page__section">
          <${ManagedBySteam} row=${installedMod} />
        </div>`
      }
      ${
        settingsRow &&
        html`<${ModSettingsControls} row=${settingsRow} actions=${actions} />`
      }

      <div class="mod-page__section mod-page__actions">
        ${
          !installedMod.external &&
          html`<${InlineJob}
            origin=${origin("toggle")}
            state=${state}
            actions=${actions}
          >
            <button
              type="button"
              class="button"
              onClick=${() =>
                actions.startToggle({
                  action: installedMod.enabled ? "disable" : "enable",
                  sourceID,
                  modID,
                  origin: origin("toggle"),
                })}
            >
              ${installedMod.enabled ? "Disable" : "Enable"}
            </button>
          <//>`
        }
        <${InlineJob}
          origin=${origin("uninstall")}
          state=${state}
          actions=${actions}
        >
          <button
            type="button"
            class="button button--danger"
            data-action="uninstall"
            onClick=${() =>
              actions.openPlan({
                kind: "uninstall",
                origin: origin("uninstall"),
                title: installedMod.external
                  ? `Stop tracking ${installedMod.name}`
                  : `Uninstall ${installedMod.name}`,
                confirmLabel: installedMod.external
                  ? "Stop tracking"
                  : "Uninstall",
                options: { source_id: sourceID, mod_id: modID },
              })}
          >
            ${installedMod.external ? "Stop tracking" : "Uninstall"}
          </button>
        <//>
        ${
          !installedMod.external &&
          html`<button
            type="button"
            class="button"
            data-action="relink"
            disabled=${lockedActions}
            title=${lockedActions ? "Unlock this mod to re-link it" : undefined}
            onClick=${() =>
              actions.openPlan({
                kind: "mod_relink",
                origin: origin("relink"),
                title: `Re-link ${installedMod.name}`,
                confirmLabel: "Re-link",
                options: { mod_id: modID, source_id: sourceID },
              })}
          >
            Re-link…
          </button>`
        }
      </div>
      ${
        // issue 394's own aside: a title on a DISABLED button is invisible
        // to a keyboard user, because a disabled button is not focusable.
        // The remedy therefore also gets a line of its own, which is the
        // only form of it that reader can reach. Rendered only when
        // something above it is actually refused.
        lockedActions &&
        html`<p
          class="mod-page__hint empty-state__hint"
          data-testid="mod-page-locked-actions"
        >
          This mod is locked${lockedVersion ? ` to ${lockedVersion}` : ""}.
          Unlock it (<span class="mono"
            >lmm mod unlock ${sourceID}:${modID}</span
          >) to re-link it.
        </p>`
      }
      ${
        findings.length > 0 &&
        html`
          <section class="mod-page__section" data-testid="mod-page-findings">
            <h2 class="plan__heading">Findings (${findings.length})</h2>
            <ul class="plan__paths">
              ${findings.map(
                (f) =>
                  html`<li key=${f.file_id ?? f.status}>
                    ${findingLabel(f)}
                  </li>`,
              )}
            </ul>
          </section>
        `
      }
      ${
        conflicts.length > 0 &&
        html`
          <section class="mod-page__section" data-testid="mod-page-conflicts">
            <h2 class="plan__heading">Conflicts (${conflicts.length})</h2>
            <ul class="plan__paths">
              ${conflicts.map(
                (c) =>
                  html`<li key=${c.path} class="mono">
                    ${c.path}
                    ${
                      c.load_order_winner.key === `${sourceID}:${modID}`
                        ? " (wins)"
                        : ` (loses to ${c.load_order_winner.name})`
                    }
                  </li>`,
              )}
            </ul>
          </section>
        `
      }
      ${
        modPage.detailError &&
        html`<p class="empty-state__hint">
          Couldn't reach the source for the live description, changelog or lock
          state: ${modPage.detailError}.
          <button
            type="button"
            class="button button--small"
            onClick=${actions.reloadModDetail}
          >
            Retry
          </button>
        </p>`
      }
      ${
        descriptionParagraphs.length > 0 &&
        html`
          <section class="mod-page__section">
            <h2 class="plan__heading">Description</h2>
            ${descriptionParagraphs.map(
              (para) => html`<p class="mod-page__prose">${para}</p>`,
            )}
          </section>
        `
      }
      ${
        modPage.detail &&
        html`
          <section class="mod-page__section">
            <h2 class="plan__heading">Changelog</h2>
            ${
              modPage.detail.changelog
                ? html`<p class="mod-page__prose">
                    ${modPage.detail.changelog}
                  </p>`
                : html`<p class="empty-state__hint">No changelog available.</p>`
            }
          </section>
        `
      }
      ${
        (detailMod?.dependencies ?? []).length > 0 &&
        html`
          <section class="mod-page__section">
            <h2 class="plan__heading">
              Dependencies (${detailMod.dependencies.length})
            </h2>
            <ul class="plan__paths">
              ${detailMod.dependencies.map(
                (d) =>
                  html`<li key=${`${d.source_id}:${d.mod_id}`} class="mono">
                    ${d.source_id}:${d.mod_id}${d.version ? ` @${d.version}` : ""}
                  </li>`,
              )}
            </ul>
          </section>
        `
      }

      <${FilesSection} filesReport=${modPage.filesReport} />
      <${VersionsSection}
        modPage=${modPage}
        installed=${installed}
        locked=${lockedActions}
        lockedVersion=${lockedVersion}
        state=${state}
        actions=${actions}
        origin=${origin}
        sourceID=${sourceID}
        modID=${modID}
      />
      <${JobHistorySection}
        state=${state}
        sourceID=${sourceID}
        modID=${modID}
      />
    </main>
  `;
}

/** FilesSection renders core.ModFilesReport's file table - part of the
 * page's PRIMARY read (hydrateModPage), so unlike every other section here
 * it has no loading/error state of its own: by the time this renders,
 * filesReport already exists. */
// splitParagraphs turns core's cleaned description text into paragraphs:
// blank-line-separated runs, trimmed, with empty runs dropped.
// CleanChangelog turns each </p><p> pair into two newlines and a <br> into
// one, so a source that uses both leaves runs of three - rendering those as
// literal blank lines (the pre-wrap the prose class carries) would put a
// gap in the page where the source only meant a paragraph break.
function splitParagraphs(text) {
  if (!text) return [];
  return text
    .split(/\n\s*\n/)
    .map((para) => para.trim())
    .filter(Boolean);
}

function FilesSection({ filesReport }) {
  return html`
    <section class="mod-page__section">
      <h2 class="plan__heading">Files</h2>
      ${
        filesReport.merged_pak_only
          ? html`<p class="empty-state__hint">
              This mod owns no files of its own - it rides the profile's merged
              artifact.
            </p>`
          : (filesReport.files ?? []).length === 0
            ? html`<p class="empty-state__hint">
                No deployed files tracked. (Files are tracked on install;
                existing mods may need to be redeployed.)
              </p>`
            : html`
                <table class="mod-page__table">
                  <thead>
                    <tr>
                      <th>Path</th>
                      <th>Size</th>
                      <th>Deployed</th>
                    </tr>
                  </thead>
                  <tbody>
                    ${filesReport.files.map(
                      (f) =>
                        html`<tr key=${f.path}>
                          <td class="mono">${f.path}</td>
                          <td class="mono">${formatBytes(f.size)}</td>
                          <td>${f.deployed ? "yes" : "no"}</td>
                        </tr>`,
                    )}
                  </tbody>
                </table>
              `
      }
    </section>
  `;
}

/** formatBytes renders a plain byte count for the files table - a small,
 * local copy of progress.js's own formatter would be one more shared
 * import for a single call site; kept inline on purpose. */
function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`;
}

/** VersionsSection renders the versions document (a per-file version TABLE,
 * which not every source can supply) plus the Rollback action, which
 * always renders regardless: it is a core.RollbackPlan flow, unrelated to
 * whether THIS source happens to implement per-file version reporting, so
 * it must not be gated behind that capability the way the table itself is.
 *
 * The rollback button IS conditioned on "has a previous version" (M1):
 * core.ModDetail's own InstalledDetail carries no PreviousVersion field,
 * but the page's PRIMARY read does - ModFilesReport.Mod is a full
 * domain.InstalledMod (internal/core/mod_files.go), whose wire form
 * carries previous_version whenever ApplyRollback would actually have
 * somewhere to land. A mod with nothing to roll back to shows no control
 * at all, rather than a fully clickable button whose plan then fails with
 * PlanRollback's own honest "no previous version available" error.
 */
function VersionsSection({
  modPage,
  installed,
  locked,
  lockedVersion,
  state,
  actions,
  origin,
  sourceID,
  modID,
}) {
  // issue 269: lmm never held a previous copy of a Steam Workshop item, so
  // there is nothing to roll back TO - the control is absent, not disabled.
  const external = Boolean(modPage.filesReport.mod?.external);
  const hasPrevious =
    !external && Boolean(modPage.filesReport.mod?.previous_version);

  return html`
    <section class="mod-page__section">
      <h2 class="plan__heading">Versions</h2>
      <${VersionsTable}
        modPage=${modPage}
        installed=${installed}
        locked=${locked}
        state=${state}
        actions=${actions}
        origin=${origin}
        sourceID=${sourceID}
        modID=${modID}
      />
      ${
        hasPrevious &&
        html`<${InlineJob}
          origin=${origin("rollback")}
          state=${state}
          actions=${actions}
        >
          <button
            type="button"
            class="button"
            disabled=${locked}
            title=${locked ? "Unlock this mod to roll it back" : undefined}
            onClick=${() =>
              actions.openPlan({
                kind: "rollback",
                origin: origin("rollback"),
                title: "Roll back to the previous version",
                confirmLabel: "Roll back",
                options: { source_id: sourceID, mod_id: modID },
              })}
          >
            Roll back to the previous version
          </button>
        <//>`
      }
      ${
        // The same visible remedy the actions group above carries, for the
        // same reason (issue 394's aside): the title on this disabled
        // button reaches a mouse and nothing else.
        hasPrevious &&
        locked &&
        html`<p
          class="mod-page__hint empty-state__hint"
          data-testid="mod-page-locked-rollback"
        >
          Locked${lockedVersion ? ` to ${lockedVersion}` : ""} — unlock it
          (<span class="mono">lmm mod unlock ${sourceID}:${modID}</span>) to
          roll back.
        </p>`
      }
    </section>
  `;
}

/** VersionsTable renders just the per-file version list - its own three
 * fetch states, separated from the always-present Rollback action above. */
function VersionsTable({
  modPage,
  installed,
  locked,
  state,
  actions,
  origin,
  sourceID,
  modID,
}) {
  if (modPage.versionsError) {
    return html`
      <div class="empty-state empty-state--error">
        <p>Couldn't load versions: ${modPage.versionsError}</p>
        <button
          type="button"
          class="button button--small"
          onClick=${actions.reloadModVersions}
        >
          Retry
        </button>
      </div>
    `;
  }
  if (modPage.versions === null) {
    return html`<p class="app-booting">Loading versions…</p>`;
  }
  if (!modPage.versions.supported) {
    return html`<p class="empty-state__hint">
      This source does not report per-file versions.
    </p>`;
  }

  // The checked update target - the ONE version CheckGameUpdates actually
  // found for this mod, if any (C1: core has no primitive for landing an
  // installed mod on any OTHER non-installed version - see this file's own
  // header comment). Joined from /api/v1/updates (main.js's hydrateModPage)
  // rather than carried on ModDetail, which has no such field.
  const updateTarget = (modPage.updates?.updates ?? []).find(
    (u) =>
      u.installed_mod?.source_id === sourceID && u.installed_mod?.id === modID,
  )?.new_version;

  return html`
    <table class="mod-page__table">
      <thead>
        <tr>
          <th>Version</th>
          <th>State</th>
          <th>Action</th>
        </tr>
      </thead>
      <tbody>
        ${modPage.versions.versions.map((v) => {
          const isInstalled = v === installed?.version;
          const isUpdateTarget = !isInstalled && v === updateTarget;
          // origin(`update:${v}`) rather than the plain origin("update")
          // every other control here uses: this table can show several
          // non-installed rows at once, each with its own button, and a
          // shared origin would morph EVERY row's button in lockstep the
          // instant any one of them started a job. This is the one origin
          // on this page modrows.js#runningMutations' "action" pattern
          // never has to parse - that helper only reads origins from the
          // HOME route's library rows, never from this page.
          return html`
            <tr key=${v}>
              <td class="mono">${v}</td>
              ${
                /* M-6: an em dash rather than an empty cell. A blank
                under a "State" header reads as a rendering failure; the
                state of a version that is neither installed nor the checked
                update target is genuinely "nothing to say", and the table
                should say so. */ ""
              }
              <td>
                ${isInstalled ? "installed" : isUpdateTarget ? "available" : "—"}
              </td>
              <td>
                ${
                  isUpdateTarget
                    ? html`<${InlineJob}
                        origin=${origin(`update:${v}`)}
                        state=${state}
                        actions=${actions}
                      >
                        <button
                          type="button"
                          class="button button--small"
                          disabled=${locked}
                          title=${locked ? "Unlock this mod to change its version" : undefined}
                          onClick=${() =>
                            actions.openPlan({
                              kind: "updates",
                              origin: origin(`update:${v}`),
                              title: `Update to ${v}`,
                              confirmLabel: "Update",
                              options: { mods: [`${sourceID}:${modID}`] },
                            })}
                        >
                          Update to ${v}
                        </button>
                      <//>`
                    : "—"
                }
              </td>
            </tr>
          `;
        })}
      </tbody>
    </table>
  `;
}

/** JobHistorySection lists the finished jobs jobhistory.js can attribute to
 * this mod - see that file's own header comment for exactly which kinds
 * that covers today. */
function JobHistorySection({ state, sourceID, modID }) {
  const [history, setHistory] = useState({ status: "loading", jobs: [] });

  // issue 330 carry-4 (unit 6): depend on the CANDIDATE set, not jobsIndex's
  // own reference - see jobhistory.js#candidateJobKey's own doc comment.
  const candidateKey = useMemo(
    () => candidateJobKey(state.jobsIndex),
    [state.jobsIndex],
  );

  useEffect(() => {
    let cancelled = false;
    setHistory({ status: "loading", jobs: [] });
    loadModJobHistory(state.jobsIndex, sourceID, modID).then((jobs) => {
      if (!cancelled) setHistory({ status: "ready", jobs });
    });
    return () => {
      cancelled = true;
    };
  }, [candidateKey, sourceID, modID]);

  return html`
    <section class="mod-page__section">
      <h2 class="plan__heading">Job history</h2>
      ${
        history.status === "loading"
          ? html`<p class="app-booting">Loading job history…</p>`
          : history.jobs.length === 0
            ? html`<p class="empty-state__hint">
                No update or rollback jobs recorded for this mod yet.
              </p>`
            : html`
                <ul class="plan__paths">
                  ${history.jobs.map(
                    (job) =>
                      html`<li key=${job.id}>
                        ${mutationLabel(job.kind)} — ${jobStateLabel(job)}
                        ${job.ended_at ? ` (${new Date(job.ended_at).toLocaleString()})` : ""}
                      </li>`,
                  )}
                </ul>
              `
      }
    </section>
  `;
}
