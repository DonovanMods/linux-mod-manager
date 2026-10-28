// batchfailures.js - a finished update batch's failed items (issues 530 and
// 533), one entry each, listed where the batch's outcome is read: the inline
// readout on the control that started it, and the activity tray.
//
// The tally says "1 applied / 2 failed"; this is the "which two". Every entry
// is headed with the item's own name and the change that was attempted, so
// two failures side by side are never "this file" twice, and its buttons
// carry the mod in their accessible names and act on that mod alone.

import { html } from "../render.js";
import { DownloadPage } from "./errordetails.js";

/** changeLabel is the attempted change, "1.2 → 1.3" - or whichever half the
 * check carried, or "" when it carried neither. */
function changeLabel(failure) {
  const { fromVersion: from, toVersion: to } = failure;
  if (from && to) return `${from} → ${to}`;
  return to ? `→ ${to}` : from;
}

/**
 * BatchFailures renders `failures` (failures.js#batchFailuresFor's shape) as
 * a list, or nothing when there are none.
 */
export function BatchFailures({ failures, actions }) {
  if (failures.length === 0) return null;
  return html`
    <ul class="batch-failures" data-testid="batch-failures">
      ${failures.map(
        (f) =>
          html`<${BatchFailure}
            key=${`${f.sourceID}:${f.modID}`}
            failure=${f}
            actions=${actions}
          />`,
      )}
    </ul>
  `;
}

function BatchFailure({ failure, actions }) {
  const name = failure.modName || failure.modID;
  const change = changeLabel(failure);
  // A download the source refuses carries its own explanation (DownloadPage
  // names the source and the mod). Any other failure shows the engine's own
  // text under the same header, with the mod's page when it has one.
  const explained = failure.manual;
  return html`
    <li
      class="batch-failure"
      data-testid="batch-failure"
      data-mod=${`${failure.sourceID}:${failure.modID}`}
    >
      <h4 class="batch-failure__head">
        <span class="batch-failure__name">${change ? `${name},` : name}</span
        >${" "}${
          change && html`<span class="batch-failure__change">${change}</span>`
        }
      </h4>
      ${
        !explained &&
        failure.error &&
        html`<p class="batch-failure__reason">${failure.error}</p>`
      }
      <${DownloadPage} failure=${failure} actions=${actions} />
    </li>
  `;
}
