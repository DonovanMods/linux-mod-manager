// finishcollection.js - "Finish collection" (issue 660): a collection
// profile's items left to finish, each with the one action that resolves
// it, as `lmm profile finish` lists them.
//
// A collection import never stops at an item it cannot install: it
// installs the rest and records each such item as a to-do on the profile
// (core.CollectionTodoItem, GET /api/v1/profiles/{name}/todos). This panel
// is where they are finished, reachable from the Profile card, the profiles
// modal's row, and the toast and tray entry of the import itself:
//
//   - answer installer: a retry, whose installer question comes back as the
//     job's fomod_choices_required and is answered in the FOMOD wizard
//     (main.js re-posts the finish with the answers, as for an install);
//   - choose a file: a gone pin's current file (use_current) or, for a gone
//     secondary file, the mod without it (skip_file);
//   - retry: a download that failed, an archive lmm could not extract;
//   - download by hand: the fill drop zone (folder or files), as the
//     profiles modal's "Fill from downloads…" - a profile_fill job;
//   - skip, for every item (the only action for a mod its source deleted,
//     mod_gone): it leaves the profile, recorded so a collection update
//     does not add it back.
//
// Every action but the drop zone's is a collection_finish plan, confirmed
// in the confirm-plan modal that replaces this one in the shared slot;
// both come back here (modal.returnTo), where the job's progress and
// outcome show at the top and the list refreshes once it ends.

import { html, useEffect, useState } from "../render.js";
import { Modal } from "./modal.js";
import { getProfileTodos } from "../api.js";
import { InlineJob } from "./jobprogress.js";
import { FillDropZone } from "./filldropzone.js";
import { collectionTallyLabel } from "../progress.js";
import { todoName } from "./plan_collection_finish.js";

/** finishOrigin is the origin every collection_finish job of profile is
 * started from: one at a time, shown at the top of the panel. */
export function finishOrigin(profile) {
  return `profile:${profile}:finish`;
}

/** fillOrigin is the fill drop zone's own origin for profile
 * (filldropzone.js). */
export function fillOrigin(profile) {
  return `profile:${profile}:fill`;
}

// reasonLabels names each to-do reason (domain.Todo*) in a few words.
const reasonLabels = {
  installer_questions: "Installer needs answers",
  gone_pin: "File no longer offered",
  download_failed: "Download failed",
  manual_download: "Download by hand",
  unsupported_archive: "Can't extract",
  not_installed: "Not installed",
  refused: "Refused",
  mod_gone: "Gone from its source",
};

/** reasonLabel is a to-do's reason in words, or the reason itself. */
export function reasonLabel(todo) {
  return reasonLabels[todo?.reason] ?? todo?.reason ?? "";
}

/** todoKey is an item's "source:mod" key, which a finish names it by. */
export function todoKey(todo) {
  return `${todo.source_id}:${todo.mod_id}`;
}

/** finishSummary is the panel's lead sentence for a todo report. */
export function finishSummary(report) {
  const total = report?.total ?? 0;
  const todos = report?.todos?.length ?? 0;
  if (total === 0) return "";
  return `${collectionTallyLabel({ total, todos })}.`;
}

/** jobRunning reports whether origin's job is still running. */
function jobRunning(state, origin) {
  const id = state.origins?.[origin];
  if (!id) return false;
  const summary = (state.jobsIndex ?? []).find((row) => row.id === id);
  return !summary || summary.state === "running";
}

/** FinishCollectionModal renders the shared modal slot's
 * "finish-collection" shape: {game, profile, openerSelector}. */
