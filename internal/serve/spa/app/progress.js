// progress.js - turning one activity frame into the words and the fraction
// a progress control renders. Pure, and deliberately separate from every
// component that shows progress, because the SAME frame is rendered in
// three places (the morphing control, the tray row, the library's live
// count) and three copies of this vocabulary would drift.
//
// The input is internal/serve/activity.go's jobProgressFrame - a flat
// summary of one core event, already carrying the phase, the mod, the batch
// position and a download's bytes. Nothing here re-derives any of that from
// the typed core event, which is the whole reason that frame exists.

// phasePrefixes strips the FLOW half of a core phase name, which is
// redundant in a UI that already knows which job it is rendering: a deploy
// job's "deploy_downloading" is just "Downloading". Longest prefix first,
// so "install_dep_" wins over "install_".
//
// Two prefixes are replaced rather than dropped, because their remainder
// alone would be a lie: an install's DEPENDENCY phases and an archive
// import's phases both describe something other than the mod the job is
// about (internal/core/phases.go's own grouping).
const phasePrefixes = [
  ["import_archive_", "Archive "],
  ["install_dep_", "Dependency "],
  ["deploy_", ""],
  ["install_", ""],
  ["update_", ""],
  ["switch_", ""],
  ["purge_", ""],
  ["import_", ""],
  ["adopt_", ""],
  ["sync_", ""],
  ["relink_", ""],
  ["mod_copy_", ""],
  ["snapshot_", ""],
  // issue 436: what a source is waiting on - "Retrying" and "Suspended",
  // with core's own sentence as the detail.
  ["source_", ""],
];

// phaseLabels names the few phases whose humanized name would read wrong.
// issue 664: an update's steps read "Extracting · OpenMap", "Updated ·
// OpenMap" - not "Archive extracting" (archive_extracting is one phase for
// every flow that extracts a download) or "Mod updated" - and its batch
// bracket reads "Updating"/"Updated" rather than "Batch item"/"Batch item
// applied".
const phaseLabels = {
  archive_extracting: "Extracting",
  // A flow's "download done" fires once the file is in the cache - after
  // its extraction - so it reads "Downloaded", never "Download done" after
  // "Extracting" (issue 664 review).
  update_download_done: "Downloaded",
  deploy_download_done: "Downloaded",
  switch_download_done: "Downloaded",
  import_download_done: "Downloaded",
  install_download_done: "Downloaded",
  install_dep_download_done: "Dependency downloaded",
  update_mod_updated: "Updated",
  update_batch_item: "Updating",
  update_batch_item_applied: "Updated",
  update_batch_item_skipped: "Skipped",
  update_batch_item_failed: "Failed",
  // issue 666: an import going on to switch to the profile it imported.
  import_switching: "Switching to profile",
};

/**
 * Humanizes a core phase name ("deploy_merge_synced" -> "Merge synced").
 *
 * A phase this function has never seen still renders - as its own name with
 * the underscores opened up - which is what makes it safe for core to add
 * one without the SPA needing a matching release. That is the design's
 * "humanized" (§Jobs) read as a rule about the vocabulary rather than a
 * hand-maintained table of every one of the ~90 phases in phases.go;
 * phaseLabels holds only the exceptions.
 */
export function humanizePhase(phase) {
  if (!phase) return "";
  if (phaseLabels[phase]) return phaseLabels[phase];
  let rest = phase;
  for (const [prefix, replacement] of phasePrefixes) {
    if (rest.startsWith(prefix)) {
      rest = replacement + rest.slice(prefix.length);
      break;
    }
  }
  const words = rest.replaceAll("_", " ").trim();
  return words ? words[0].toUpperCase() + words.slice(1) : "";
}

