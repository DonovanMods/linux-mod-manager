// cards.js - the attention cards: Updates, Health, Conflicts
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Attention
// cards"). Each renders only when it has something to say - a card absent
// entirely is itself the "nothing needs you here" signal. Unit 3 landed the
// confirm-modal framework every mutation below submits through; this unit
// (issue 332) wires the per-row and batch actions themselves.

import { html, useState } from "../render.js";
import { findingLabel } from "../verify.js";
import { InlineJob } from "./jobprogress.js";
import { EXTERNAL_UPDATE_NOTE, lockedNote, modKey } from "../modrows.js";
import { displayVersion, displayUpdateTarget } from "../version.js";
import { relativeTime } from "../relativetime.js";
import { conflictLabel } from "../conflicts.js";

// UPDATES_BATCH_ORIGIN is the Updates card's own "Update selected" control -
// distinct from a single-mod update's own "mod:{source}/{id}:update"
// (modpanel.js/fullmodpage.js), which this card's own checkboxes never use.
const UPDATES_BATCH_ORIGIN = "updates:batch";

// HEALTH_REPAIR_ALL_ORIGIN is the Health card's "Repair all" control.
const HEALTH_REPAIR_ALL_ORIGIN = "health:repair-all";

// PROFILE_APPLY_ORIGIN is the Profile card's "Apply profile…" control.
const PROFILE_APPLY_ORIGIN = "profile:apply";

// PROFILE_SYNC_ORIGIN is the Profile card's "Sync…" control (C-3).
const PROFILE_SYNC_ORIGIN = "profile:sync";

/** notFixableReason names why a finding's own Repair is absent.
 *
 * Since issue 334 this is the ENGINE's own sentence, read straight off the
 * finding (core.VerifyFinding.FixableReason), which replaced the
 * hand-maintained status->sentence table and the locked/unlocked guess that
 * used to live here. That guess read the lock off a separate library fetch
 * and could not see the local-source case at all, so a version_mismatch on
 * a locally imported mod got the generic sentence; the engine knows which
 * of its own decision points refused the repair and says so.
 *
 * The generic fallback survives for a row the engine had nothing to add
 * about - and for a finding decoded from an older document that carries no
 * such key. */
function notFixableReason(f) {
  return (
    f.fixable_reason ||
    "A verify --fix run would not attempt a repair for this finding"
  );
}

/** repairOrigin is one finding's own per-mod "Repair" control - shared by
 * every finding ROW for the same mod (a mod can carry more than one
 * finding), which is deliberate: they name the same repair operation, so
 * clicking any of them and having every one of that mod's rows morph
 * together is the honest picture of what a mod_filter repair actually
 * does. */
function repairOrigin(modID) {
  return `health:${modID}:repair`;
}

/** AttentionCards reads the three already-fetched report documents (core.
 * UpdateCheckReport, core.VerifyReport, core.ConflictReport) and renders
 * nothing at all - not even the section - when none has anything to show
 * AND none of the three fetches failed. A failed fetch is never swallowed
 * into that same "nothing needs you here" silence (design doc §Search:
 * "Source failures surface as a warning row, never swallowed") - its card
 * renders with an explicit error and a retry, even though the document
 * behind it is null. */
