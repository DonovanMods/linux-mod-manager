// plan_profile_import.js - the confirm modal's renderer for a
// core.ImportPlan (kind_profile_import.go, issue 332): what will be created,
// whether the name collides, and the three domain.ModReference buckets -
// already installed, needing a redownload, or missing entirely.
//
// A plan carrying `workshop_collection` (issue 346) is the same flow over a
// different input, and it renders differently on purpose: core's
// ApplyWorkshopCollectionImport FORCES Install=false / NoInstall=true, so
// the install controls below would be present-and-ignored - the exact shape
// issue 365 (b) was filed to remove. The collection document is also the
// only place the per-item remedy lives ("subscribe in Steam and re-run
// `lmm import --workshop`"), which the generic buckets cannot show: they
// hold bare refs with no name, no link and no note.

import { html } from "../render.js";
import { displayVersion } from "../version.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";

/** refLabel names a domain.ModReference - it carries no display name of its
 * own (domain.ModReference's own fields: source_id, mod_id, version,
 * file_ids, locked), so "source:mod" @ version is the honest rendering.
 *
 * The version goes through displayVersion, never raw: since issue 365 the
 * ref carries `external` and `updated_at` for exactly this reason - an
 * imported profile can name a Steam Workshop item, whose version field is a
 * 19-digit content id no human-facing surface may print (issue 269's
 * approval note). An external ref with no recorded date renders the helper's
 * em dash. */
function refLabel(ref) {
  const shown = displayVersion(ref);
  const at = shown ? ` @ ${shown}` : "";
  return `${ref.source_id}:${ref.mod_id}${at}`;
}

function RefList({ heading, refs }) {
  if (!refs || refs.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${refs.length})</h3>
      <ul class="plan__paths">
        ${refs.map(
          (ref) =>
            html`<li key=${`${ref.source_id}:${ref.mod_id}`} class="mono">
              ${refLabel(ref)}
            </li>`,
        )}
      </ul>
    </section>
  `;
}

/** CollectionItemList renders core.WorkshopCollection's own per-item view:
 * the item's display name (or its bare file id when lmm could not describe
 * it), a link to its Workshop page, and for anything not already tracked,
 * the note saying what to do about it. */
function CollectionItemList({ items }) {
  if (!items || items.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">Collection items (${items.length})</h3>
      <ul class="plan__mods" data-testid="collection-items">
        ${items.map(
          (item) => html`
            <li
              key=${item.file_id}
              class=${`plan__mod${item.tracked ? "" : " plan__mod--warn"}`}
            >
              <span class="plan__mod-name">
                ${item.name || `Workshop item ${item.file_id}`} </span
              >${" "}
              ${
                item.url &&
                html`<a
                  class="plan__mod-detail"
                  href=${item.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  >Workshop page</a
                >`
              }${" "}
              <span
                class=${`plan__mod-detail${item.tracked ? "" : " plan__mod-detail--warn"}`}
              >
                ${item.tracked ? "already tracked" : item.note}
              </span>
            </li>
          `,
        )}
      </ul>
    </section>
  `;
}

/** CollectionHeader names the collection the profile comes from, and says
 * plainly that nothing is downloaded - which is the whole reason the install
 * controls are absent rather than merely unchecked. */
function CollectionHeader({ collection }) {
  const name = collection.name || `Collection ${collection.collection_id}`;
  return html`
    <section class="plan__section" data-testid="collection-header">
      <h3 class="plan__heading">
        ${name}${" "}
        ${
          collection.url &&
          html`<a
            href=${collection.url}
            target="_blank"
            rel="noopener noreferrer"
            >on Steam</a
          >`
        }
      </h3>
      <p class="plan__note">
        ${collection.tracked} of ${collection.items?.length ?? 0} item(s) are
        already tracked. Nothing is downloaded: this import records the
        collection's list.
      </p>
    </section>
  `;
}

export function ProfileImportPlanView({ plan, modal, actions }) {
  const collection = plan.workshop_collection ?? null;
  const installed = plan.installed ?? [];
  const needsRedownload = plan.needs_redownload ?? [];
  const missing = plan.missing ?? [];
  // A collection import installs nothing whatever this preview offers, so
  // it has no pending count to offer it for.
  const pending = collection ? 0 : needsRedownload.length + missing.length;

  /** setInstall is the one apply-time choice this preview offers -
   * core.ProfileImportOptions.Install, the v2 Phase 3 Ruling 1 case in its
   * purest form: fully derivable from the plan (whether `pending` is
   * non-zero) before Apply ever runs. Force is the OTHER apply-time
   * option, offered only when the plan says the name collides. */
  function setInstall(e) {
    actions.setPlanOptions({ install: e.currentTarget.checked });
  }
  function setForce(e) {
    actions.setPlanOptions({ force: e.currentTarget.checked });
  }

  return html`
    <div class="plan plan--profile-import">
      <p class="plan__summary">
        Importing <span class="mono">${plan.profile?.name}</span> as a new
        profile${
          installed.length === 0
            ? ""
            : // "tracked", not "installed", for a collection: an already
              // tracked Workshop item is not deployed into the new profile,
              // and a switch to it changes nothing (W2 review, Important 5).
              ` (${installed.length} ${collection ? "item" : "mod"}${installed.length === 1 ? "" : "s"} already ${collection ? "tracked" : "installed"})`
        }.
      </p>

      ${
        plan.exists &&
        html`
          <p class="plan__note plan__note--warn">
            A profile named <span class="mono">${plan.profile?.name}</span>
            already exists.
          </p>
          <label class="plan__control">
            <input type="checkbox" onChange=${setForce} />
            Overwrite the existing profile
          </label>
        `
      }
      ${
        pending > 0 &&
        html`
          <label class="plan__control">
            <input type="checkbox" onChange=${setInstall} />
            Download and install ${pending} pending
            mod${pending === 1 ? "" : "s"}
          </label>
          <p class="plan__note">
            Left unchecked, the profile is still saved and every pending mod is
            recorded as skipped.
          </p>
        `
      }
      ${collection && html`<${CollectionHeader} collection=${collection} />`}
      ${
        collection
          ? html`<${CollectionItemList} items=${collection.items} />`
          : html`
              <${RefList} heading="Already installed" refs=${installed} />
              <${RefList} heading="Needs redownload" refs=${needsRedownload} />
              <${RefList} heading="Missing entirely" refs=${missing} />
            `
      }
      ${
        !collection &&
        html`
          <${PlanAdvanced}>
            <${ApplyOption}
              modal=${modal}
              actions=${actions}
              name="no_install"
              label="Never install"
              hint="--no-install: a hard override that skips installing even if the checkbox above is checked"
            />
          <//>
        `
      }
    </div>
  `;
}
