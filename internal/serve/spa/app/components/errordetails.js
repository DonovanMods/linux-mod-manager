// errordetails.js - a failure's typed details, rendered as what they MEAN
// where this UI knows the shape, and in full where it does not.
//
// Every place that shows a failed plan or job shows its envelope's details
// through here (issue 423): the confirm-plan modal, the activity tray and
// the inline failure chip. A loader refusal's setup steps are the whole
// value of that refusal, and the generic document view flattened them into
// a key/value dump beside five other fields - the web UI was the one
// surface that did not read them out as steps.
//
// The rule documentview.js follows still holds for everything else: show
// what the document carries, invent nothing.

import { html } from "../render.js";
import { codeSpans } from "../errortext.js";
import { adapterRefusalFor, loaderSetupFor, retryAtFor } from "../failures.js";
import { DocumentView } from "./documentview.js";

/** loaderLabel spells a loader kind the way its own project does. An
 * unrecognised kind is shown as sent - kind is an open string. */
function loaderLabel(kind) {
  return kind === "bepinex" ? "BepInEx" : kind;
}

/**
 * ErrorDetails renders an error envelope's `details`, or nothing when there
 * are none.
 */
export function ErrorDetails({ details }) {
  if (!details) return null;

  const loader = loaderSetupFor(details);
  if (loader) return html`<${LoaderSetup} loader=${loader} />`;

  const refusal = adapterRefusalFor(details);
  if (refusal) return html`<${AdapterRefusal} refusal=${refusal} />`;

  const retry = retryAtFor(details);
  if (retry) {
    // A clock time for today's resumption, the date too for a later one -
    // "at 09:00" is wrong by a day otherwise.
    const far = Math.abs(retry.at.getTime() - Date.now()) >= 12 * 3_600_000;
    const when = far
      ? retry.at.toLocaleString()
      : retry.at.toLocaleTimeString();
    return html`
      <p class="error-details__retry" data-testid="retry-at">
        lmm will ask ${retry.source} again at ${" "}${when}.
      </p>
    `;
  }

  return html`<${DocumentView} value=${details} />`;
}

/**
 * LoaderSetup is a core.LoaderRequiredError's setup steps as an ordered
 * list - each step verbatim, as the terminal prints it, with its commands
 * set in the monospace face (errortext.js) - under a heading that names the
 * loader and, when the mod asked for one, the version.
 */
export function LoaderSetup({ loader }) {
  const wanted = loader.loaderVersion
    ? `${loaderLabel(loader.kind)} ${loader.loaderVersion}`
    : loaderLabel(loader.kind);
  return html`
    <div
      class="loader-setup"
      data-testid="loader-setup"
      data-kind=${loader.kind}
      data-version=${loader.loaderVersion || undefined}
    >
      <p class="loader-setup__title">
        ${loader.modName || "This mod"} needs ${wanted}. To set it up for this
        game:
      </p>
      <ol class="loader-setup__steps">
        ${loader.steps.map(
          (step, i) =>
            html`<li key=${i} class="loader-setup__step">
              ${codeSpans(step)}
            </li>`,
        )}
      </ol>
    </div>
  `;
}

/**
 * AdapterRefusal is a refused adapter's remedy (issue 461): the game's
 * configuration refuses the flow, so nothing is wrong with the request and
 * nothing will change on a retry until the game is changed. The message
 * above it is core's sentence; this names where the fix is made, in both
 * frontends.
 */
export function AdapterRefusal({ refusal }) {
  const adapter = refusal.adapter || "generic-files";
  return html`
    <div
      class="adapter-refusal"
      data-testid="adapter-refusal"
      data-adapter=${adapter}
    >
      ${
        refusal.reason &&
        html`<p class="adapter-refusal__reason">
          ${codeSpans(refusal.reason)}
        </p>`
      }
      <p class="adapter-refusal__remedy">
        Fix the game's adapter in Setup → Games, or run ${" "}<code
          >lmm game edit ${refusal.gameID} --adapter ${"<name>"}</code
        >, then try again.
      </p>
    </div>
  `;
}