// mutationKindLabels names each registered job kind (planKinds/toggleKinds,
// internal/serve/plankinds.go and kind_toggle.go) in the present participle
// a live indicator reads naturally with ("Deploying…", not "Deploy…").
// issue 330 carry-3: with several kinds able to run at once, the library's
// live line used to read only the humanized PHASE - which is empty for a
// kind that reports no progress events at all (enable/disable/uninstall
// all run with no core.EventSink, kind_toggle.go's and kind_uninstall.go's
// own doc comments) - so a running uninstall rendered nothing rather than
// naming itself.
const mutationKindLabels = {
  deploy: "Deploying",
  install: "Installing",
  uninstall: "Uninstalling",
  updates: "Updating",
  rollback: "Rolling back",
  switch: "Switching profile",
  profile_apply: "Applying profile",
  verify_fix: "Repairing",
  profile_import: "Importing profile",
  profile_fill: "Installing downloads",
  collection_finish: "Finishing collection",
  enable: "Enabling",
  disable: "Disabling",
  snapshot_restore: "Restoring snapshot",
  mod_copy: "Copying to profile",
  catalog_relink: "Re-linking",
};

/** mutationLabel names a running job's KIND in words, falling back to the
 * kind's own wire name (underscores opened up) for one this vocabulary
 * hasn't seen - the same "still renders, just not humanized" safety
 * humanizePhase gives an unrecognized phase, so a kind added later needs no
 * matching SPA release to show something sensible. */
export function mutationLabel(kind, origin) {
  // issue 587: the automatic update pass is an ordinary updates job that
  // no one clicked for, so it says so.
  if (kind === "updates" && origin === "auto") return "Updating automatically";
  // issue 590: the automatic re-link of a mod whose source gave it a new ID.
  if (kind === "catalog_relink" && origin === "auto") {
    return "Re-linking automatically";
  }
  const label = mutationKindLabels[kind] ?? humanizePhase(kind);
  // An automatic job of a kind with no wording of its own above still
  // says no one clicked for it.
  return origin === "auto" ? `${label} (automatic)` : label;
}

// jobKindTitles names each job kind as a noun, for a sentence about the
// job as a whole - a toast's "Deploy failed", a tray row (issue 655: a toast
// read "profile_import failed", the raw wire name). profile_import is the
// one kind with two names: jobTitle says "Collection import" when the job
// imported a collection.
const jobKindTitles = {
  adopt: "Adopt",
  catalog_relink: "Re-link",
  collection_finish: "Finish collection",
  collection_update: "Collection update",
  deploy: "Deploy",
  disable: "Disable",
  enable: "Enable",
  import_archive: "Install from file",
  install: "Install",
  mod_copy: "Copy to profile",
  mod_fomod: "Installer reconfiguration",
  mod_relink: "Mod re-link",
  profile_apply: "Profile apply",
  profile_fill: "Install from downloads",
  profile_import: "Profile import",
  profile_sync: "Profile sync",
  purge: "Purge",
  rollback: "Rollback",
  snapshot_restore: "Snapshot restore",
  switch: "Profile switch",
  uninstall: "Uninstall",
  update_from_archive: "Update from file",
  updates: "Update",
  verify_fix: "Repair",
  workshop_adopt: "Workshop adopt",
};

/** jobTitle names a job's kind as a noun ("Deploy", "Collection import"),
 * falling back to the wire name opened up for a kind this table has not
 * seen. `collection` marks a profile_import that imported a collection;
 * `origin` "auto" says no one clicked for it. */
export function jobTitle(kind, { collection = false, origin = "" } = {}) {
  let title =
    kind === "profile_import" && collection
      ? "Collection import"
      : (jobKindTitles[kind] ?? humanizePhase(kind));
  if (!title) title = "Job";
  return origin === "auto" ? `${title} (automatic)` : title;
}

/** jobRunningLabel is mutationLabel for a job this page knows more about:
 * a profile_import that imports a collection reads "Importing collection"
 * (issue 655). */
export function jobRunningLabel(
  kind,
  { collection = false, origin = "" } = {},
) {
  const automatic = origin === "auto";
  if (kind === "profile_import" && collection && !automatic) {
    return "Importing collection";
  }
  return mutationLabel(kind, origin);
}

/** formatBytes renders a byte count at the largest unit that keeps it
 * readable. Used for a download whose total size is unknown, where the byte
 * counter is the only thing that can move (activity.go's frame vocabulary:
 * a Content-Length-less download reports Percent 0 throughout). */
