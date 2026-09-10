// profilesmodal.js - the profiles modal: list (default marked, mod counts),
// create, rename, delete, set-default, export, import
// (docs/plans/2026-08-31-serve-spa-design.md §Modals: "profiles (list/
// create/rename/delete/export/import/set-default)"), issue 332.
//
// Every mutation here but Import is one of api_profiles.go's SANCTIONED
// single-step writes - a create/delete/rename/set-default answers
// synchronously with the profile it produced, so this component calls
// api.js directly rather than through the confirm-plan framework. Delete
// and rename confirm INLINE, in place on the row, rather than as a second
// modal: "modals stack at most one deep" (design doc §Modals) leaves no
// room for a nested confirm dialog.
//
// Import is the one profile mutation that IS a real Plan/Apply pair
// (kind_profile_import.go) - picking a file hands off to the confirm-plan
// framework via actions.openPlan, which REPLACES this modal in the shared
// modal slot (store.js's own doc comment: "another shape in this same
// slot, not another slot"). Nothing here waits for that job; the user is
// back at Mission Control once it starts, same as every other mutation.
//
// core.ProfileResult's own `mods` can be null (never `[]`) for the ONE
// document this file renders that isn't from the list read: DELETE's
// "as it stood immediately before" snapshot for a profile whose own file
// failed to parse (task-A review note). Every read of `.mods` below goes
// through `(x.mods ?? [])` for that reason, not just for the list.

import { html, useEffect, useState } from "../render.js";
import { Modal } from "./modal.js";
import {
  createProfile,
  deleteProfile,
  renameProfile,
  setDefaultProfile,
  profileExportURL,
} from "../api.js";