export function AttentionCards({
  state,
  updates,
  health,
  conflicts,
  mods,
  errors = {},
  actions,
}) {
  const updateRows = updates?.updates ?? [];
  const findings = (health?.result?.findings ?? []).filter(
    (f) => f.status !== "ok",
  );
  const conflictRows = conflicts?.conflicts ?? [];
  const hasError = Boolean(errors.updates || errors.health || errors.conflicts);
  const notInstalled = notInstalledCount(state, mods);
  const notListed = notListedCount(state, mods);

  if (
    updateRows.length === 0 &&
    findings.length === 0 &&
    conflictRows.length === 0 &&
    notInstalled === 0 &&
    notListed === 0 &&
    !hasError
  ) {
    return null;
  }

  return html`
    <section class="attention-cards">
      ${
        (updateRows.length > 0 || errors.updates) &&
        html`<${UpdatesCard}
          state=${state}
          rows=${updateRows}
          error=${errors.updates}
          onRetry=${actions.reloadUpdates}
          actions=${actions}
        />`
      }
      ${
        (findings.length > 0 || errors.health) &&
        html`<${HealthCard}
          state=${state}
          findings=${findings}
          result=${health?.result}
          error=${errors.health}
          onReverify=${actions.reloadHealth}
          actions=${actions}
        />`
      }
      ${
        (notInstalled > 0 || notListed > 0) &&
        html`<${ProfileCard}
          state=${state}
          notInstalled=${notInstalled}
          notListed=${notListed}
          actions=${actions}
        />`
      }
      ${
        (conflictRows.length > 0 || errors.conflicts) &&
        html`<${ConflictsCard}
          state=${state}
          rows=${conflictRows}
          error=${errors.conflicts}
          onRetry=${actions.reloadConflicts}
          actions=${actions}
        />`
      }
    </section>
  `;
}

function UpdatesCard({ state, rows, error, onRetry, actions }) {
  const [selected, setSelected] = useState(() => new Set());

  function toggle(key) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function updateSelected() {
    if (selected.size === 0) return;
    actions.openPlan({
      kind: "updates",
      origin: UPDATES_BATCH_ORIGIN,
      title: `Update ${selected.size} mod${selected.size === 1 ? "" : "s"}`,
      confirmLabel: "Update",
      options: { mods: [...selected] },
    });
  }

  return html`
    <div class="card card--updates">
      <h2 class="card__title">⬆ Updates (${rows.length})</h2>
      ${
        error
          ? html`<${CardError}
              message="Couldn't check for updates"
              detail=${error}
              onRetry=${onRetry}
            />`
          : html`
              <ul class="card__list">
                ${rows.map((u) => {
                  const key = modKey(u.installed_mod);
                  // issue 269: an EXTERNAL row is one ApplyUpdateBatch declines
                  // outright, and no choice available in this UI can change
                  // that - so it is rendered, marked, and given no checkbox.
                  // It therefore never enters `selected`, never reaches the
                  // count on the button, and never reaches the batch. (A
                  // LOCKED row keeps its checkbox: a lock is the user's own
                  // reversible choice, so ticking it is a coherent thing to
                  // do once the lock is lifted.)
                  const external = Boolean(u.installed_mod.external);
                  // ONE string, not adjacent interpolations: htm collapses
                  // the whitespace between those, and at this indent Prettier
                  // is free to break the line in the middle of the arrow -
                  // which puts a real newline into the rendered text (the trap
                  // conflictLabel below documents). Both halves go through
                  // version.js, so an external row reads as its revision date
                  // and an honest "newer" rather than two 19-digit Steam
                  // content ids (issue 269's version DISPLAY rule).
                  const detail = `${displayVersion(u.installed_mod)} → ${displayUpdateTarget(u)}`;
                  return html`
                    <li key=${key} class="card__row">
                      ${
                        external
                          ? html`<span
                              class="card__row-spacer"
                              aria-hidden="true"
                            ></span>`
                          : html`<input
                              type="checkbox"
                              aria-label=${`Select ${u.installed_mod.name} for update`}
                              checked=${selected.has(key)}
                              onChange=${() => toggle(key)}
                            />`
                      }
                      <span class="card__row-name" title=${u.installed_mod.name}
                        >${u.installed_mod.name}</span
                      >
                      <span class="mono card__row-detail" title=${detail}
                        >${detail}</span
                      >
                      ${
                        // Owner item 3, unit 8 gate review: a locked row was
                        // rendered as a plain checkbox like any other, so
                        // ticking it promised an update the engine will
                        // refuse. The mark is here, on the control the user
                        // actually ticks, and again on the confirm modal.
                        u.locked &&
                        html`<span
                          class="card__row-lock"
                          data-testid="update-lock"
                          title=${`This update will be skipped: ${lockedNote(u)}`}
                          >${`🔒 ${lockedNote(u)}`}</span
                        >`
                      }
                      ${
                        external &&
                        html`<span
                          class="card__row-lock"
                          data-testid="update-external"
                          title=${`This update will be skipped: ${EXTERNAL_UPDATE_NOTE}`}
                          >${`will be skipped — ${EXTERNAL_UPDATE_NOTE}`}</span
                        >`
                      }
                    </li>
                  `;
                })}
              </ul>
              <div class="card__actions">
                <${InlineJob}
                  origin=${UPDATES_BATCH_ORIGIN}
                  state=${state}
                  actions=${actions}
                >
                  <button
                    type="button"
                    class="button"
                    data-action="update-selected"
                    disabled=${selected.size === 0}
                    onClick=${updateSelected}
                  >
                    Update selected
                  </button>
                <//>
              </div>
            `
      }
    </div>
  `;
}

