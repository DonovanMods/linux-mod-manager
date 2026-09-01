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

import { html, useEffect, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { loadModJobHistory } from "../jobhistory.js";
import { mutationLabel, jobStateLabel } from "../progress.js";
import { InlineJob } from "./jobprogress.js";

/** BackLink is this page's one route out - always to Mission Control as it
 * stood, never the browser's own history stack. */
function BackLink({ to }) {
  return html`<a
    class="mod-page__back"
    href=${to}
    onClick=${(e) => {
      e.preventDefault();
      navigate(to);
    }}
    >← Back to library</a
  >`;
}

export function FullModPage({ state, route, onThemeChange, actions }) {
  const home = contextPath(route.game, route.profile);
  const modPage = state.modPage;
  const key = `${route.sourceID}/${route.modID}`;

  const header = html`
    <header class="app-bar">
      <span class="app-bar__brand">LMM</span>
      <${BackLink} to=${home} />
      <button
        type="button"
        class="theme-toggle"
        onClick=${() => onThemeChange(cycleTheme())}
      >
        Theme: ${currentTheme()}
      </button>
    </header>
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
      <main class="app-main"><p class="app-booting">Loading&#8230;</p></main>`;
  }

  if (modPage.error) {
    return html`
      ${header}
      <main class="app-main">
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
  const installed = modPage.detail?.installed;
  const sourceID = route.sourceID;
  const modID = route.modID;
  const origin = (action) => `mod:${sourceID}/${modID}:${action}`;

  return html`
    ${header}
    <main class="app-main mod-page">
      <h1 class="mod-page__title">${installedMod.name}</h1>
      <p class="mod-page__meta">
        ${installedMod.author ? html`by ${installedMod.author} · ` : ""}
        <span class="mono">${sourceID}/${modID}</span>
        · <span class="mono">${installedMod.version}</span> installed
        ${installed?.locked && " · locked"}
      </p>

      <div class="mod-page__section">
        <${InlineJob}
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
        <//>
      </div>

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
        detailMod?.description &&
        html`
          <section class="mod-page__section">
            <p class="plan__heading">Description</p>
            <p class="mod-page__prose">${detailMod.description}</p>
          </section>
        `
      }
      ${
        modPage.detail &&
        html`
          <section class="mod-page__section">
            <p class="plan__heading">Changelog</p>
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
            <p class="plan__heading">
              Dependencies (${detailMod.dependencies.length})
            </p>
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
function FilesSection({ filesReport }) {
  return html`
    <section class="mod-page__section">
      <p class="plan__heading">Files</p>
      ${
        filesReport.merged_pak_only
          ? html`<p class="empty-state__hint">
              This mod owns no files of its own - it rides the profile's merged
              artifact.
            </p>`
          : (filesReport.files ?? []).length === 0
            ? html`<p class="empty-state__hint">No files recorded.</p>`
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
  state,
  actions,
  origin,
  sourceID,
  modID,
}) {
  const locked = Boolean(installed?.locked);
  const hasPrevious = Boolean(modPage.filesReport.mod?.previous_version);

  return html`
    <section class="mod-page__section">
      <p class="plan__heading">Versions</p>
      <${VersionsTable}
        modPage=${modPage}
        installed=${installed}
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
    </section>
  `;
}

/** VersionsTable renders just the per-file version list - its own three
 * fetch states, separated from the always-present Rollback action above. */
function VersionsTable({
  modPage,
  installed,
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
    return html`<p class="app-booting">Loading versions&#8230;</p>`;
  }
  if (!modPage.versions.supported) {
    return html`<p class="empty-state__hint">
      This source does not report per-file versions.
    </p>`;
  }

  const locked = Boolean(installed?.locked);
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
          <th></th>
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
              <td>${isInstalled ? "installed" : ""}</td>
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
                    : ""
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

  useEffect(() => {
    let cancelled = false;
    setHistory({ status: "loading", jobs: [] });
    loadModJobHistory(state.jobsIndex, sourceID, modID).then((jobs) => {
      if (!cancelled) setHistory({ status: "ready", jobs });
    });
    return () => {
      cancelled = true;
    };
  }, [state.jobsIndex, sourceID, modID]);

  return html`
    <section class="mod-page__section">
      <p class="plan__heading">Job history</p>
      ${
        history.status === "loading"
          ? html`<p class="app-booting">Loading job history&#8230;</p>`
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
