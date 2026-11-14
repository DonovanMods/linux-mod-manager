// plan_collection_update.js - the confirm modal's renderer for a
// core.CollectionUpdatePlan (kind_collection_update.go, `lmm profile sync
// --collection`, issue 613): a collection profile updated from its origin.
//
// The plan is four buckets and the new version's gate. Two of the buckets
// are questions, and each of their rows carries the answer as a checkbox:
//
//   - removed: mods the new version no longer lists. Kept, as the user's,
//     unless checked - then removed and uninstalled (`--remove`);
//   - conflicts: mods the user locked or edited that the new version
//     changes. The user's version is kept unless checked - then the entry
//     takes the collection's (`--override`).
//
// Both answers are APPLY-time options (kind_collection_update.go), so a
// checkbox never re-fetches the collection: it patches the modal's
// applyOptions. Checking every row is the CLI's --remove-obsolete /
// --override-conflicts. Every string was sanitised by core.

import { html } from "../render.js";
import { displayVersion } from "../version.js";
import { PlanWarnings } from "./planwarnings.js";
import { SourceCollectionSection } from "./plan_profile_import.js";

/** keyOf is an entry's "source:mod" key, the one the options name. */
function keyOf(it) {
  return `${it.source_id}:${it.mod_id}`;
}

/** labelOf names an entry: its display name and key. */
function labelOf(it) {
  return it.name ? `${it.name} (${keyOf(it)})` : keyOf(it);
}

/** move is "1.0 → 1.1", or the one side an entry has - each side through
 * displayVersion, with core's display stamp for a Workshop item. */
function move(it) {
  const from = displayVersion(it);
  const to = displayVersion({
    version: it.new_version,
    display_version: it.new_display_version,
  });
  if (from && to && from !== to) return `${from} → ${to}`;
  return to || from;
}

/** changes words an entry's change list. */
function changes(it) {
  return (it.changes ?? []).map((c) => c.replaceAll("_", " ")).join(", ");
}

