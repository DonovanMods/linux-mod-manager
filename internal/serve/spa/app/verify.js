// verify.js - small, pure helpers shared by every surface that renders a
// core.VerifyFinding: the Health card's own list (cards.js) and the
// verify_fix confirm modal's preview (plan_verify_fix.js, issue 332). Extracted
// rather than duplicated so the two surfaces can never drift on what a
// finding's own words say.

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
