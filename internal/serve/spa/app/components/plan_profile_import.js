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

// A plan carrying `collection` (issue 621) is an lmm collection: an
// ordinary import - its buckets and install controls are the same - with
// the collection's own part rendered first. That part is everything the
// buckets do not show and the user must see before anything is written:
// who made it and how it matched this game, the features this lmm lacks
// (the apply refuses then), the hooks (scripts lmm would run, imported only
// when the user ticks the box), the config overrides, the items lmm does
// not fetch, and mods from sources this machine does not have.

import { html } from "../render.js";
import { displayVersion } from "../version.js";
import { safeWebUrl } from "../weburl.js";
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

/** matchLine says how a collection was matched to this game. */
function matchLine(c) {
  if (c.matched_by === "game") return `imported into ${c.game_id}`;
  if (c.matched_by === "steam_app_id")
    return `matched to ${c.game_id} by Steam app id ${c.game?.steam_app_id}`;
  if (c.matched_by?.startsWith("source:")) {
    const src = c.matched_by.slice("source:".length);
    return `matched to ${c.game_id} by its ${src} id ${c.game?.sources?.[src] ?? ""}`;
  }
  return c.game_id;
}

/** LMMCollectionSection renders core.CollectionImport. Every string in it
 * was sanitised by core (issue 594), and the preact templates escape it. */
function LMMCollectionSection({ collection: c, actions }) {
  const unavailable = (c.items ?? []).filter((item) => item.unavailable);
  function setWithHooks(e) {
    actions.setPlanOptions({ with_hooks: e.currentTarget.checked });
  }
  return html`
    <section class="plan__section" data-testid="lmm-collection">
      <h3 class="plan__heading">
        ${c.name}${c.version ? ` (version ${c.version})` : ""}
      </h3>
      ${c.author && html`<p class="plan__note">by ${c.author}</p>`}
      ${
        c.origin &&
        html`<p class="plan__note">
          from
          ${" "}${c.origin.url || `${c.origin.source_id ?? ""} ${c.origin.slug ?? ""}`}${
            c.origin.author ? `, curated by ${c.origin.author}` : ""
          }
        </p>`
      }
      <p class="plan__note">${matchLine(c)}</p>
      ${c.description && html`<p class="plan__note">${c.description}</p>`}
      ${
        c.install_instructions &&
        html`<p class="plan__note">
          <strong>Install instructions:</strong> ${c.install_instructions}
        </p>`
      }
      ${(c.warnings ?? []).map(
        (w) => html`<p class="plan__note plan__note--warn">${w}</p>`,
      )}
    </section>
    ${
      (c.unsupported ?? []).length > 0 &&
      html`<section class="plan__section" data-testid="collection-unsupported">
        <p class="plan__note plan__note--warn">
          This collection needs features this version of lmm cannot import:
          ${" "}${c.unsupported.map((f) => f.feature).join(", ")}. Importing it
          is refused, with nothing written; update lmm and try again.
        </p>
      </section>`
    }
    ${
      unavailable.length > 0 &&
      html`<section class="plan__section">
        <h3 class="plan__heading">
          From a source lmm does not have here (${unavailable.length})
        </h3>
        <ul class="plan__mods">
          ${unavailable.map(
            (item) =>
              html`<li
                key=${`${item.source_id}:${item.mod_id}`}
                class="plan__mod plan__mod--warn"
              >
                <span class="plan__mod-name"
                  >${item.name || `${item.source_id}:${item.mod_id}`}</span
                >${" "}
                <span class="plan__mod-detail plan__mod-detail--warn"
                  >${item.unavailable}</span
                >
              </li>`,
          )}
        </ul>
      </section>`
    }
    ${
      (c.external ?? []).length > 0 &&
      html`<section class="plan__section" data-testid="collection-external">
        <h3 class="plan__heading">
          Not fetched by lmm - get these by hand (${c.external.length})
        </h3>
        <ul class="plan__mods">
          ${c.external.map((e, i) => {
            // Core drops a link that is not a web address; the browser
            // applies the same rule again before anything becomes an href.
            const url = safeWebUrl(e.url);
            return html`<li key=${i} class="plan__mod">
              <span class="plan__mod-name">${e.name}</span>${" "}
              ${
                url
                  ? html`<a
                      class="plan__mod-detail"
                      href=${url}
                      target="_blank"
                      rel="noopener noreferrer"
                      >${url}</a
                    >`
                  : e.url &&
                    html`<span class="plan__mod-detail">${e.url}</span>`
              }${" "}
              ${
                e.instructions &&
                html`<span class="plan__mod-detail">${e.instructions}</span>`
              }
            </li>`;
          })}
        </ul>
      </section>`
    }
    ${
      (c.overrides ?? []).length > 0 &&
      html`<section class="plan__section">
        <h3 class="plan__heading">
          Config overrides written into the game directory
        </h3>
        <ul class="plan__paths">
          ${c.overrides.map((path) => html`<li key=${path} class="mono">${path}</li>`)}
        </ul>
      </section>`
    }
    ${
      (c.hooks ?? []).length > 0 &&
      html`<section class="plan__section" data-testid="collection-hooks">
        <h3 class="plan__heading">Hooks - scripts lmm would run</h3>
        <ul class="plan__paths">
          ${c.hooks.map(
            (h) =>
              html`<li key=${h.hook} class="mono">
                ${h.hook}: ${h.command || "(disables the game's hook)"}
              </li>`,
          )}
        </ul>
        ${
          c.hooks_importable
            ? html`<label class="plan__control">
                  <input type="checkbox" onChange=${setWithHooks} />
                  Import these hooks
                </label>
                <p class="plan__note">
                  Left unchecked, the profile is imported without them.
                </p>`
            : html`<p class="plan__note">
                Hooks are never imported from a URL.
              </p>`
        }
      </section>`
    }
  `;
}