export function FinishCollectionModal({ modal, state, actions }) {
  const open = modal?.type === "finish-collection";
  const game = modal?.game;
  const profile = modal?.profile;
  const [report, setReport] = useState(null);
  const [error, setError] = useState(null);
  // The row whose fill drop zone is open, by key.
  const [filling, setFilling] = useState("");

  // The list is re-read whenever a job ends: a finish or a fill this panel
  // started, or anything else that changed the profile meanwhile.
  const ended = (state.jobsIndex ?? [])
    .filter((row) => row.state !== "running")
    .map((row) => row.id)
    .join(",");
  useEffect(() => {
    if (!open || !profile) return undefined;
    let live = true;
    getProfileTodos(profile, { game })
      .then((doc) => {
        if (!live) return;
        setReport(doc);
        setError(null);
      })
      .catch((err) => {
        if (live) setError(err?.message ?? String(err));
      });
    return () => {
      live = false;
    };
  }, [open, game, profile, ended]);

  if (!open) return null;

  const todos = report?.todos ?? [];
  const busy =
    jobRunning(state, finishOrigin(profile)) ||
    jobRunning(state, fillOrigin(profile));
  const returnTo = {
    type: "finish-collection",
    game,
    profile,
    openerSelector: modal.openerSelector,
  };

  /** finish opens the collection_finish plan for request. */
  function finish(request, title, confirmLabel) {
    actions.openPlan({
      kind: "collection_finish",
      origin: finishOrigin(profile),
      title,
      confirmLabel,
      options: request,
      context: { game, profile },
      returnTo,
    });
  }

  const name = report?.collection || profile;

  return html`
    <${Modal}
      kind="finish-collection"
      title=${`Finish ${name}`}
      onClose=${actions.closeModal}
      openerSelector=${modal.openerSelector}
      footer=${html`
        <button
          type="button"
          class="button"
          data-action="close-finish"
          onClick=${actions.closeModal}
        >
          Close
        </button>
        <button
          type="button"
          class="button button--primary"
          data-action="finish-retry-all"
          disabled=${busy || todos.length === 0}
          onClick=${() =>
            finish(
              { retry_all: true },
              `Finish ${name}: retry all`,
              "Retry all",
            )}
        >
          Retry all…
        </button>
      `}
    >
      <div
        class="form-section finish-collection"
        data-testid="finish-collection"
      >
        ${error && html`<p class="modal__error">Couldn't list the items: ${error}</p>`}
        ${
          report &&
          html`<p class="form-hint" data-testid="finish-summary">
            ${
              todos.length === 0
                ? `Nothing is left to finish in ${profile}.`
                : `${finishSummary(report)} Each item below has the one thing that resolves it, or Skip to leave it out.`
            }
          </p>`
        }
        <${InlineJob}
          origin=${finishOrigin(profile)}
          state=${state}
          actions=${actions}
        />
        <${InlineJob}
          origin=${fillOrigin(profile)}
          state=${state}
          actions=${actions}
        />
        ${
          todos.length > 0 &&
          html`<ul class="finish-collection__list" data-testid="finish-todos">
            ${todos.map(
              (todo) =>
                html`<${TodoRow}
                  key=${todoKey(todo)}
                  todo=${todo}
                  busy=${busy}
                  filling=${filling === todoKey(todo)}
                  onFill=${() =>
                    setFilling(filling === todoKey(todo) ? "" : todoKey(todo))}
                  finish=${finish}
                  game=${game}
                  profile=${profile}
                  returnTo=${returnTo}
                  actions=${actions}
                />`,
            )}
          </ul>`
        }
      </div>
    <//>
  `;
}

/** TodoRow is one item: its name, why it is not installed, what resolves
 * it, and the buttons that do. */
function TodoRow({
  todo,
  busy,
  filling,
  onFill,
  finish,
  game,
  profile,
  returnTo,
  actions,
}) {
  const key = todoKey(todo);
  const label = todoName(todo);
  const primary = primaryAction(todo, label, finish);
  return html`
    <li
      class="finish-collection__row"
      data-todo=${key}
      data-reason=${todo.reason}
    >
      <div class="finish-collection__text">
        <span class="finish-collection__name">${label}</span>
        <span class="badge badge--warn">${reasonLabel(todo)}</span>
        <p class="form-hint">${todo.remedy}</p>
        ${
          todo.detail &&
          html`<p class="form-hint finish-collection__detail">
            ${todo.detail}
          </p>`
        }
      </div>
      <div class="form-actions finish-collection__actions">
        ${
          todo.action === "download"
            ? html`<button
                type="button"
                class="button button--small"
                data-action="finish-download"
                aria-expanded=${filling ? "true" : "false"}
                disabled=${busy}
                onClick=${onFill}
              >
                Download by hand…
              </button>`
            : primary &&
              html`<button
                type="button"
                class="button button--small"
                data-action=${primary.action}
                disabled=${busy}
                onClick=${primary.run}
              >
                ${primary.label}
              </button>`
        }
        <button
          type="button"
          class="button button--small"
          data-action="finish-skip"
          disabled=${busy}
          onClick=${() => finish({ skip: [key] }, `Skip ${label}`, "Skip")}
        >
          Skip…
        </button>
      </div>
      ${
        filling &&
        html`<${FillDropZone}
          profile=${profile}
          game=${game}
          actions=${actions}
          returnTo=${returnTo}
        />`
      }
    </li>
  `;
}

/** primaryAction is an item's one action other than Skip (and the drop
 * zone, which the row renders itself): {action, label, run}. */
function primaryAction(todo, label, finish) {
  const key = todoKey(todo);
  switch (todo.action) {
    case "skip":
      // A mod its source deleted (mod_gone): Skip is all there is.
      return null;
    case "answer_installer":
      return {
        action: "finish-answer",
        label: "Answer installer…",
        run: () =>
          finish({ retry: [key] }, `Answer ${label}'s installer`, "Continue"),
      };
    case "choose_file":
      return todo.can_skip_file
        ? {
            action: "finish-skip-file",
            label: "Skip file…",
            run: () =>
              finish(
                { skip_file: [key] },
                `Install ${label} without its gone file`,
                "Install",
              ),
          }
        : {
            action: "finish-use-current",
            label: "Use current file…",
            run: () =>
              finish(
                { use_current: [key] },
                `Install ${label}'s current file`,
                "Install",
              ),
          };
    default:
      return {
        action: "finish-retry",
        label: "Retry…",
        run: () => finish({ retry: [key] }, `Retry ${label}`, "Retry"),
      };
  }
}
