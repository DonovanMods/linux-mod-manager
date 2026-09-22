// purgeresult.js - the parts of a completed core.PurgeResult a person needs
// after the job has finished. The plan says what lmm expects to do; this
// component says what it actually left in place or removed after rechecking
// the live tree.

import { html } from "../render.js";

/**
 * PurgeResultDetails renders the additive result fields that are otherwise
 * invisible behind a successful purge job: files kept in place and files
 * removed from an earlier mod_path (issues 478, 451, and 466).
 */
export function PurgeResultDetails({ result }) {
  const kept = Array.isArray(result?.kept) ? result.kept : [];
  const removedPaths = Number.isSafeInteger(result?.removed_paths)
    ? result.removed_paths
    : 0;
  if (kept.length === 0 && removedPaths < 1) return null;

  return html`
    <div class="job-progress__explainer" data-testid="purge-job-result">
      ${
        removedPaths > 0 &&
        html`<p>
          ${`Removed ${removedPaths} file${removedPaths === 1 ? "" : "s"} deployed under an earlier mod_path.`}
        </p>`
      }
      ${
        kept.length > 0 &&
        html`<ul class="tray__skips" data-testid="purge-job-kept">
          ${kept.map(
            (path, i) =>
              html`<li key=${`${path.path ?? "kept"}:${i}`} class="tray__skip">
                ${keptResultText(path)}
              </li>`,
          )}
        </ul>`
      }
    </div>
  `;
}

// keptResultText intentionally matches cmd/lmm/purge.go's completion words:
// a user-modified file is no longer tracked, while any other kept path names
// the profile or game that still claims it. A stale mod_path is part of the
// location, not a side note, because that is the directory the user must
// inspect after a manual path move.
function keptResultText(k) {
  const path = keptPath(k);
  if (k?.reason === "user_file") {
    const note =
      typeof k.note === "string" && k.note !== "" ? ` (${k.note})` : "";
    return `Kept your file; lmm no longer tracks it${note}: ${path}`;
  }
  return `Left in place (${keptReason(k)}): ${path}`;
}

function keptReason(k) {
  const names = (value) =>
    Array.isArray(value) ? value.filter(Boolean).join(", ") : "";
  switch (k?.reason) {
    case "listed":
      return `its mod is in the active profile ${names(k.profiles)}`;
    case "other_game":
      return `still recorded by game ${names(k.games)}`;
    case "recorded":
      return `still recorded by ${names(k.profiles)}`;
    default:
      return k?.reason || "another profile";
  }
}

function keptPath(k) {
  const path = typeof k?.path === "string" ? k.path.replace(/^\/+/, "") : "";
  if (typeof k?.mod_path !== "string" || k.mod_path === "") return path;
  return `${k.mod_path.replace(/\/+$/, "")}/${path}`;
}