/** ProfilesModal renders only when the shared modal slot holds this shape. */
export function ProfilesModal({ modal, state, actions }) {
  const open = modal?.type === "profiles";

  // The list is fetched into the store (actions.reloadProfiles), not local
  // state: the same document a re-open should show fresh, and the store is
  // already the one place a slow/failed fetch's error lives (fetchErrors).
  useEffect(() => {
    if (open) actions.reloadProfiles();
    // Intentionally re-fires only on open/close, not on every store change -
    // reloadProfiles is also called explicitly after every mutation below.
  }, [open]);

  if (!open) return null;

  const context = { game: state.route.game, profile: state.route.profile };
  const profiles = state.profiles?.profiles ?? [];
  const error = state.fetchErrors?.profiles;

  function close() {
    actions.closeModal();
  }

  /** afterMutation re-reads both the modal's own list AND the app-wide
   * status document: TopBar's game/profile pickers read status.profiles
   * (core.GameStatus), a document this modal never itself fetches, so a
   * create/rename/delete/set-default here would otherwise leave the top bar
   * showing a profile that no longer exists (or missing one that now does)
   * until the next unrelated re-hydrate. */
  async function afterMutation() {
    await Promise.all([actions.reloadProfiles(), actions.reloadStatus()]);
  }

  return html`
    <${Modal}
      kind="profiles"
      title="Manage profiles"
      onClose=${close}
      openerSelector=".profile-picker__trigger"
    >
      ${error && html`<p class="modal__error">Couldn't load profiles: ${error}</p>`}

      <ul class="profiles-list" data-testid="profiles-list">
        ${profiles.map(
          (p) => html`
            <${ProfileRow}
              key=${p.name}
              profile=${p}
              context=${context}
              actions=${actions}
              afterMutation=${afterMutation}
            />
          `,
        )}
      </ul>

      <${CreateProfileForm} context=${context} afterMutation=${afterMutation} />
      <${ImportProfileForm} actions=${actions} close=${close} />
    <//>
  `;
}

/** ProfileRow is one profile: its name/count/default marker, and its own
 * rename/delete/set-default/export affordances. Rename and delete each own
 * a small local "are you sure" / "editing" state, INLINE on this row -
 * exactly the substitute for a nested confirm modal this file's header
 * comment explains. */
function ProfileRow({ profile, context, actions, afterMutation }) {
  const [mode, setMode] = useState("view"); // "view" | "renaming" | "deleting"
  const [nameInput, setNameInput] = useState(profile.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);

  function reset() {
    setMode("view");
    setNameInput(profile.name);
    setError(null);
  }

  async function submitRename(e) {
    e.preventDefault();
    if (nameInput === profile.name) {
      reset();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await renameProfile(profile.name, nameInput, context);
      await afterMutation();
      reset();
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirmDelete() {
    setBusy(true);
    setError(null);
    try {
      await deleteProfile(profile.name, context);
      await afterMutation();
    } catch (err) {
      setError(err?.message ?? String(err));
      setBusy(false);
    }
  }

  async function makeDefault() {
    setBusy(true);
    setError(null);
    try {
      await setDefaultProfile(profile.name, context);
      await afterMutation();
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  // Purge and Sync are the C-3 pair, and the only two mutations in this
  // modal that go through the confirm-plan framework other than Import.
  // Both name THIS ROW's profile as the plan's own context rather than
  // relying on the selected one: kind_purge.go reads ?profile= and the plan
  // handle binds the profile at plan time, so a row for a profile you are
  // not currently in has to say so BEFORE the plan is computed. Opening a
  // plan replaces this modal in the shared slot ("modals stack at most one
  // deep"), exactly as Import already does.
  const rowContext = { game: context.game, profile: profile.name };

  function purgeProfile() {
    actions.openPlan({
      kind: "purge",
      origin: `profile:${profile.name}:purge`,
      title: `Purge ${profile.name}`,
      confirmLabel: "Purge",
      options: {},
      context: rowContext,
      openerSelector: `[data-action="purge-profile"][data-profile="${profile.name}"]`,
    });
  }

  function syncProfile() {
    actions.openPlan({
      kind: "profile_sync",
      origin: `profile:${profile.name}:sync`,
      title: `Sync ${profile.name}`,
      confirmLabel: "Sync",
      options: { profile: profile.name },
      context: rowContext,
      openerSelector: `[data-action="sync-profile"][data-profile="${profile.name}"]`,
    });
  }

  if (mode === "deleting") {
    return html`
      <li class="profiles-row profiles-row--confirm">
        <span
          >Delete
          <span class="mono">${profile.name}</span> (${profile.mod_count}
          mod${profile.mod_count === 1 ? "" : "s"})? This cannot be
          undone.</span
        >
        ${error && html`<p class="modal__error">${error}</p>`}
        <span class="profiles-row__actions">
          <button
            type="button"
            class="button button--danger button--small"
            disabled=${busy}
            onClick=${confirmDelete}
          >
            ${busy ? "Deleting…" : "Yes, delete"}
          </button>
          <button
            type="button"
            class="button button--small"
            disabled=${busy}
            onClick=${reset}
          >
            Cancel
          </button>
        </span>
      </li>
    `;
  }

  if (mode === "renaming") {
    return html`
      <li class="profiles-row">
        <form class="profiles-row__rename-form" onSubmit=${submitRename}>
          <input
            type="text"
            value=${nameInput}
            aria-label=${`Rename ${profile.name} to`}
            disabled=${busy}
            onInput=${(e) => setNameInput(e.currentTarget.value)}
          />
          <button
            type="submit"
            class="button button--small button--primary"
            disabled=${busy || !nameInput.trim()}
          >
            ${busy ? "Saving…" : "Save"}
          </button>
          <button
            type="button"
            class="button button--small"
            disabled=${busy}
            onClick=${reset}
          >
            Cancel
          </button>
        </form>
        ${error && html`<p class="modal__error">${error}</p>`}
      </li>
    `;
  }

  return html`
    <li class="profiles-row">
      <span class="profiles-row__name">
        ${profile.name}
        ${profile.is_default && html`<span class="badge">default</span>`}
      </span>
      <span class="profiles-row__count mono"
        >${profile.mod_count} mod${profile.mod_count === 1 ? "" : "s"}</span
      >
      <span class="profiles-row__actions">
        ${
          !profile.is_default &&
          html`<button
            type="button"
            class="button button--small"
            disabled=${busy}
            onClick=${makeDefault}
          >
            Set default
          </button>`
        }
        <a
          class="button button--small"
          href=${profileExportURL(profile.name, context)}
          download=${`${profile.name}.json`}
        >
          Export
        </a>
        <button
          type="button"
          class="button button--small"
          disabled=${busy}
          onClick=${() => setMode("renaming")}
        >
          Rename
        </button>
        <button
          type="button"
          class="button button--small"
          data-action="sync-profile"
          data-profile=${profile.name}
          disabled=${busy}
          onClick=${syncProfile}
        >
          Sync…
        </button>
        <button
          type="button"
          class="button button--small button--danger"
          data-action="purge-profile"
          data-profile=${profile.name}
          disabled=${busy}
          onClick=${purgeProfile}
        >
          Purge…
        </button>
        <button
          type="button"
          class="button button--small button--danger"
          disabled=${busy}
          onClick=${() => setMode("deleting")}
        >
          Delete
        </button>
      </span>
      ${error && html`<p class="modal__error">${error}</p>`}
    </li>
  `;
}

/** CreateProfileForm is the modal's own "new profile" row. */
function CreateProfileForm({ context, afterMutation }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);

  async function submit(e) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await createProfile(name.trim(), context);
      await afterMutation();
      setName("");
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  return html`
    <form class="profiles-create" onSubmit=${submit}>
      <h3 class="plan__heading">Create a profile</h3>
      <div class="profiles-create__row">
        <input
          type="text"
          placeholder="Profile name"
          aria-label="New profile name"
          value=${name}
          disabled=${busy}
          onInput=${(e) => setName(e.currentTarget.value)}
        />
        <button
          type="submit"
          class="button button--small button--primary"
          disabled=${busy || !name.trim()}
        >
          ${busy ? "Creating…" : "Create"}
        </button>
      </div>
      ${error && html`<p class="modal__error">${error}</p>`}
    </form>
  `;
}

/** ImportProfileForm reads the picked file as TEXT and hands it to the
 * confirm-plan framework as the profile-import kind's own request body
 * (kind_profile_import.go: "the SPA reads the file the user picked and
 * posts its contents"). It never uploads the file itself. */
function ImportProfileForm({ actions, close }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);

  async function onPick(e) {
    const file = e.currentTarget.files?.[0];
    e.currentTarget.value = "";
    if (!file) return;
    setBusy(true);
    setError(null);
    try {
      const data = await file.text();
      close();
      await actions.openPlan({
        kind: "profile_import",
        origin: "profile-import",
        title: `Import ${file.name}`,
        confirmLabel: "Import",
        options: { data },
      });
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  return html`
    <div class="profiles-import">
      <h3 class="plan__heading">Import a profile</h3>
      <label class="button button--small">
        ${busy ? "Reading…" : "Choose file…"}
        <input
          type="file"
          accept=".json,.yaml,.yml,application/json"
          disabled=${busy}
          onChange=${onPick}
          hidden
        />
      </label>
      ${error && html`<p class="modal__error">${error}</p>`}
      <${ImportCollectionForm} actions=${actions} close=${close} />
    </div>
  `;
}

/** ImportCollectionForm is the profiles modal's SECOND import input (issue
 * 269 W2): a Steam Workshop collection, by id or by the URL of its page.
 *
 * It is an import rather than a search facet because a collection IS a mod
 * list, which is what a profile is - surfacing it among search hits would
 * produce a row nobody can install as a unit. The reference is forwarded to
 * the server verbatim: the SOURCE decides what it recognises, and a second
 * parser here could only disagree with it.
 *
 * It needs no API key. Resolving a collection is one of Valve's keyless
 * endpoints, so this works for a user who has never run `lmm auth login
 * steamworkshop` - which the hint says, because the field sitting under a
 * Setup page full of key prompts otherwise implies the opposite. */
export function ImportCollectionForm({ actions, close }) {
  const [ref, setRef] = useState("");
  const [name, setName] = useState("");

  async function submit(e) {
    e.preventDefault();
    const collection = ref.trim();
    if (!collection) return;
    close();
    await actions.openPlan({
      kind: "profile_import",
      origin: "profile-import-collection",
      title: "Import Steam Workshop collection",
      confirmLabel: "Import",
      options: {
        workshop_collection: collection,
        ...(name.trim() ? { profile_name: name.trim() } : {}),
      },
    });
  }

  return html`
    <form class="profiles-import__collection" onSubmit=${submit}>
      <h4 class="plan__heading">Steam Workshop collection</h4>
      <input
        type="text"
        aria-label="Steam Workshop collection id or URL"
        placeholder="Collection id or URL"
        data-testid="collection-ref"
        value=${ref}
        onInput=${(e) => setRef(e.currentTarget.value)}
      />
      <input
        type="text"
        aria-label="Name for the imported profile"
        placeholder="Profile name (optional)"
        data-testid="collection-profile-name"
        value=${name}
        onInput=${(e) => setName(e.currentTarget.value)}
      />
      <button
        type="submit"
        class="button button--small button--primary"
        disabled=${!ref.trim()}
      >
        Import collection
      </button>
      <p class="empty-state__hint">
        No API key needed. Items you are already subscribed to are recorded as
        installed; the rest are listed with what to do about them.
      </p>
    </form>
  `;
}
