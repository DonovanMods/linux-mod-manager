// importcollectionmodal.js - "Import collection…" (issue 650).
//
// Importing a collection used to live only behind the profile picker's
// "Manage profiles…", at the bottom of a modal whose first job is the
// profile list. This modal is the visible door to it: the profile picker's
// menu and Mission Control's Profile card open it.
//
// It holds the profiles modal's own import section (ImportProfileForm: the
// file upload, the URL, Steam Workshop and NexusMods inputs) rather than a
// second copy of those forms, so both doors plan the identical
// profile_import request. A small modal of its own was chosen over opening
// the profiles modal scrolled to that section: that modal fills its profile
// list in AFTER it opens (issue 584), which moves the import section down under
// a scroll that would have to chase it, and puts a list the user did not
// ask for between them and the field they did.
//
// Each form closes this modal and opens the plan in the shared modal slot,
// as it does from the profiles modal.

import { html } from "../render.js";
import { Modal } from "./modal.js";
import { ImportProfileForm } from "./profilesmodal.js";

/** ImportCollectionModal renders only when the shared modal slot holds
 * this shape. openerSelector is the stable control focus returns to on
 * close (modal.js), for an opener that was a menu item. */
export function ImportCollectionModal({ modal, actions }) {
  if (modal?.type !== "import-collection") return null;

  function close() {
    actions.closeModal();
  }

  return html`
    <${Modal}
      kind="import-collection"
      title="Import a collection"
      onClose=${close}
      openerSelector=${modal.openerSelector}
    >
      <p class="empty-state__hint">
        A collection imports as a new profile of this game. You review what it
        would do before anything is written.
      </p>
      <${ImportProfileForm}
        actions=${actions}
        close=${close}
        heading="From a file"
      />
    <//>
  `;
}
