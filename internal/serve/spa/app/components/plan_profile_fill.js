// plan_profile_fill.js - the confirm modal's renderer for a
// core.ProfileFillPlan (kind_profile_fill.go, `lmm profile fill`, issue
// 608): which pending mods the uploaded files fill, which stay pending and
// why, and which files were not used - the files the drop zone did not
// upload at all (no pending file has their size) included, since the user
// dropped them and should hear what became of each.

import { html } from "../render.js";

const pendingReason = {
  no_archive: "not among these files",
  partial: "only some of its files are here",
  no_checksum: "the collection records no checksum for its file",
  disabled: "switched off in the profile",
};

const ignoredReason = {
  no_match: "matches no pending file",
  checksum_mismatch: "the size of a pending file, but not its checksum",
  duplicate: "a copy of a file already matched",
  hash_limit: "not read: too many files of this size did not match",
  symlink: "a link, which is never followed",
  not_regular_file: "not a regular file",
  unreadable: "could not be read",
};

// countReason words a counted reason (plan.ignored_counts).
const countReason = {
  no_match: "no pending file has their size",
  symlink: "links, which are never followed",
  not_regular_file: "not regular files",
  unreadable: "could not be read",
};

/** modName is a fill entry's name, or its source:mod when the collection
 * gave none. */
function modName(m) {
  return m.name || `${m.source_id}:${m.mod_id}`;
}

/** ProfileFillPlanView renders core.ProfileFillPlan. */
export function ProfileFillPlanView({ plan, modal }) {
  const notUploaded = modal?.extra?.notUploaded ?? [];
  const later = modal?.extra?.later ?? [];
  const tooLarge = modal?.extra?.tooLarge ?? [];
  const matched = plan.matched ?? [];
  const pending = plan.pending ?? [];
  const ignored = plan.ignored ?? [];
  // The rest of the folder is counted, never named (core.FillIgnoredCount).
  const counts = plan.ignored_counts ?? [];
  return html`
    <div class="plan plan--profile-fill" data-testid="fill-plan">
      <p class="plan__summary">
        ${
          matched.length > 0
            ? html`${matched.length} pending mod(s) of${" "}
                <span class="mono">${plan.profile}</span> are among these files,
                and install as if downloaded: with the installer choices and mod
                type the collection records.`
            : html`None of these files is a pending download of${" "}
                <span class="mono">${plan.profile}</span>.`
        }
        ${
          plan.recorded_only &&
          html` ${plan.profile} is not the active profile, so they are recorded
          in it and deployed when you switch to it.`
        }
      </p>
      ${(plan.notes ?? []).map(
        (n) => html`<p key=${n} class="plan__note plan__note--warn">${n}</p>`,
      )}
      ${
        matched.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">To install (${matched.length})</h3>
          <ul class="plan__mods" data-testid="fill-matched">
            ${matched.map(
              (m) =>
                html`<li key=${`${m.source_id}:${m.mod_id}`}>
                  ${modName(m)} ←${" "}
                  <span class="mono"
                    >${(m.files ?? []).map((f) => f.archive).join(", ")}</span
                  >
                  ${m.mod_type && html` (type ${m.mod_type})`}
                  ${m.installer && " (recorded installer choices)"}
                  ${m.auto_update && " (updates automatically)"}
                </li>`,
            )}
          </ul>
        </section>`
      }
      ${
        pending.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">Still pending (${pending.length})</h3>
          <ul class="plan__mods" data-testid="fill-pending">
            ${pending.map(
              (p) =>
                html`<li key=${`${p.source_id}:${p.mod_id}`}>
                  ${modName(p)}: ${pendingReason[p.reason] ?? p.reason}
                </li>`,
            )}
          </ul>
        </section>`
      }
      ${
        ignored.length + notUploaded.length + tooLarge.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">
            Not used (${ignored.length + notUploaded.length + tooLarge.length})
          </h3>
          <ul class="plan__paths" data-testid="fill-ignored">
            ${ignored.map(
              (f, i) =>
                html`<li key=${`s${i}`}>
                  <span class="mono">${f.name}</span>:${" "}
                  ${ignoredReason[f.reason] ?? f.reason}
                </li>`,
            )}
            ${tooLarge.map(
              (name, i) =>
                html`<li key=${`l${i}`}>
                  <span class="mono">${name}</span>: too large to upload (over 2
                  GiB) - choose the folder it is in instead
                </li>`,
            )}
            ${notUploaded.map(
              (name, i) =>
                html`<li key=${`c${i}`}>
                  <span class="mono">${name}</span>: not uploaded - no pending
                  file has its size
                </li>`,
            )}
          </ul>
        </section>`
      }
      ${
        counts.length > 0 &&
        html`<ul class="plan__mods" data-testid="fill-ignored-counts">
          ${counts.map(
            (c) =>
              html`<li key=${c.reason}>
                ${c.count} other ${c.count === 1 ? "entry" : "entries"}:${" "}
                ${countReason[c.reason] ?? c.reason}
              </li>`,
          )}
        </ul>`
      }
      ${
        later.length > 0 &&
        html`<p class="plan__note" data-testid="fill-later">
          ${later.length} more file(s) are the size of a pending download and
          were not uploaded yet: up to four go at a time, so drop them again
          once these are installed.
        </p>`
      }
    </div>
  `;
}
