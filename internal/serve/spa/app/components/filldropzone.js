// filldropzone.js - "Fill from downloads": drop (or pick) the archives you
// downloaded by hand for a collection profile's pending mods, and they are
// matched and installed as `lmm profile fill` does (kind_profile_fill.go,
// issue 608).
//
// The server matches by size and then md5, against what the collection
// records for each pending file. A browser cannot hand the server a path,
// so the files travel (POST /api/v1/uploads) - and the size rule is applied
// here first, before anything is uploaded: a plan with no uploads answers
// the pending files' sizes, and a dropped file of no pending size is never
// sent, as the CLI never reads one. What is left goes up at most
// maxFillUploads at a time (the server holds that many uploads at once,
// uploads.go's defaultUploadStoreCap); the rest wait for the next drop, and
// the plan view says how many.
//
// It is shown on a finished collection import's result in the activity
// tray, and on each collection profile in the profiles modal.

import { html, useState } from "../render.js";
import { plan as planMutation, uploadArchive } from "../api.js";

// maxFillUploads mirrors the server's upload store cap (uploads.go's
// defaultUploadStoreCap, kind_profile_fill.go's maxFillUploads): a plan
// naming more would name uploads the store has already let go.
export const maxFillUploads = 4;

/** pendingSizes is every size a fill plan's pending files have. */
export function pendingSizes(plan) {
  const sizes = new Set();
  for (const p of plan?.pending ?? []) {
    for (const f of p.files ?? []) {
      if (f.size > 0) sizes.add(f.size);
    }
  }
  return sizes;
}

/** splitForFill sorts files into the ones worth uploading (a pending
 * file's size), at most maxFillUploads of them, the further ones that wait
 * for another drop, and the ones no pending file could be. */
export function splitForFill(files, sizes) {
  const candidates = files.filter((f) => sizes.has(f.size));
  return {
    batch: candidates.slice(0, maxFillUploads),
    later: candidates.slice(maxFillUploads),
    unused: files.filter((f) => !sizes.has(f.size)),
  };
}

/** FillDropZone is the drop target and file picker for profile's fill. */
export function FillDropZone({ profile, game, actions, onStarted }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);
  const [error, setError] = useState(null);
  const [over, setOver] = useState(false);
  const context = { game, profile };

  async function fill(list) {
    const files = Array.from(list ?? []);
    if (files.length === 0 || busy) return;
    setBusy(true);
    setError(null);
    try {
      setStatus("Checking what is still pending…");
      const probe = await planMutation("profile_fill", {}, context);
      const sizes = pendingSizes(probe.plan);
      if (sizes.size === 0) {
        setStatus(null);
        setError(
          `${profile} has no pending download a file could be matched to.`,
        );
        return;
      }
      const { batch, later, unused } = splitForFill(files, sizes);
      if (batch.length === 0) {
        setStatus(null);
        setError(
          `None of these ${files.length} file(s) is the size of a pending download of ${profile}, so none was uploaded.`,
        );
        return;
      }
      const ids = [];
      for (const [i, file] of batch.entries()) {
        setStatus(`Uploading ${i + 1} of ${batch.length}: ${file.name}`);
        const staged = await uploadArchive(file);
        ids.push(staged.upload_id);
      }
      setStatus(null);
      onStarted?.();
      await actions.openPlan({
        kind: "profile_fill",
        origin: `profile:${profile}:fill`,
        title: `Install downloads into ${profile}`,
        confirmLabel: "Install",
        options: { upload_ids: ids },
        context,
        extra: {
          notUploaded: unused.map((f) => f.name),
          later: later.map((f) => f.name),
        },
      });
    } catch (err) {
      setStatus(null);
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  function onDrop(e) {
    e.preventDefault();
    setOver(false);
    fill(e.dataTransfer?.files);
  }

  return html`
    <div
      class="fill-drop ${over ? "fill-drop--over" : ""}"
      data-testid="fill-drop"
      data-profile=${profile}
      onDragOver=${(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave=${() => setOver(false)}
      onDrop=${onDrop}
    >
      <p class="fill-drop__hint">
        Drop the archives you downloaded for${" "}
        <span class="mono">${profile}</span>'s pending mods here, or
      </p>
      <label class="button button--small">
        ${busy ? "Working…" : "Choose files…"}
        <input
          type="file"
          multiple
          data-testid="fill-drop-input"
          disabled=${busy}
          onChange=${(e) => {
            const picked = Array.from(e.currentTarget.files ?? []);
            e.currentTarget.value = "";
            fill(picked);
          }}
          hidden
        />
      </label>
      ${status && html`<p class="fill-drop__status" role="status">${status}</p>`}
      ${error && html`<p class="modal__error" data-testid="fill-drop-error">${error}</p>`}
    </div>
  `;
}
