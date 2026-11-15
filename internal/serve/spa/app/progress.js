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

/**
 * Humanizes a core phase name ("deploy_merge_synced" -> "Merge synced").
 *
 * A phase this function has never seen still renders - as its own name with
 * the underscores opened up - which is what makes it safe for core to add
 * one without the SPA needing a matching release. That is the design's
 * "humanized" (§Jobs) read as a rule about the vocabulary rather than a
 * hand-maintained table of every one of the ~90 phases in phases.go.
 */
export function humanizePhase(phase) {
  if (!phase) return "";
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
  if (!frame) return "";
  const parts = [];
  const phase = humanizePhase(frame.phase);
  if (phase) parts.push(phase);
  if (frame.mod_name) parts.push(frame.mod_name);
  if (frame.total > 0) parts.push(`${frame.index ?? 0} of ${frame.total}`);

  const bytes = downloadText(frame);
  if (bytes) parts.push(bytes);
  // The detail line is the event's own words (a warning's message, a step's
  // note). It is added last and only when it is not simply repeating the
  // phase this frame already rendered.
  if (frame.detail && frame.detail !== phase) parts.push(frame.detail);

  return parts.join(" · ") || humanizePhase(frame.type);
}

/**
 * progressFraction is how far along the bar should be, 0..1, or null when
 * the frame cannot say - in which case the control must render an
 * indeterminate bar rather than a confident 0%.
 *
 * A download with a known total is measured in bytes; anything else is
 * measured in batch position, which is the only honest denominator a
 * multi-mod flow has.
 */
export function progressFraction(frame) {
  if (!frame) return null;
  if (frame.type === "download" && frame.total_bytes > 0) {
    return clamp((frame.percent ?? 0) / 100);
  }
  if (frame.total > 0 && frame.index > 0) {
    return clamp(frame.index / frame.total);
  }
  return null;
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
 * "Imported 40 mods" once none is. */
export function collectionTallyLabel(tally) {
  const total = tally.total ?? 0;
  const todos = tally.todos ?? 0;
  const mods = (n) => `${n} mod${n === 1 ? "" : "s"}`;
  if (todos === 0) return `Imported ${mods(total)}`;
  // The CLI's own words (cmd/lmm/profile_finish.go): "K need you", for one
  // as for several.
  return `Imported ${Math.max(0, total - todos)} of ${mods(total)}; ${todos} need you`;
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
    index: event.index,
    total: event.total,
    detail: event.detail ?? event.message,
    percent: event.percent,
    downloaded: event.downloaded,
    total_bytes: event.total_bytes,
  };
}

/** itemProgressKey is the item a frame reports progress on (issue 660) -
 * its op and its mod (by name, else its place in the batch) - or null for
 * a frame that is not one: a warning, a failure, a verify finding, or a
 * line about no item at all. A job's event log keeps only the latest
 * progress line per item (appendCollapsed). */
export function itemProgressKey(frame) {
  if (!frame || frame.type === "warning" || frame.type === "verify") {
    return null;
  }
  if (/fail|warn|skip/.test(frame.phase ?? "")) return null;
  const item = frame.mod_name || (frame.index > 0 ? `#${frame.index}` : "");
  if (!item) return null;
  return `${frame.op ?? ""}|${item}`;
}

/** appendCollapsed is events with entry ({key, frame}) appended, an earlier
 * progress line for the same item dropped (issue 660): a collection's
 * hundreds of items each said where they were - downloading, every
 * download tick, installing - and the log was a wall of lines. Warnings
 * and failures are always kept. */
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
