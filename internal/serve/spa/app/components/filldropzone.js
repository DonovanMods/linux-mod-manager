// filldropzone.js - "Fill from downloads": the archives you downloaded by
// hand for a collection profile's pending mods, matched and installed as
// `lmm profile fill` does (kind_profile_fill.go, issue 608). Two ways to
// hand them over:
//
//   - CHOOSE A FOLDER on this machine with the folder chooser
//     (folderpicker.js). The server scans it exactly as the CLI scans a
//     directory, and nothing is uploaded, so there is no size limit and no
//     limit on how many files match. It opens at the downloads folder.
//   - DROP (or pick) files. A browser cannot hand the server a path, so
//     the files travel (POST /api/v1/uploads) - and the size rule is
//     applied here first: a plan with no uploads answers the pending files'
//     sizes, and a file of no pending size is never sent, as the CLI never
//     reads one. A file over the upload cap is not sent either (the folder
//     is the way for it), and what is left goes up at most maxFillUploads
//     at a time (the server holds that many uploads at once); the rest wait
//     for the next drop, and the plan view says how many.
//
// It is shown on a finished collection import's result in the activity
// tray, and on each collection profile in the profiles modal.

import { html, useEffect, useState } from "../render.js";
import {
  deleteUpload,
  getDownloadsFolder,
  maxUploadBytes,
  plan as planMutation,
  uploadArchive,
} from "../api.js";
import { BrowseButton } from "./folderpicker.js";

// maxFillUploads mirrors the server's upload store cap (uploads.go's
// defaultUploadStoreCap, kind_profile_fill.go's maxFillUploads): a plan
// naming more would name uploads the store has already let go.
// api_js_constants_internal_test.go holds the two equal.
export const maxFillUploads = 4;

// unmatchable are the pending reasons no file can fill: their sizes are no
// reason to upload anything.
const unmatchable = new Set(["no_checksum", "disabled"]);

/** pendingSizes is every size a fill plan's matchable pending files have. */
export function pendingSizes(plan) {
  const sizes = new Set();
  for (const p of plan?.pending ?? []) {
    if (unmatchable.has(p.reason)) continue;
    for (const f of p.files ?? []) {
      if (f.size > 0) sizes.add(f.size);
    }
  }
  return sizes;
}

/** splitForFill sorts files into the ones to upload (a pending file's size,
 * within the upload cap), at most maxFillUploads of them, the further ones
 * that wait for another drop, the ones too large to upload, and the ones no
 * pending file could be. */
export function splitForFill(files, sizes) {
  const candidates = files.filter((f) => sizes.has(f.size));
  const uploadable = candidates.filter((f) => f.size <= maxUploadBytes);
  return {
    batch: uploadable.slice(0, maxFillUploads),
    later: uploadable.slice(maxFillUploads),
    tooLarge: candidates.filter((f) => f.size > maxUploadBytes),
    unused: files.filter((f) => !sizes.has(f.size)),
  };
}

/** FillDropZone is the folder chooser, drop target and file picker for
 * profile's fill. */
export function FillDropZone({ profile, game, actions, onStarted }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);
  const [error, setError] = useState(null);
  const [over, setOver] = useState(false);
  const [downloads, setDownloads] = useState("");
  const context = { game, profile };

  // Where the folder chooser opens: the downloads folder, a cheap read
  // (no plan is made until the user hands over files or a folder).
  useEffect(() => {
    let live = true;
    getDownloadsFolder()
      .then((doc) => {
        if (live) setDownloads(doc?.path ?? "");
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [profile, game]);

  function open(options, extra) {
    onStarted?.();
    return actions.openPlan({
      kind: "profile_fill",
      origin: `profile:${profile}:fill`,
      title: `Install downloads into ${profile}`,
      confirmLabel: "Install",
      options,
      context,
      extra,
    });
  }

  async function fill(list) {
    const files = Array.from(list ?? []);
    if (files.length === 0 || busy) return;
    setBusy(true);
    setError(null);
    const ids = [];
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
      const { batch, later, tooLarge, unused } = splitForFill(files, sizes);
      if (batch.length === 0) {
        setStatus(null);
        setError(
          tooLarge.length > 0
            ? `${tooLarge.map((f) => f.name).join(", ")}: too large to upload (over 2 GiB). Choose the folder it is in instead, and nothing is uploaded.`
            : `None of these ${files.length} file(s) is the size of a pending download of ${profile}, so none was uploaded.`,
        );
        return;
      }
      for (const [i, file] of batch.entries()) {
        setStatus(`Uploading ${i + 1} of ${batch.length}: ${file.name}`);
        const staged = await uploadArchive(file);
        ids.push(staged.upload_id);
      }
      setStatus(null);
      await open(
        { upload_ids: ids },
        {
          notUploaded: unused.map((f) => f.name),
          tooLarge: tooLarge.map((f) => f.name),
          later: later.map((f) => f.name),
        },
      );
    } catch (err) {
      // The batch's earlier uploads are no use without the rest.
      for (const id of ids) deleteUpload(id).catch(() => {});
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
      data-downloads=${downloads || null}
      onDragOver=${(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave=${() => setOver(false)}
      onDrop=${onDrop}
    >
      <p class="fill-drop__hint">
        Install${" "}<span class="mono">${profile}</span>'s pending mods from
        the files you downloaded: choose the folder they are in, or drop them
        here.
      </p>
      <${BrowseButton}
        start=${() => downloads}
        label="the folder you downloaded the files to"
        text="Choose a folder…"
        action="fill-folder"
        disabled=${busy}
        onChoose=${(path) => open({ folder: path }, {})}
      />
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