/** EntryList renders a bucket with no question. */
function EntryList({ heading, items, testID }) {
  if (!items || items.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${items.length})</h3>
      <ul class="plan__mods" data-testid=${testID}>
        ${items.map(
          (it) => html`
            <li key=${keyOf(it)} class="plan__mod">
              <span class="plan__mod-name">${labelOf(it)}</span>${" "}
              <span class="mono">${move(it)}</span>
              ${
                // htm-ws-ok: the ${" "} above already carries the gap.
                it.changes?.length > 0 &&
                html` <span class="plan__mod-detail">(${changes(it)})</span>`
              }
              ${it.note && html`<p class="plan__note">${it.note}</p>`}
            </li>
          `,
        )}
      </ul>
    </section>
  `;
}

/** ChoiceList renders a bucket whose rows each carry a checkbox writing
 * their key into the apply option `option`. */
function ChoiceList({
  heading,
  note,
  items,
  option,
  checkLabel,
  modal,
  actions,
  testID,
}) {
  if (!items || items.length === 0) return null;
  const chosen = new Set(modal?.applyOptions?.[option] ?? []);
  function toggle(key, on) {
    const next = new Set(chosen);
    if (on) next.add(key);
    else next.delete(key);
    actions.setPlanOptions({ [option]: [...next].sort() });
  }
  return html`
    <section class="plan__section" data-testid=${testID}>
      <h3 class="plan__heading">${heading} (${items.length})</h3>
      <p class="plan__note">${note}</p>
      <ul class="plan__mods">
        ${items.map((it) => {
          const key = keyOf(it);
          return html`
            <li key=${key} class="plan__mod">
              <span class="plan__mod-name">${labelOf(it)}</span>${" "}
              <span class="mono">${move(it)}</span>
              ${
                // htm-ws-ok: the ${" "} above already carries the gap.
                it.changes?.length > 0 &&
                html` <span class="plan__mod-detail"
                  >(${changes(it)}${it.locked ? ", locked" : ""})</span
                >`
              }
              <label class="plan__control plan__control--inline">
                <input
                  type="checkbox"
                  data-key=${key}
                  checked=${chosen.has(key)}
                  disabled=${modal?.status !== "ready"}
                  onChange=${(e) => toggle(key, e.currentTarget.checked)}
                />
                ${checkLabel}
              </label>
              ${it.note && html`<p class="plan__note">${it.note}</p>`}
            </li>
          `;
        })}
      </ul>
    </section>
  `;
}

/** ConflictBox is one conflict's checkbox: checked, its key is in the
 * apply option "override" and the collection's side is taken. */
function ConflictBox({ conflictKey, label, modal, actions }) {
  const chosen = new Set(modal?.applyOptions?.override ?? []);
  function toggle(on) {
    const next = new Set(chosen);
    if (on) next.add(conflictKey);
    else next.delete(conflictKey);
    actions.setPlanOptions({ override: [...next].sort() });
  }
  return html`
    <label class="plan__control plan__control--inline">
      <input
        type="checkbox"
        data-key=${conflictKey}
        checked=${chosen.has(conflictKey)}
        disabled=${modal?.status !== "ready"}
        onChange=${(e) => toggle(e.currentTarget.checked)}
      />
      ${label}
    </label>
  `;
}

/** PluginChange names what the update does to the plugin list, and each
 * conflict with its checkbox. */
function PluginChange({ plugins: p, modal, actions }) {
  if (!p) return null;
  const changed = [];
  if (p.reordered) changed.push("reordered");
  if (p.rules_changed) changed.push("rules changed");
  return html`
    <section class="plan__section" data-testid="update-plugins">
      <h3 class="plan__heading">Plugin order</h3>
      ${
        p.added?.length > 0 &&
        html`<p class="plan__note">Added: ${p.added.join(", ")}</p>`
      }
      ${
        p.removed?.length > 0 &&
        html`<p class="plan__note">Dropped: ${p.removed.join(", ")}</p>`
      }
      ${
        changed.length > 0 &&
        html`<p class="plan__note">The list is ${changed.join(", ")}.</p>`
      }
      ${
        (p.conflicts?.length > 0 || p.moved?.length > 0 || p.rules_conflict) &&
        html`<p class="plan__note">
            You and the collection both changed these; yours is kept unless you
            take the collection's.
          </p>
          <ul class="plan__mods" data-testid="update-plugin-conflicts">
            ${(p.conflicts ?? []).map(
              (name) =>
                html`<li key=${name} class="plan__mod">
                  <span class="plan__mod-name">${name}</span>${" "}
                  <span class="plan__mod-detail">(switched on or off)</span>
                  <${ConflictBox}
                    conflictKey=${`plugin:${name}`}
                    label="Take the collection's"
                    modal=${modal}
                    actions=${actions}
                  />
                </li>`,
            )}
            ${
              p.moved?.length > 0 &&
              html`<li class="plan__mod">
                <span class="plan__mod-name"
                  >Order of ${p.moved.join(", ")}</span
                >
                <${ConflictBox}
                  conflictKey="plugins:order"
                  label="Take the collection's order"
                  modal=${modal}
                  actions=${actions}
                />
              </li>`
            }
            ${
              p.rules_conflict &&
              html`<li class="plan__mod">
                <span class="plan__mod-name">Plugin rules</span>
                <${ConflictBox}
                  conflictKey="plugins:rules"
                  label="Take the collection's rules"
                  modal=${modal}
                  actions=${actions}
                />
              </li>`
            }
          </ul>`
      }
    </section>
  `;
}

/** External names the items to get by hand the new version adds and
 * drops. */
function External({ plan }) {
  const added = plan.external_added ?? [];
  const removed = plan.external_removed ?? [];
  if (added.length === 0 && removed.length === 0) return null;
  return html`
    <section class="plan__section" data-testid="update-external">
      <h3 class="plan__heading">Items to get by hand</h3>
      <ul class="plan__mods">
        ${added.map(
          (e) =>
            html`<li key=${`+${e.name}`} class="plan__mod">
              <span class="plan__mod-name">${e.name}</span>${" "}
              <span class="plan__mod-detail">(new in this version)</span>
            </li>`,
        )}
        ${removed.map(
          (e) =>
            html`<li key=${`-${e.name}`} class="plan__mod">
              <span class="plan__mod-name">${e.name}</span>${" "}
              <span class="plan__mod-detail"
                >(no longer in the collection: remove it yourself if you
                installed it)</span
              >
            </li>`,
        )}
      </ul>
    </section>
  `;
}

/** Gate renders the new version's overrides, hooks and missing features. */
function Gate({ plan, modal, actions }) {
  const touched = [
    ...(plan.overrides_added ?? []),
    ...(plan.overrides_changed ?? []),
  ];
  const removedOverrides = plan.overrides_removed ?? [];
  const overrideConflicts = plan.override_conflicts ?? [];
  const restricted = plan.restricted_overrides ?? [];
  const hooks = plan.hooks ?? [];
  const unsupported = plan.unsupported ?? [];
  return html`
    ${
      (touched.length > 0 ||
        removedOverrides.length > 0 ||
        overrideConflicts.length > 0) &&
      html`<section class="plan__section" data-testid="update-overrides">
        <h3 class="plan__heading">Config overrides in the game directory</h3>
        <ul class="plan__paths">
          ${touched.map((path) => html`<li key=${path} class="mono">${path}</li>`)}
          ${removedOverrides.map(
            (path) =>
              html`<li key=${`-${path}`}>
                <span class="mono">${path}</span> (no longer in the collection:
                removed)
              </li>`,
          )}
          ${overrideConflicts.map(
            (path) =>
              html`<li key=${`!${path}`}>
                <span class="mono">${path}</span> (you and the collection both
                changed it; yours is kept unless you take the collection's)
                <${ConflictBox}
                  conflictKey=${`override:${path}`}
                  label="Take the collection's"
                  modal=${modal}
                  actions=${actions}
                />
              </li>`,
          )}
        </ul>
      </section>`
    }
    ${
      restricted.length > 0 &&
      html`<section
        class="plan__section"
        data-testid="update-restricted-overrides"
      >
        <h3 class="plan__heading">Overrides that are not text config files</h3>
        <ul class="plan__paths">
          ${restricted.map(
            (o) =>
              html`<li key=${o.path}>
                <span class="mono">${o.path}</span>: ${o.reason}
              </li>`,
          )}
        </ul>
        <label class="plan__control">
          <input
            type="checkbox"
            name="allow_any_overrides"
            checked=${Boolean(modal?.applyOptions?.allow_any_overrides)}
            disabled=${modal?.status !== "ready"}
            onChange=${(e) =>
              actions.setPlanOptions({
                allow_any_overrides: e.currentTarget.checked,
              })}
          />
          Import these overrides too
        </label>
      </section>`
    }
    ${
      hooks.length > 0 &&
      html`<section class="plan__section" data-testid="update-hooks">
        <h3 class="plan__heading">Hooks the new version carries</h3>
        <ul class="plan__paths">
          ${hooks.map(
            (h) =>
              html`<li key=${h.hook} class="mono">${h.hook}: ${h.command}</li>`,
          )}
        </ul>
        <p class="plan__note">
          An update never applies a collection's hooks; the profile keeps its
          own.
        </p>
      </section>`
    }
    ${
      unsupported.length > 0 &&
      html`<section class="plan__section" data-testid="update-unsupported">
        <h3 class="plan__heading">
          Features this version of lmm cannot import
        </h3>
        <ul class="plan__paths">
          ${unsupported.map(
            (f) => html`<li key=${f.feature} class="mono">${f.feature}</li>`,
          )}
        </ul>
        <p class="plan__note">
          The update is refused until lmm can replay them: update lmm.
        </p>
      </section>`
    }
  `;
}

/** CollectionUpdatePlanView renders core.CollectionUpdatePlan. */
export function CollectionUpdatePlanView({ plan, modal, actions }) {
  const from =
    plan.origin === "url" ? plan.url : `${plan.source_id} ${plan.slug}`;
  if (plan.up_to_date) {
    return html`
      <div class="plan plan--collection-update">
        <p class="plan__summary" data-testid="update-up-to-date">
          <span class="mono">${plan.profile}</span>${" "}
          ${`is up to date with its collection (version ${plan.version}).`}
        </p>
        <p class="plan__note">From <span class="mono">${from}</span>.</p>
        <${PlanWarnings} warnings=${plan.warnings} testID="update-warnings" />
      </div>
    `;
  }
  return html`
    <div class="plan plan--collection-update">
      <p class="plan__summary" data-testid="update-summary">
        Updating <span class="mono">${plan.profile}</span>${" "}
        ${`to version ${plan.new_version} of ${plan.name} (it has version ${plan.version}).`}
      </p>
      <p class="plan__note">From <span class="mono">${from}</span>.</p>
      <${PlanWarnings} warnings=${plan.warnings} testID="update-warnings" />
      ${
        plan.install_instructions &&
        html`<p class="plan__note">
          <strong>Install instructions:</strong> ${plan.install_instructions}
        </p>`
      }
      ${
        plan.no_changes &&
        html`<p class="plan__note">
          The new version changes none of the profile's mods; the update records
          it.
        </p>`
      }
      <${EntryList}
        heading="Will be added"
        items=${plan.added}
        testID="update-added"
      />
      <${EntryList}
        heading="Will move to the collection's new pin"
        items=${plan.changed}
        testID="update-changed"
      />
      <${ChoiceList}
        heading="No longer in the collection"
        note="Kept as your own mods unless you choose to remove them."
        items=${plan.removed}
        option="remove"
        checkLabel="Remove and uninstall"
        modal=${modal}
        actions=${actions}
        testID="update-removed"
      />
      <${ChoiceList}
        heading="Conflicts - mods you locked or edited"
        note="The collection changes these. Your version is kept unless you take the collection's."
        items=${plan.conflicts}
        option="override"
        checkLabel="Take the collection's version"
        modal=${modal}
        actions=${actions}
        testID="update-conflicts"
      />
      <${PluginChange}
        plugins=${plan.plugins}
        modal=${modal}
        actions=${actions}
      />
      <${External} plan=${plan} />
      <${Gate} plan=${plan} modal=${modal} actions=${actions} />
      ${
        plan.source &&
        html`<${SourceCollectionSection}
          source=${plan.source}
          actions=${actions}
        />`
      }
      ${
        plan.recorded_only &&
        html`<p class="plan__note">
          ${`${plan.profile} is not the active profile (${plan.active_profile} is): the update records and downloads, and deploys nothing.`}
        </p>`
      }
    </div>
  `;
}
