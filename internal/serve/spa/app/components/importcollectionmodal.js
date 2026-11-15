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
// as it does from the profiles modal. The section offers only the
// collection sources the game maps, read from state.status (issue 652), so
// this modal is handed the store's state as the profiles modal is.
//
// Issue 655: a failed import's toast offers "Back to import", which opens
// this modal with modal.failedJob naming the job - its failure is shown
// above the forms, one line per mod, so the dialog opens on what went wrong
// rather than on empty fields.

import { html } from "../render.js";
import { Modal } from "./modal.js";
import { ImportProfileForm } from "./profilesmodal.js";
import { ItemFailureList, RawError } from "./errordetails.js";

/** ImportCollectionModal renders only when the shared modal slot holds
 * this shape. openerSelector is the stable control focus returns to on
 * close (modal.js), for an opener that was a menu item. */
export function ImportCollectionModal({ modal, state, actions }) {
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
      <${ImportFailure} jobID=${modal.failedJob} state=${state} />
      <p class="empty-state__hint">
        A collection imports as a new profile of this game. You review what it
        would do before anything is written.
      </p>
      <${ImportProfileForm}
        state=${state}
        actions=${actions}
        close=${close}
        heading=${null}
      />
    <//>
  `;
}

/** ImportFailure is the failed import jobID's failure, from the jobs index:
 * one line per mod when the failure names several, the engine's own text
 * under "Details". Nothing when there is no such job (it has aged out of the
 * index) or it did not fail. */
function ImportFailure({ jobID, state }) {
  if (!jobID) return null;
  const job = (state.jobsIndex ?? []).find((row) => row.id === jobID);
  if (!job || job.state !== "failed") return null;
  const envelope = job.error ?? {};
  const items = envelope.items ?? [];
  return html`
    <div class="modal__error" role="alert" data-testid="import-failure">
      <p>
        The last import failed.${" "}
        ${
          items.length > 0
            ? items.length === 1
              ? "1 mod failed:"
              : `${items.length} mods failed:`
            : envelope.error
        }
      </p>
      <${ItemFailureList} items=${items} />
      ${items.length > 0 && html`<${RawError} text=${envelope.error} />`}
    </div>
  `;
}
