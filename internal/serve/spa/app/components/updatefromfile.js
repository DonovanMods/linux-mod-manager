// updatefromfile.js - "Update from file…" (issue 530): update an installed
// mod from an archive the user downloaded by hand, for a source that will
// not serve the file through its API (kind_update_from_archive.go).
//
// Offered wherever the mod is: the slide-over, the full mod page, the
// library row's menu - and beside "Open on <source>" on a failed download
// whose source refused it (errordetails.js's DownloadPage), which is the
// moment the user has just been told to fetch the file themselves.
//
// ONE file input serves every one of those buttons, mounted once by app.js
// rather than inside each button. A button in the row menu is gone the
// moment the menu closes - which it does as the file dialog takes focus -
// and an input unmounted with it would never deliver the file it was
// asked for. The button names the mod (main.js#updateFromFile) and opens
// the dialog; the input hands the chosen file to main.js, which uploads it
// (POST /api/v1/uploads) and opens the plan in the confirm modal - the same
// Plan/Apply pipeline every mutation here travels.

import { html } from "../render.js";

/** updateFromFileInputID is the one input's id, for main.js to open. */
export const updateFromFileInputID = "update-from-file-input";

/** updateFromFileLabel is the control's visible text, on every surface. */
export const updateFromFileLabel = "Update from file…";

/**
 * UpdateFromFileInput is the page's one hidden file input for "Update from
 * file…". It is never focusable: the visible buttons are the controls, and
 * this only carries the browser's file dialog. It is named all the same,
 * like every form control here (TestE2E_EveryFormControlHasAnAccessibleName).
 */
export function UpdateFromFileInput({ actions }) {
  return html`<input
    id=${updateFromFileInputID}
    data-testid=${updateFromFileInputID}
    type="file"
    accept=".zip,.7z,.rar"
    hidden
    tabindex="-1"
    aria-hidden="true"
    aria-label="Archive to update the mod from"
    onChange=${(e) => {
      const file = e.currentTarget.files?.[0];
      // Cleared so choosing the same file again still fires a change.
      e.currentTarget.value = "";
      if (file) actions.updateFromFileChosen(file);
    }}
  />`;
}

/**
 * UpdateFromFileButton opens the file dialog for `mod` ({source_id, id,
 * name}); the job it ends up starting reports under `origin`, so an
 * InlineJob around the button shows its progress. `menu` renders it as a
 * row-menu item instead of a button. A locked mod's control is disabled - an
 * update of it is refused - with the reason in `title` and, for a keyboard
 * user who cannot reach a disabled control's title, wherever the surface
 * already says the mod is locked.
 */
export function UpdateFromFileButton({
  mod,
  origin,
  actions,
  menu = false,
  locked = false,
  onOpen,
}) {
  const open = () => {
    onOpen?.();
    actions.updateFromFile(mod, origin);
  };
  const title = locked ? "Unlock this mod to update it" : undefined;
  return menu
    ? html`<button
        type="button"
        class="menu-item row-menu__item"
        data-action="update-from-file"
        disabled=${locked}
        title=${title}
        onClick=${open}
      >
        ${updateFromFileLabel}
      </button>`
    : html`<button
        type="button"
        class="button"
        data-action="update-from-file"
        disabled=${locked}
        title=${title}
        onClick=${open}
      >
        ${updateFromFileLabel}
      </button>`;
}

/** updateFromFileOrigin is the per-mod origin the job reports under
 * (modrows.js#modOriginPattern's "mod:{source}/{id}:{action}"). */
export function updateFromFileOrigin(sourceID, modID) {
  return `mod:${sourceID}/${modID}:update_from_file`;
}
