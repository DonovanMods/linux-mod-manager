// planstoredfiles.js - a plan's mods whose stored files mix main files of
// more than one version (issue 662), as core lists them: `heals`, each a
// user's mod the run heals to one version (the version kept and every main
// file dropped), and `curator_mixed`, each collection mod that installs the
// curator's files exactly as the collection names them. A heal drops only
// copies of one file at other versions; a separate component at another
// version is kept and named. Worded as the
// CLI's preview words them (cmd/lmm/stored_files_heal.go).

import { html } from "../render.js";

/** fileName is a mixed-version file's name, else its id. */
function fileName(f) {
  return f.file_name || f.id;
}

/** modName is a mod's name, else its source:id. */
function modName(m) {
  return m.name || `${m.source_id}:${m.mod_id}`;
}

/** fileList is versions' files as "a.zip (1.0), b.zip (1.0)". */
function fileList(versions) {
  return (versions ?? [])
    .flatMap((v) => (v.files ?? []).map((f) => `${fileName(f)} (${v.version})`))
    .join(", ");
}

/** healDetail is one heal's "keeps version 2.0, drops a.zip (1.0); keeps
 * c.zip (1.0), a separate component" - the CLI's wording. */
export function healDetail(h) {
  const parts = [];
  if ((h.dropped ?? []).length > 0) {
    const keep = h.recorded_version
      ? `keeps the newest, ${h.version} (none is at the recorded ${h.recorded_version})`
      : `keeps version ${h.version}`;
    parts.push(`${keep}, drops ${fileList(h.dropped)}`);
  }
  if ((h.kept ?? []).length > 0) {
    parts.push(`keeps ${fileList(h.kept)}, a separate component`);
  }
  return parts.join("; ");
}

/** curatorDetail is one collection mod's "1.0 (a.zip); 1.1 (b.zip)". */
export function curatorDetail(c) {
  return (c.versions ?? [])
    .map((v) => `${v.version} (${(v.files ?? []).map(fileName).join(", ")})`)
    .join("; ");
}

/** PlanStoredFiles renders a plan's `heals` and `curator_mixed`, or nothing
 * when both are empty. testID prefixes each list's data-testid. */
export function PlanStoredFiles({ heals, curator, testID = "plan" }) {
  const healList = heals ?? [];
  const curatorList = curator ?? [];
  if (healList.length === 0 && curatorList.length === 0) return null;
  return html`
    ${
      healList.length > 0 &&
      html`
        <section class="plan__section">
          <h3 class="plan__heading">
            Stored files of more than one version (${healList.length})
          </h3>
          <p class="plan__note">
            These mods' stored main files are of more than one version. Of
            copies of one file, the one at the mod's version stays; a separate
            component, and every optional, update and miscellaneous file, stays
            too, and the profile records the files kept. To use a dropped copy
            instead, reinstall the mod at its version.
          </p>
          <ul class="plan__mods" data-testid=${`${testID}-heals`}>
            ${healList.map(
              (h) => html`
                <li key=${`${h.source_id}:${h.mod_id}`} class="plan__mod">
                  <span class="plan__mod-name">${modName(h)}</span>${" "}
                  <span class="plan__mod-detail">${healDetail(h)}</span>
                </li>
              `,
            )}
          </ul>
        </section>
      `
    }
    ${
      curatorList.length > 0 &&
      html`
        <section class="plan__section">
          <h3 class="plan__heading">
            Collection files of mixed versions (${curatorList.length})
          </h3>
          <p class="plan__note">
            The collection names these files, so they install as the curator
            chose them.
          </p>
          <ul class="plan__mods" data-testid=${`${testID}-curator-mixed`}>
            ${curatorList.map(
              (c) => html`
                <li key=${`${c.source_id}:${c.mod_id}`} class="plan__mod">
                  <span class="plan__mod-name">${modName(c)}</span>${" "}
                  <span class="plan__mod-detail">${curatorDetail(c)}</span>
                </li>
              `,
            )}
          </ul>
        </section>
      `
    }
  `;
}