// HealthCard carries both halves of the design doc's "last-verify
// timestamp + re-run" (§Mission Control): onReverify is a plain re-fetch of
// /api/v1/health (it also doubles as the I3 CardError retry above), and the
// timestamp is core.VerifyResult.checked_at, the field issue 334 added for
// exactly this line after issue 332 had to carry it.
//
// It renders as an AGE, not a clock time: what a reader needs from it is
// whether the findings below are minutes or days old. checked_at is stamped
// at the START of a verify run (internal/core/verify.go), which is the
// honest anchor - a run that stopped halfway still checked what it checked,
// at that moment.
//
// omitzero on the wire means a hand-built VerifyResult carries no such key
// at all, so the line is omitted rather than rendered as an invalid date.
function HealthCard({ state, findings, result, error, onReverify, actions }) {
  function repair(modID, name) {
    actions.openPlan({
      kind: "verify_fix",
      origin: repairOrigin(modID),
      title: `Repair ${name || modID}`,
      confirmLabel: "Repair",
      options: { mod_filter: modID },
    });
  }

  function repairAll() {
    actions.openPlan({
      kind: "verify_fix",
      origin: HEALTH_REPAIR_ALL_ORIGIN,
      title: "Repair all findings",
      confirmLabel: "Repair all",
      options: {},
    });
  }

  // ONE string, not adjacent interpolations - htm's whitespace collapsing
  // (see conflictLabel below) would fuse "verified" to the age.
  //
  // core.VerifyResult.cached (issue 336) says this answer came from core's
  // memo rather than from a run just now: nothing it fingerprints has moved
  // since checked_at, so the previous verdict still stands. The line says
  // which it is, because "last verified 3 minutes ago" over a memo hit and
  // over a fresh run mean different things to a reader deciding whether to
  // press Re-verify.
  const checked = relativeTime(result?.checked_at);
  const lastVerified = checked
    ? result?.cached
      ? `Unchanged since ${checked}`
      : `Last verified ${checked}`
    : "";

  return html`
    <div class="card card--health">
      <h2 class="card__title">
        ⚠ Health${result ? ` (${result.issues + result.warnings})` : ""}
      </h2>
      ${
        lastVerified &&
        html`<p class="card__meta" data-testid="health-last-verified">
          ${lastVerified}
        </p>`
      }
      ${
        error
          ? html`<${CardError}
              message="Couldn't check health"
              detail=${error}
              onRetry=${onReverify}
            />`
          : html`
              <ul class="card__list">
                ${findings.map(
                  (f, i) => html`
                    <li
                      key=${f.mod_id + "/" + (f.file_id || i)}
                      class="card__row"
                    >
                      <span
                        class="card__row-name"
                        title=${f.mod_name || f.mod_id}
                        >${f.mod_name || f.mod_id}</span
                      >
                      <span class="card__row-detail" title=${findingLabel(f)}
                        >${findingLabel(f)}</span
                      >
                      ${
                        f.fixable
                          ? html`<${InlineJob}
                              origin=${repairOrigin(f.mod_id)}
                              state=${state}
                              actions=${actions}
                            >
                              <button
                                type="button"
                                class="button button--small"
                                data-action="repair"
                                onClick=${() => repair(f.mod_id, f.mod_name)}
                              >
                                Repair
                              </button>
                            <//>`
                          : html`<span
                              class="card__row-detail"
                              title=${notFixableReason(f)}
                              >Not fixable: ${notFixableReason(f)}</span
                            >`
                      }
                    </li>
                  `,
                )}
              </ul>
              <div class="card__actions">
                <button
                  type="button"
                  class="button button--small"
                  onClick=${onReverify}
                >
                  Re-verify
                </button>
                <${InlineJob}
                  origin=${HEALTH_REPAIR_ALL_ORIGIN}
                  state=${state}
                  actions=${actions}
                >
                  <button
                    type="button"
                    class="button"
                    data-action="repair-all"
                    disabled=${!findings.some((f) => f.fixable)}
                    onClick=${repairAll}
                  >
                    Repair all
                  </button>
                <//>
              </div>
            `
      }
    </div>
  `;
}

/** notInstalledCount is how many of the current profile's listed mods have
 * no install behind them - the number `lmm profile apply` exists to bring
 * to zero, and the only thing the Profile card renders on.
 *
 * It is a SUBTRACTION of two documents this route already has, not a third
 * fetch: core.ProfileSummary.mod_count is the profile YAML's own load order
 * (GET /api/v1/status, scoped), and core.ModList carries one row per
 * INSTALLED mod (GET /api/v1/mods). Planning the mutation would answer it
 * exactly, but a plan is a mutation-shaped round trip with a server-side
 * handle and a TTL, and Mission Control renders on every hydrate - so the
 * card is derived, and the PLAN behind "Apply profile…" is what tells the
 * precise truth.
 *
 * The one case the subtraction understates: core.ListMods also lists a mod
 * that is installed but ABSENT from the load order (never silently
 * dropped - internal/core/queries.go), so a profile with one such row AND
 * one uninstalled entry cancels to zero and the card stays away. That is a
 * missing prompt, never a false one, which is the right direction for a
 * surface whose whole contract is "renders only when it has something to
 * say".
 */
function notInstalledCount(state, mods) {
  const profileName = state?.route?.profile;
  const summary = (state?.status?.profiles ?? []).find(
    (p) => p.name === profileName,
  );
  if (!summary) return 0;
  return Math.max(0, summary.mod_count - (mods?.mods?.length ?? 0));
}

/** notListedCount is the OTHER direction of the same subtraction (C-3):
 * how many installed rows this profile's load order does not list. It is
 * what `lmm profile sync` brings to zero, where notInstalledCount is what
 * `lmm profile apply` does - the two commands are mirror images, and so are
 * the two numbers. Under-counts for exactly the reason its twin does, and
 * in the same safe direction: a missing prompt, never a false one. */
function notListedCount(state, mods) {
  const summary = (state?.status?.profiles ?? []).find(
    (p) => p.name === state?.route?.profile,
  );
  if (!summary) return 0;
  return Math.max(0, (mods?.mods?.length ?? 0) - summary.mod_count);
}

/** ProfileCard is the design's third attention state (issue 334): the
 * profile and the installed set have drifted apart, and there are exactly
 * two commands that close the gap - `lmm profile apply` pulls the INSTALLS
 * onto what the profile lists, `lmm profile sync` pulls the PROFILE onto
 * what is installed. Which of the two is offered depends on which way the
 * drift runs; Sync is offered either way, because it is the one that can
 * answer both buckets at once. */
function ProfileCard({ state, notInstalled, notListed, actions }) {
  // ONE string rather than three adjacent interpolations: htm collapses
  // JSX-style whitespace between them, which silently fuses "profile" and
  // "is" into "profileis" (the same trap conflictLabel below documents).
  const sentence =
    notInstalled > 0
      ? `${notInstalled} mod${notInstalled === 1 ? "" : "s"} in this profile ${notInstalled === 1 ? "is" : "are"} not installed`
      : `${notListed} installed mod${notListed === 1 ? "" : "s"} ${notListed === 1 ? "is" : "are"} not in this profile's load order`;

  function apply() {
    actions.openPlan({
      kind: "profile_apply",
      origin: PROFILE_APPLY_ORIGIN,
      title: `Apply ${state.route.profile}`,
      confirmLabel: "Apply profile",
      options: { profile: state.route.profile },
    });
  }

  function sync() {
    actions.openPlan({
      kind: "profile_sync",
      origin: PROFILE_SYNC_ORIGIN,
      title: `Sync ${state.route.profile}`,
      confirmLabel: "Sync",
      options: { profile: state.route.profile },
    });
  }

  return html`
    <div class="card card--profile">
      <h2 class="card__title">${`◎ Profile (${notInstalled || notListed})`}</h2>
      <ul class="card__list">
        <li class="card__row">
          <span class="card__row-name">${sentence}</span>
        </li>
      </ul>
      <div class="card__actions">
        ${
          notInstalled > 0 &&
          html`<${InlineJob}
            origin=${PROFILE_APPLY_ORIGIN}
            state=${state}
            actions=${actions}
          >
            <button
              type="button"
              class="button"
              data-action="apply-profile"
              onClick=${apply}
            >
              Apply profile…
            </button>
          <//>`
        }
        <${InlineJob}
          origin=${PROFILE_SYNC_ORIGIN}
          state=${state}
          actions=${actions}
        >
          <button
            type="button"
            class="button"
            data-action="sync-profile"
            onClick=${sync}
          >
            Sync…
          </button>
        <//>
      </div>
    </div>
  `;
}

/** The per-card explicit error state: what failed, and a retry that
 * re-fetches just this card's document (main.js's reload actions) - never
 * the whole-page hydrate, so a retry after a slow health check doesn't cost
 * the mods/updates/conflicts reads that already succeeded. */
function CardError({ message, detail, onRetry }) {
  return html`
    <div>
      <p class="card__error">${message}: ${detail}</p>
      <button type="button" class="button button--small" onClick=${onRetry}>
        Retry
      </button>
    </div>
  `;
}

function ConflictsCard({ state, rows, error, onRetry, actions }) {
  // Demo item 9 (unit 6 gate review): "Resolve…" used to always land the
  // reorder modal at the top of the list, same as the library's own plain
  // "Reorder…" - on a long profile the owner had to hunt for the two mods
  // they just clicked about. c.owner is the file's CURRENT deployed
  // provider (this row's own label lists it first, "owner ↔ also_in"),
  // which is what "Reorder here" already scrolls to for a single mod's own
  // row menu (library.js) - the same focusKey here.
  function resolve(c) {
    actions.openReorderModal({
      profileName: state.route.profile,
      focusKey: c.owner.key,
    });
  }

  return html`
    <div class="card card--conflicts">
      <h2 class="card__title">⇄ Conflicts (${rows.length})</h2>
      ${
        error
          ? html`<${CardError}
              message="Couldn't check for conflicts"
              detail=${error}
              onRetry=${onRetry}
            />`
          : html`
              <ul class="card__list">
                ${rows.map(
                  (c) => html`
                    <li key=${c.path} class="card__row">
                      <span class="card__row-name" title=${conflictLabel(c)}
                        >${conflictLabel(c)}</span
                      >
                      <span class="mono card__row-detail" title=${c.path}
                        >${c.path}</span
                      >
                      <button
                        type="button"
                        class="button button--small"
                        data-action="resolve"
                        onClick=${() => resolve(c)}
                      >
                        Resolve…
                      </button>
                    </li>
                  `,
                )}
              </ul>
            `
      }
    </div>
  `;
}
