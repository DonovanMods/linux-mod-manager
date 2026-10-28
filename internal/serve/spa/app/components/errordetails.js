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
import {
  adapterRefusalFor,
  downloadFailureFor,
  loaderSetupFor,
  retryAtFor,
} from "../failures.js";
import { useSourceName } from "../sourcenames.js";
import { ModPageLink } from "./modpagelink.js";
import {
  UpdateFromFileButton,
  updateFromFileLabel,
  updateFromFileOrigin,
} from "./updatefromfile.js";
import {
  InstallFromFileButton,
  installFromFileLabel,
  installFromFileOrigin,
} from "./installfromfile.js";
import { DocumentView } from "./documentview.js";

/** loaderLabel spells a loader kind the way its own project does. An
 * unrecognised kind is shown as sent - kind is an open string. */
function loaderLabel(kind) {
  return kind === "bepinex" ? "BepInEx" : kind;
}

/**
 * ErrorDetails renders an error envelope's `details`, or nothing when there
 * are none. `actions`, when given, lets a failed download offer "Update from
 * file…" or "Install from file…" (DownloadPage); `headed` says the surface
 * already led with that download's headline (DownloadHeadline).
 */
export function ErrorDetails({ details, actions, headed }) {
  if (!details) return null;

  const loader = loaderSetupFor(details);
  if (loader) return html`<${LoaderSetup} loader=${loader} />`;

  const refusal = adapterRefusalFor(details);
  if (refusal) return html`<${AdapterRefusal} refusal=${refusal} />`;

  // Issue 513: a failed download names the mod's page. A Steam Workshop
  // failure is also a download failure, and its own keys (the tool's output
  // tail among them) still belong on screen - so the page goes ABOVE the
  // generic document view there rather than replacing it.
  const download = downloadFailureFor(details);
  if (download) {
    return details.published_file_id
      ? html`<${DownloadPage}
            failure=${download}
            actions=${actions}
            headed=${headed}
          />
          <${DocumentView} value=${details} />`
      : html`<${DownloadPage}
          failure=${download}
          actions=${actions}
          headed=${headed}
        />`;
  }

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
 * isUpdatable says whether a failed download is an UPDATE's - the mod is
 * installed in the current profile - so its way out is "Update from file…"
 * rather than "Install from file…".
 */
function isUpdatable(failure, actions) {
  return Boolean(
    failure.manual &&
    actions?.isModInstalled?.(failure.sourceID, failure.modID),
  );
}

/**
 * downloadHeadline is what a download its source refuses means, in one
 * sentence naming the source and the mod (issue 533: two failed items side
 * by side used to read identically). It leads every readout of such a
 * failure (issue 535); the engine's own text sits under it, collapsed
 * (RawError).
 */
function downloadHeadline(failure, sourceName, updatable) {
  const mod = failure.modName || "this mod";
  return updatable
    ? `${sourceName} won't let lmm download the update for ${mod}.`
    : `${sourceName} won't let lmm download ${mod}.`;
}

/**
 * DownloadHeadline renders downloadHeadline for a failure, for a surface
 * whose first line it is (jobprogress.js, the tray) - the source's display
 * name is a hook, so it is a component of its own.
 */
export function DownloadHeadline({ failure, actions }) {
  const sourceName = useSourceName(failure.sourceID, failure.manual);
  return downloadHeadline(failure, sourceName, isUpdatable(failure, actions));
}

/**
 * RawError is a failure's own engine text, collapsed under a "Details"
 * disclosure (issue 535): where a readout leads with what the failure means,
 * the text it came from is one click away rather than first.
 */
export function RawError({ text }) {
  if (!text) return null;
  return html`<details class="raw-error" data-testid="raw-error">
    <summary class="raw-error__summary">Details</summary>
    <p class="raw-error__text">${text}</p>
  </details>`;
}

/**
 * DownloadPage is a failed download's way out (issue 513): the mod's page on
 * its source, as an "Open on <source>" link. For a source that refuses
 * automated downloads it also says what to do with the file once it is
 * fetched by hand, and offers the control that takes it back right beside
 * the link: "Update from file…" for a mod that is already installed (a
 * failed update, issue 530), "Install from file…" for one that is not (a
 * failed install, issue 535) - the failure already names the mod and the
 * file, so nothing is retyped. `headed` drops the sentence's lead when the
 * surface has already shown it (DownloadHeadline); `origin` is the job
 * origin an "Install from file…" job reports under - the failed control's
 * own, so the new job takes its place. Renders nothing when there is neither
 * a usable page nor anything extra to say - the message above already gave
 * the reason.
 */
export function DownloadPage({ failure, actions, headed, origin }) {
  // Called before the early return: a hook never sits behind a condition.
  const sourceName = useSourceName(failure.sourceID, failure.manual);
  if (!failure.url && !failure.manual) return null;
  const updatable = isUpdatable(failure, actions);
  const installable =
    failure.manual && !updatable && Boolean(actions?.installFromFile);
  const what = failure.fileName
    ? html`<span class="mono">${failure.fileName}</span>`
    : "it";
  const control = updatable ? updateFromFileLabel : installFromFileLabel;
  return html`
    <div
      class="download-page"
      data-testid="download-page"
      data-manual=${failure.manual ? "true" : undefined}
    >
      ${
        failure.manual &&
        html`<p class="download-page__hint batch-failure__reason">
          ${!headed && `${downloadHeadline(failure, sourceName, updatable)} `}Download${" "}${what}${" "}from
          the mod's page, then hand it to lmm with "${control}" below.
        </p>`
      }
      <div class="download-page__actions">
        <${ModPageLink}
          url=${failure.url}
          sourceID=${failure.sourceID}
          modName=${failure.modName}
        />
        ${
          updatable &&
          html`<${UpdateFromFileButton}
            mod=${{
              source_id: failure.sourceID,
              id: failure.modID,
              name: failure.modName,
            }}
            origin=${updateFromFileOrigin(failure.sourceID, failure.modID)}
            actions=${actions}
          />`
        }
        ${
          installable &&
          html`<${InstallFromFileButton}
            failure=${failure}
            origin=${origin ?? installFromFileOrigin(failure.sourceID, failure.modID)}
            actions=${actions}
          />`
        }
      </div>
    </div>
  `;
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
 *
 * When `refusal.reason` is set (an AdapterPreconditionError, issue 353), the
 * reason IS the adapter's own remedy - and core's `Error()` already embeds
 * it verbatim in the message this component sits under (review F3). So
 * there is nothing left for this component to add: the generic "change the
 * adapter" sentence would point the wrong way, and repeating the reason
 * here would say the same thing twice.
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
        !refusal.reason &&
        html`<p class="adapter-refusal__remedy">
          Nothing runs on this game until its adapter can: change it in its
          games.yaml entry, or run${" "}<code
            >lmm game edit ${refusal.gameID} --adapter ${"<name>"}</code
          >, then try again.
        </p>`
      }
    </div>
  `;
}
