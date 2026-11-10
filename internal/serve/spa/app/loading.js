// loading.js - when a read's loading state is painted, and what it says
// (issue 583).
//
// Mission Control renders the moment its fast reads land and names each
// slow one in place ("Checking 42 mods for updates (17/42)…") rather than
// waiting on the slowest. Two rules keep that from turning into noise: a
// read must be out for loadingShowDelayMillis before anything is painted
// (most reads answer in a few milliseconds and should show nothing at all),
// and once painted it stays for loadingMinShownMillis so it never flickers.

import { useEffect, useRef, useState } from "./render.js";
import { loadingMinShownMillis, loadingShowDelayMillis } from "./timers.js";
import { countOf } from "./modrows.js";

/**
 * useShownWhile is true while `active` has held for the show delay, and
 * stays true for at least the minimum once it has become so - the painted
 * half of a loading state, as opposed to the in-flight half.
 */
export function useShownWhile(active) {
  const [shown, setShown] = useState(false);
  const shownAt = useRef(0);

  useEffect(() => {
    if (active && !shown) {
      const timer = setTimeout(() => {
        shownAt.current = performance.now();
        setShown(true);
      }, loadingShowDelayMillis);
      return () => clearTimeout(timer);
    }
    if (!active && shown) {
      const left =
        loadingMinShownMillis - (performance.now() - shownAt.current);
      if (left <= 0) {
        setShown(false);
        return undefined;
      }
      const timer = setTimeout(() => setShown(false), left);
      return () => clearTimeout(timer);
    }
    return undefined;
  }, [active, shown]);

  return shown;
}

/**
 * useLastWhile is `value` while `active`, and the last value it had while
 * active once it is not - what a loading line keeps saying through its
 * minimum time on screen after its read has already answered.
 */
export function useLastWhile(active, value) {
  const last = useRef(value);
  if (active) last.current = value;
  return last.current;
}

/** PENDING_READS is every Mission Control read that can name itself while
 * it is out, in the order their lines are listed. The library's own read
 * (mods) is not here: the library region says that one itself. */
export const PENDING_READS = ["updates", "health", "conflicts", "snapshots"];

/**
 * pendingReadLabel is what read's loading line says. `mods` is how many
 * mods the profile has, when the library has answered (null until then);
 * `progress` is the newest read_progress frame for this load, if any
 * (internal/serve/read_progress.go) - its total is the number actually
 * being walked, which is the one to state once known.
 */
export function pendingReadLabel(read, { mods = null, progress = null } = {}) {
  const total = progress?.total > 0 ? progress.total : mods;
  const position =
    progress?.total > 0 ? ` (${progress.index}/${progress.total})` : "";
  switch (read) {
    case "updates":
      return total
        ? `Checking ${countOf(total, "mod")} for updates${position}…`
        : "Checking for updates…";
    case "health":
      return total
        ? `Verifying ${countOf(total, "mod")}${position}…`
        : "Verifying your mods…";
    case "conflicts":
      return "Checking for file conflicts…";
    case "snapshots":
      return "Listing snapshots…";
    default:
      return "Loading…";
  }
}

/** pendingReadDone is what the live region says once read has answered,
 * for a read whose loading line was painted. */
export function pendingReadDone(read) {
  switch (read) {
    case "updates":
      return "Update check finished.";
    case "health":
      return "Verify finished.";
    case "conflicts":
      return "Conflict check finished.";
    case "snapshots":
      return "Snapshots listed.";
    case "mods":
      return "Library loaded.";
    default:
      return "Finished loading.";
  }
}

/**
 * progressFor is the newest read_progress frame for the load of `read`
 * still in flight, or null - frames for a load that has since answered, or
 * for another tab's read, carry a token nothing here is waiting on.
 */
export function progressFor(state, read) {
  const token = state.loading?.[read]?.token;
  return token ? (state.readProgress?.[token] ?? null) : null;
}

/** progressFractionOf is how far a read_progress frame says its read has
 * got, 0..1, or null when there is no frame to say. */
export function progressFractionOf(progress) {
  if (!progress || !(progress.total > 0)) return null;
  return Math.min(1, Math.max(0, progress.index / progress.total));
}