export function ProfileImportPlanView({ plan, modal, actions }) {
  const lmmCollection = plan.collection ?? null;
  const collection = plan.workshop_collection ?? null;
  const installed = plan.installed ?? [];
  // Issue 371: installed under ANOTHER profile. The bytes are cached, so
  // nothing is downloaded - but this profile still needs its own rows, so
  // these are pending work rather than "already installed".
  const alreadyCached = plan.already_cached ?? [];
  const needsRedownload = plan.needs_redownload ?? [];
  const missing = plan.missing ?? [];
  // A collection import installs nothing whatever this preview offers, so
  // it has no pending count to offer it for.
  const pending = collection
    ? 0
    : alreadyCached.length + needsRedownload.length + missing.length;
  // A Workshop item is tracked game-wide, so for a collection BOTH buckets
  // count as already present; an ordinary import counts only the rows this
  // profile itself already has.
  const alreadyPresent = collection
    ? installed.length + alreadyCached.length
    : installed.length;

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
          alreadyPresent === 0
            ? ""
            : // "tracked", not "installed", for a collection: an already
              // tracked Workshop item is not deployed into the new profile,
              // and a switch to it changes nothing (W2 review, Important 5).
              ` (${alreadyPresent} ${collection ? "item" : "mod"}${alreadyPresent === 1 ? "" : "s"} already ${collection ? "tracked" : "installed"})`
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
      ${
        lmmCollection &&
        html`<${LMMCollectionSection}
          collection=${lmmCollection}
          actions=${actions}
        />`
      }
      ${collection && html`<${CollectionHeader} collection=${collection} />`}
      ${
        collection
          ? html`<${CollectionItemList} items=${collection.items} />`
          : html`
              <${RefList} heading="Already installed" refs=${installed} />
              <${RefList}
                heading="Already downloaded, added to this profile"
                refs=${alreadyCached}
              />
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