export function formatBytes(bytes) {
  if (!bytes || bytes < 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

/** downloadText is a download tick's own readout: a percentage when the
 * total size is known, the transferred bytes when it is not, and nothing
 * at all for an event that is not a download. */
export function downloadText(frame) {
  if (!frame || frame.type !== "download") return "";
  if (frame.total_bytes > 0) {
    return `${Math.round(frame.percent ?? 0)}% of ${formatBytes(frame.total_bytes)}`;
  }
  const moved = formatBytes(frame.downloaded);
  return moved ? `${moved} transferred` : "";
}

/**
 * progressText is the one line a progress control shows: what is happening,
 * to which mod, where in the batch, and how far through the bytes. Every
 * part is omitted when the frame does not carry it, so a phase-only event
 * reads as a phase rather than as a phase with empty brackets after it.
 */
export function progressText(frame) {
  return progressLines(frame).filter(Boolean).join(" · ");
}

/**
 * progressLines is progressText in two halves, for a control that gives
 * each its own fixed line (issue 670, the activity tray): what is happening
 * and where - the phase, the mod, its place in the batch - and then what
 * moves within that step - the download's bytes and the event's own words.
 * Either may be empty; joined with " · " they are progressText.
 */
export function progressLines(frame) {
  if (!frame) return ["", ""];
  const where = [];
  const phase = humanizePhase(frame.phase);
  if (phase) where.push(phase);
  if (frame.mod_name) where.push(frame.mod_name);
  if (frame.total > 0) where.push(`${frame.index ?? 0} of ${frame.total}`);

  const doing = [];
  const bytes = downloadText(frame);
  if (bytes) doing.push(bytes);
  // The detail line is the event's own words (a warning's message, a step's
  // note). It is added last and only when it is not simply repeating the
  // phase this frame already rendered.
  if (frame.detail && frame.detail !== phase) doing.push(frame.detail);

  const first = where.join(" · ");
  const second = doing.join(" · ");
  if (!first && !second) return [humanizePhase(frame.type), ""];
  return [first, second];
}

// UPDATE_STEPS is how far through ONE mod's update each of its steps is
// (issue 664), in the order core emits them (internal/core/phases.go). The
// first ACQUIRE_SHARE is acquiring the mod's files - each one downloaded
// and then extracted, one file after another (downloadModFiles) - and the
// steps after it, which used to leave the bar sitting at the download's
// last percentage, take it the rest of the way. The weights are positions,
// not timings: what they promise is that each step moves the bar forward
// and the last one fills it.
const ACQUIRE_SHARE = 0.55;
// DOWNLOAD_PART is how much of one file's share of ACQUIRE_SHARE its
// download fills; its extraction ends the share.
const DOWNLOAD_PART = 0.9;
const UPDATE_STEPS = {
  update_batch_item: 0,
  update_downloading: 0,
  archive_extracting: ACQUIRE_SHARE,
  update_download_done: 0.6,
  update_running_hook: 0.65,
  update_installing: 0.75,
  update_recording: 0.9,
  update_mod_updated: 1,
  update_batch_item_applied: 1,
  update_batch_item_skipped: 1,
  update_batch_item_failed: 1,
};

/** acquireStep is where a download tick or an extracting step puts one mod's
 * update, 0..ACQUIRE_SHARE: within its file's share, by the frame's file
 * position (core.FilePosition - file_index of file_total; a frame without
 * one is the mod's only file). */
function acquireStep(frame) {
  const total = frame.file_total > 0 ? frame.file_total : 1;
  const index = Math.min(Math.max(frame.file_index ?? 1, 1), total);
  const share = ACQUIRE_SHARE / total;
  const start = (index - 1) * share;
  if (frame.phase === "archive_extracting") return start + share;
  return (
    start + share * DOWNLOAD_PART * (clamp((frame.percent ?? 0) / 100) ?? 0)
  );
}

// reached is the furthest each update item's bar has got, keyed by job and
// the item's batch position (issue 664 review): a backstop, so a frame that
// would place it earlier - one without the file position that orders it -
// holds the bar instead of sending it back, and an update frame that is not
// a step (a warning, a note, a Workshop fetch's heartbeat) holds it where
// the last step left it. Bounded: the oldest entries go first.
const reached = new Map();
const REACHED_LIMIT = 200;

function reachedKey(frame, position, counted) {
  if (!frame.job_id) return null;
  return `${frame.job_id}|${counted ? position.index : 0}`;
}

function remember(key, value) {
  if (key === null || value === null) return value;
  const held = reached.get(key);
  const next = held === undefined ? value : Math.max(held, value);
  reached.delete(key);
  reached.set(key, next);
  if (reached.size > REACHED_LIMIT) {
    reached.delete(reached.keys().next().value);
  }
  return next;
}

/** isUpdateItemStep reports whether frame is one of an update item's own
 * steps (issue 664: "Installing", "Recording", ...) - not the batch's bracket
 * around the item, which carries the item's batch position (index/total)
 * itself, and not an update frame that is no step (a warning, a note). A
 * readout that names the item from its batch position adds the step's
 * words for exactly these. */
export function isUpdateItemStep(frame) {
  return (
    frame?.op === "update" && frame.phase in UPDATE_STEPS && !(frame.total > 0)
  );
}

/**
 * updateStepFraction is how far an update job is, 0..1, from one of its
 * frames (issue 664) - or null for a frame that is not an update's, which
 * the caller measures its own way. An update job's events carry no batch
 * position of their own: `position` is the job's newest frame that did (an
 * update_batch_item, activity.js's jobPositions), and the item's step is
 * placed within that item's share of the bar, so a batch of three fills a
 * third per mod and every step inside one moves it on. With no position (an
 * update from a file is one mod) the step is the whole bar.
 *
 * The bar never goes back within an item (`reached`). An update frame that
 * is not a step holds the bar where the item has got to; with nothing
 * reached yet it is the item's start, or null - indeterminate - when there
 * is no batch position either.
 */
export function updateStepFraction(frame, position) {
  if (!frame || frame.op !== "update") return null;
  const counted =
    position?.op === "update" && position.total > 0 && position.index > 0;
  const key = reachedKey(frame, position, counted);
  const place = (step) =>
    counted ? clamp((position.index - 1 + step) / position.total) : clamp(step);

  if (!(frame.phase in UPDATE_STEPS)) {
    const held = key === null ? undefined : reached.get(key);
    if (held !== undefined) return held;
    return counted ? place(0) : null;
  }
  const step =
    frame.phase === "update_downloading" || frame.phase === "archive_extracting"
      ? acquireStep(frame)
      : UPDATE_STEPS[frame.phase];
  return remember(key, place(step));
}

/**
 * progressFraction is how far along the bar should be, 0..1, or null when
 * the frame cannot say - in which case the control must render an
 * indeterminate bar rather than a confident 0%.
 *
 * An update's steps are placed by updateStepFraction (issue 664), within
 * the batch position `position` gives. Otherwise a download with a known
 * total is measured in bytes, and anything else in batch position, which
 * is the only honest denominator a multi-mod flow has.
 */
export function progressFraction(frame, position) {
  if (!frame) return null;
  if (importSwitchStep(frame)) return null;
  const stepped = updateStepFraction(frame, position);
  if (stepped !== null) return stepped;
  if (frame.type === "download" && frame.total_bytes > 0) {
    return clamp((frame.percent ?? 0) / 100);
  }
  if (frame.total > 0 && frame.index > 0) {
    return clamp(frame.index / frame.total);
  }
  return null;
}

/** importSwitchStep reports whether frame is an import's switch to its
 * profile (issue 666): in a profile_import job, the import_switching frame
 * that starts it, or any of the switch's own frames (op "switch"). The
 * switch is a fresh step with its own label and an indeterminate bar - its
 * disables, enables and downloads count from their own start, and the bar
 * must not go back from where the import left it (issue 664).
 *
 * Issue 669: decided from the frame alone. A rule that remembered seeing
 * the switch begin knew it only when a progress view had rendered that
 * frame; a view opened later - the tray closed and the toast dismissed
 * while the switch began, or a reconnect that replays only the latest
 * frame - drew the switch's frames as a determinate bar again. */
function importSwitchStep(frame) {
  return (
    frame.kind === "profile_import" &&
    (frame.op === "switch" || frame.phase === "import_switching")
  );
}

function clamp(value) {
  if (!Number.isFinite(value)) return null;
  return Math.min(1, Math.max(0, value));
}

/**
 * jobStateLabel is the word the tray puts on a job.
 *
 * "Queued" is not a registry state - core's beginOp serialises mutations by
 * BLOCKING, so a job waiting for the mutation slot is in state running
 * while doing nothing (activity.go's jobSummary doc comment). The signals
 * available are EventCount and, once one arrives, a progress frame: either
 * says the job has started working. Both are needed, because a SUMMARY is
 * only refreshed at the job's start and end - its event_count stays at the
 * value it had when the job was admitted (0) for the job's whole life, so
 * the count alone would label every running job "queued" from the moment it
 * began until the moment it ended.
 *
 * It is a heuristic, it is named as one in activity.go, and this is the
 * single place the UI applies it.
 */
export function jobStateLabel(summary, frame) {
  if (!summary) return "";
  if (summary.state === "running") {
    const working = (summary.event_count ?? 0) > 0 || Boolean(frame);
    return working ? "running" : "queued";
  }
  return summary.state;
}

// A finished BATCH job's outcome tally is not derived here: the server
// computes it from the job's own result and carries it on the job summary
// (internal/serve/jobtally.go's jobTally, issue 364), so a control has it on
// the same frame that says the job is over. A summary's `tally` is one of
// two shapes, told apart by `kind` - "updates" {applied, failed, skipped,
// skipped_notes} and "profile_import" {installed, failed, skipped, warnings}
// - and is absent for a job with no batch outcome to report, which every
// caller below reads as "say Done, not a tally". The kind-specific counts
// are omitted on the wire when zero, so each is read with a 0 default.

/** resultTallyLabel is a summary tally's own words, read off a finished batch's
 * real outcome - replacing the bare "Done" a job's mere `state` would
 * otherwise print over a batch that applied nothing at all.
 *
 * "updates" reads "n applied / m failed" for a batch that skipped nothing
 * (byte-identical to what this printed before), and "n applied / m skipped"
 * when it did - a lock refusal is a SKIP, and pairing it with a "0 failed"
 * it has nothing to do with is what made the honest outcome read as an
 * alarm. A batch that both failed and skipped names all three counts, since
 * dropping either one would hide an item's fate. profile_import
 * instead reads "n
 * installed · m skipped", since skipped is the flow's own documented
 * outcome, not a failure - with " · k failed" appended only when failed is
 * non-empty, and " · j warnings" appended only when the result actually
 * carries any (core.ProfileImportResult.Warnings, one entry per failed
 * mod). */
export function resultTallyLabel(tally) {
  const failed = tally.failed ?? 0;
  const skipped = tally.skipped ?? 0;
  // issue 660: a collection's import, update or finish counts its items
  // against the collection: "Imported 38 of 40 mods; 2 need you".
  // An import that only saved its mods pending (Download and install left
  // unchecked) keeps its "n installed · m skipped".
  if (
    isCollectionTally(tally) &&
    ((tally.todos ?? 0) > 0 ||
      skipped === 0 ||
      tally.kind === "collection_finish")
  ) {
    return collectionTallyLabel(tally);
  }
  if (tally.kind === "profile_fill") {
    // issue 608: what the files installed, and what is still to download.
    const parts = [`${tally.installed ?? 0} installed`, `${skipped} pending`];
    if (failed > 0) parts.push(`${failed} failed`);
    return parts.join(" · ");
  }
  if (tally.kind === "profile_import") {
    const parts = [`${tally.installed ?? 0} installed`, `${skipped} skipped`];
    if (failed > 0) parts.push(`${failed} failed`);
    if ((tally.warnings ?? 0) > 0) parts.push(`${tally.warnings} warnings`);
    return parts.join(" · ");
  }
  if (tally.kind === "catalog_relink") {
    // issue 590: "n re-linked", and the failures when there are any.
    const parts = [`${tally.applied ?? 0} re-linked`];
    if (failed > 0) parts.push(`${failed} failed`);
    return parts.join(" / ");
  }
  const parts = [`${tally.applied ?? 0} applied`];
  if (failed > 0 || skipped === 0) {
    parts.push(`${failed} failed`);
  }
  if (skipped > 0) parts.push(`${skipped} skipped`);
  return parts.join(" / ");
}

// INSTALLED_TALLY_KINDS are the tallies that count `installed`, not
// `applied`. "profile_import" stays last: spa_internal_test.go's import
// scanner reads a quoted word that follows `import"` on the same line as a
// module path.
const INSTALLED_TALLY_KINDS = new Set([
  "collection_finish",
  "collection_update",
  "profile_fill",
  "profile_import",
]);

/** isCollectionTally reports whether tally counts a collection's items
 * (issue 660): the tally of a collection import, update or finish, which carries
 * the items its profile lists as `total`. */
export function isCollectionTally(tally) {
  return (tally?.total ?? 0) > 0;
}

/** collectionTallyLabel is a collection tally in words (issue 660):
 * "Imported 38 of 40 mods; 2 need you" while items are left to finish,
 * "Imported 40 mods" once none is - "Recorded", as the CLI says it, for
 * one into a profile that is not the active one (`recorded`: its mods are
 * recorded, nothing installed). */
export function collectionTallyLabel(tally) {
  const total = tally.total ?? 0;
  const todos = tally.todos ?? 0;
  const verb = tally.recorded ? "Recorded" : "Imported";
  const mods = (n) => `${n} mod${n === 1 ? "" : "s"}`;
  if (todos === 0) return `${verb} ${mods(total)}`;
  // The CLI's own words (cmd/lmm/profile_finish.go): "K need you", for one
  // as for several.
  return `${verb} ${Math.max(0, total - todos)} of ${mods(total)}; ${todos} need you`;
}

/** outcomeLabel is a finished batch's words (issue 660): a collection
 * job's count of its collection (jobresult.js collectionOf: "Recorded 1 of
 * 3 mods; 2 need you"), which knows what its tally does not - whether the
 * profile was the active one, and a clean recorded import's count - else
 * its tally's (resultTallyLabel), else "". An import that only saved its
 * mods pending keeps its tally's "n installed · m skipped". */
export function outcomeLabel(tally, collection) {
  if (
    collection &&
    (!tally ||
      tally.kind === "collection_finish" ||
      (collection.todos ?? 0) > 0 ||
      (tally.skipped ?? 0) === 0)
  ) {
    return collectionTallyLabel(collection);
  }
  return tally ? resultTallyLabel(tally) : "";
}

/**
 * resultTallyTone says how alarmed the tally should look, in the same
 * three words a job's own `state` already uses ("succeeded"/"failed") plus
 * "mixed" for the case neither word is honest about: some items applied,
 * some did not. Never "succeeded" when anything failed, and never "failed"
 * outright when at least one item DID apply - a batch that partially
 * worked is not the same failure as one that achieved nothing (the design's
 * own "tone = failure when applied is empty; mixed tone otherwise").
 *
 * For profile_import (N1, unit 6 re-review) the same rule applies to
 * `installed`, not to `skipped` - a fully successful import that installs
 * nothing (the flow's own default, every pending mod counted as Skipped)
 * has failed === 0 and is "succeeded", never "failed".
 */
export function resultTallyTone(tally) {
  // issue 660: a collection with items left to finish needs the user -
  // never a plain success, and not a failure: the rest installed.
  if ((tally.todos ?? 0) > 0) return "attention";
  if ((tally.failed ?? 0) === 0) return "succeeded";
  const applied = INSTALLED_TALLY_KINDS.has(tally.kind)
    ? tally.installed
    : tally.applied;
  return (applied ?? 0) === 0 ? "failed" : "mixed";
}

/**
 * Normalizes one RAW core event from the per-job stream into the same flat
 * shape a jobProgressFrame has, so both streams render through the one
 * vocabulary above.
 *
 * The two streams carry the same facts in two envelopes on purpose: the
 * multiplexed stream summarises (activity.go's jobProgressFrame), the
 * per-job stream carries core.MarshalEvent's {"type","data"} verbatim for
 * whoever opened a job. Flattening here - rather than teaching every
 * renderer both shapes - is what keeps the second copy of core's event
 * vocabulary out of this application.
 *
 * data's members are the event struct's own json tags: Scope is embedded,
 * so op/mod_name/index/total are flat on it, and the human line is `detail`
 * on most types but `message` on a WarningEvent.
 */
export function frameFromEvent({ type, data } = {}) {
  const event = data ?? {};
  if (type === "verify") return verifyFrame(event);
  return {
    type,
    phase: event.phase,
    op: event.op,
    mod_name: event.mod_name,
    // The scope's exact mod (core.Scope.Mod), "source:mod": what an event
    // log keys an item's progress by (appendCollapsed).
    mod_key: event.mod?.source_id
      ? `${event.mod.source_id}:${event.mod.mod_id}`
      : "",
    index: event.index,
    total: event.total,
    detail: event.detail ?? event.message,
    percent: event.percent,
    downloaded: event.downloaded,
    total_bytes: event.total_bytes,
    file_index: event.file_index,
    file_total: event.file_total,
  };
}

// PROGRESS_PHASE is the closed list of phases that only say where an item
// has got to (issue 660): downloading, a download done, extracting,
// installing, compiling, deploying, fetching - and an update's running a
// hook and recording (issue 664). A job's event log keeps only
// the latest of these per item (appendCollapsed). Every other phase - a
// failure, a refusal, a note, a warning, an item skipped, unavailable or
// done - says something of its own and is always kept.
const PROGRESS_PHASE =
  /_(downloading|download_done|extracting|installing|installing_mod|mod_installing|compiling|deploying|fetching|running_hook|recording)$/;

/** frameModKey is the exact mod a frame is about - its scope's mod,
 * "source:mod" (frameFromEvent's mod_key) - else its place in the batch,
 * or "" for neither. Never its display name: two mods may share one. */
function frameModKey(frame) {
  if (frame.mod_key) return frame.mod_key;
  return frame.index > 0 ? `#${frame.index}/${frame.total ?? ""}` : "";
}

/** itemProgressKey is the item a progress frame reports on (issue 660) -
 * its op and its exact mod - or null for a frame that is not a plain
 * progress line (PROGRESS_PHASE; a download's byte count is one) or that
 * names no item. */
export function itemProgressKey(frame) {
  if (!frame) return null;
  const progress =
    frame.type === "download" || PROGRESS_PHASE.test(frame.phase ?? "");
  if (!progress) return null;
  const item = frameModKey(frame);
  if (!item) return null;
  return `${frame.op ?? ""}|${item}`;
}

/** appendCollapsed is events with entry ({key, frame}) appended, an earlier
 * progress line for the same item dropped (issue 660): a collection's
 * hundreds of items each said where they were - downloading, every
 * download tick, installing - and the log was a wall of lines. Any line
 * that is not plain progress is always kept. */
export function appendCollapsed(events, entry) {
  const item = itemProgressKey(entry.frame);
  if (!item) return [...events, entry];
  return [...events.filter((e) => itemProgressKey(e.frame) !== item), entry];
}

// verifyFrame is a core.VerifyEvent's frame, or null for one with nothing to
// say (issue 517). A verify run emits an event per file checked - mostly healthy
// ones - so only what a person reads is kept: a repair's sub-line (its own
// words, "Checksum filled from the cached files ..."), a finding that is a
// problem or was just fixed, and the sync warning. The per-file ok tick, the
// run's begin marker and the verbose diagnostics are dropped.
function verifyFrame(event) {
  const frame = { type: "verify", op: event.op };
  switch (event.kind) {
    case "repair_detail":
    case "sync_warning":
      return { ...frame, detail: event.detail };
    case "finding": {
      const finding = event.finding ?? {};
      if (finding.status === "ok" && !event.checksum_populated) return null;
      return {
        ...frame,
        mod_name: finding.mod_name || event.mod_name,
        detail:
          finding.note ||
          (event.checksum_populated
            ? "checksum populated"
            : (finding.status ?? "").replaceAll("_", " ")),
      };
    }
    default:
      return null;
  }
}
