// installfromfile.js - "Install from file…" (issue 535): install a mod
// whose source refused the download from the archive the user fetched by
// hand - the install twin of updatefromfile.js's "Update from file…".
//
// It is offered where the refusal is shown: beside "Open on <source>" on a
// failed install (errordetails.js's DownloadPage) - the search rows, the
// omnibar's fan-out, the search page, the slide-over and the tray. The
// failure itself names the mod and the file the install tried
// (core.DownloadError's details), so nothing is typed: the button hands
// them to main.js#installFromFile, which opens the page's one file input
// (updatefromfile.js's FromFileInput), uploads the chosen archive and plans
// the archive import with that identity (kind_import_archive.go's
// install_from_file).

import { html } from "../render.js";

/** installFromFileLabel is the control's visible text. */
export const installFromFileLabel = "Install from file…";

/**
 * InstallFromFileButton opens the file dialog for the failed install
 * `failure` describes (failures.js#downloadFailureFor's shape); the job it
 * starts reports under `origin`. Its accessible name carries the mod
 * ("Install from file…: Name", beginning with the visible text as WCAG
 * 2.5.3 requires), so two side by side are told apart.
 */
export function InstallFromFileButton({ failure, origin, actions }) {
  const label = failure.modName
    ? `${installFromFileLabel}: ${failure.modName}`
    : undefined;
  return html`<button
    type="button"
    class="button"
    data-action="install-from-file"
    aria-label=${label}
    onClick=${() => actions.installFromFile(failure, origin)}
  >
    ${installFromFileLabel}
  </button>`;
}

/** installFromFileOrigin is the per-mod origin a job started from a surface
 * with no control of its own to morph (the tray) reports under
 * (modrows.js#modOriginPattern's "mod:{source}/{id}:{action}"). */
export function installFromFileOrigin(sourceID, modID) {
  return `mod:${sourceID}/${modID}:install_from_file`;
}
