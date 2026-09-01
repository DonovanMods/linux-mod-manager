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
  enable: "Enabling",
  disable: "Disabling",
};

/** mutationLabel names a running job's KIND in words, falling back to the
 * kind's own wire name (underscores opened up) for one this vocabulary
 * hasn't seen - the same "still renders, just not humanized" safety
 * humanizePhase gives an unrecognized phase, so a kind added later needs no
 * matching SPA release to show something sensible. */
export function mutationLabel(kind) {
  return mutationKindLabels[kind] ?? humanizePhase(kind);
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
