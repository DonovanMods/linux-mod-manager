// verify.js - small, pure helpers shared by every surface that renders a
// core.VerifyFinding: the Health card's own list (cards.js), the verify_fix
// confirm modal's preview (plan_verify_fix.js, issue 332) and the
// slide-over's Findings section (modpanel.js). Extracted rather than
// duplicated so the surfaces can never drift on what a finding's own words
// say - which they had, until issue 334's gate review found the slide-over
// still printing the raw status slug beside a card rendering the same
// finding as prose.

/** findingSubject names what a finding is about: its mod, or - for a
 * game-level row with no mod (issue 460: `mod_path_missing`) - the game's
 * mod path. */
export function findingSubject(f) {
  if (f.status === "mod_path_missing") return "Mod path";
  return f.mod_name || f.mod_id || f.status.replaceAll("_", " ");
}

/** findingLabel prefers a finding's own note (already human-worded, e.g. a
 * repair failure's reason) and falls back to the status verbatim, appending
 * the recorded/source versions VerifyFinding carries for a version_mismatch
 * - the most common finding - to match the CLI's own "AlphaMod - VERSION
 * MISMATCH (recorded 1.0, source reports 2.0)". */
export function findingLabel(f) {
  const label = f.note || f.status.replaceAll("_", " ");
  if (f.recorded && f.effective) {
    return `${label} (recorded ${f.recorded}, source reports ${f.effective})`;
  }
  return label;
}

/** isRepaired reports whether a --fix run repaired this finding (issue 517).
 *
 * The "fixed_*" statuses say so themselves. A repair that resolves a row to
 * "ok" cannot - a repaired "ok" and an untouched one are the same status - so
 * core marks it with the repair's own sentence in `repair`. */
export function isRepaired(f) {
  if (typeof f?.status !== "string") return false;
  return (
    f.status.startsWith("fixed_") || (f.status === "ok" && Boolean(f.repair))
  );
}

/** repairOutcome splits a finished --fix run's findings into the two groups
 * worth reading: what it repaired, and what still needs attention. Every
 * other row - a healthy file, a skipped one - is neither, and is left out
 * so the list stays about the repair rather than about the profile. */
export function repairOutcome(findings) {
  const repaired = [];
  const attention = [];
  for (const f of Array.isArray(findings) ? findings : []) {
    if (isRepaired(f)) repaired.push(f);
    else if (f?.status && f.status !== "ok" && f.status !== "skipped") {
      attention.push(f);
    }
  }
  return { repaired, attention };
}

/** repairNote is the sentence saying what a repair did: the row's own
 * `repair`, or - for a "fixed_*" row - its note. */
export function repairNote(f) {
  return f.repair || f.note || "";
}
