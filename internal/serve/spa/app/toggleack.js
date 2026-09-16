// toggleack.js - the click acknowledgment behind every enable/disable
// control in this application (issue 432).
//
// Enabling a mod genuinely deploys its files (core.Service.EnableMod ->
// installer.Install), so the wait between the click and the new truth is
// real work and cannot be optimised away. What was wrong is that the UI
// spent that whole wait looking exactly as it had before: the library row's
// checkbox, the slide-over's button and the full mod page's button all
// rendered straight off server state, greyed themselves out, and only moved
// once the job's terminal frame had landed AND the re-hydrate behind it had
// committed. A click that changes nothing on screen reads as a click that
// was missed - so people clicked again, which the `disabled` attribute then
// swallowed in silence too.
//
// This module is the one place that remembers "you asked for this, it has
// not happened yet". A REQUESTED state is recorded per mod the instant the
// control is clicked, rendered in place of the server's value, and dropped
// again as soon as either half of the truth catches up:
//
//   - the server agrees - the re-hydrate main.js#onJobDone runs has landed
//     and the mod now reads the way it was asked to. The ordinary ending,
//     and the one that makes the hand-off seamless: the requested value and
//     the real value are the same pixel, so nothing flickers.
//   - the job started FOR THAT REQUEST failed, so what was asked for is not
//     going to happen. The control goes back to what is actually true.
//
// "For that request" is load-bearing. Every entry records the id of its own
// job once one is bound, and only that job can settle it. The control's
// origin is no substitute: it is stable across the toggle's direction (see
// modToggleOrigin) and a failed job's binding is never released, so an
// origin still names the PREVIOUS job for as long as a new request's own
// start is in flight - and for a sequenced batch, for as long as the rows
// ahead of it are running. Settling against the origin meant a retry after
// a failure was settled by that failure on the frame it was made in, and the
// click it was supposed to acknowledge showed nothing at all.
//
// Neither ending toasts from here, deliberately. A failure already has a
// surface: a control wrapped in InlineJob (the slide-over, the full mod
// page) renders "Failed: ..." in place of itself, and a control with no
// inline surface of its own - the library row's bare checkbox - is by
// definition not "mounted" as far as activity.js's origin registry is
// concerned, so main.js#onJobDone toasts it. A third telling from here
// would only ever repeat something the user is already looking at.

import { useEffect, useState } from "./render.js";

/** modToggleOrigin is the stable origin every enable/disable control for one
 * mod shares - "mod:{source}/{id}:toggle" (modrows.js#modOriginPattern).
 *
 * Stable across the DIRECTION of the toggle, which is the whole point: an
 * origin keyed on "enable" vs "disable" flips the instant the job succeeds,
 * so the next render looks up an origin the job was never bound to. That was
 * a real bug once (TestE2E_SlideOver_EnableDisableMorphsInline) and this
 * function is what keeps its fix in one place. */
export function modToggleOrigin(sourceID, modID) {
  return `mod:${sourceID}/${modID}:toggle`;
}

/** pendingToggleLabel is what an in-flight toggle says about itself, in the
 * same present participle progress.js#mutationKindLabels uses for the job
 * once it has actually started ("Enabling", "Disabling") - so the sentence
 * the row shows before the job exists and the one it shows after are the
 * same words, not two different vocabularies for one operation. */
export function pendingToggleLabel(want) {
  return want ? "Enabling…" : "Disabling…";
}

/**
 * usePendingToggles is the hook a component with toggle controls calls
 * once, at the top of its render.
 *
 * `enabledOf(key)` answers with the SERVER's current enabled value for a mod
 * key (domain.ModKey - "sourceID:modID"), or undefined when this component
 * cannot see that mod at all. It is read from the LATEST render on purpose -
 * that is how a pending entry finds out the re-hydrate has landed - so the
 * caller passes a plain closure over whatever document it renders from and
 * never has to memoise it.
 *
 * The returned object is deliberately small:
 *
 *   requestedFor(key)  the state the user asked for and has not got yet, or
 *                      undefined when nothing is pending for that mod
 *   start(mod)         acknowledge and run one toggle
 *   startBatch(a, m)   acknowledge and run a whole batch of them
 */
export function usePendingToggles(state, actions, enabledOf) {
  const [pending, setPending] = useState(() => new Map());

  // No dependency array, on purpose. What settles an entry is a fact spread
  // across three separately-updated places - the mods document `enabledOf`
  // closes over, state.origins and state.jobsIndex - and a dependency list
  // naming all three would still be wrong for the callers whose document is
  // not `state.mods` at all (the full mod page hydrates its own). Running
  // after every render is both simpler and complete: the effect returns on
  // its first line while nothing is pending, which is almost always, and it
  // cannot loop, because it only ever calls setPending when it has actually
  // removed an entry.
  useEffect(() => {
    if (pending.size === 0) return;
    const next = new Map(pending);
    for (const [key, entry] of pending) {
      if (hasSettled(state, entry, enabledOf(key))) next.delete(key);
    }
    if (next.size !== pending.size) setPending(next);
  });

  function remember(entries) {
    setPending((prev) => {
      const next = new Map(prev);
      for (const [key, entry] of entries) next.set(key, entry);
      return next;
    });
  }

  // bind and forget both act on one REQUEST, not on whatever is stored
  // under its key: a later request for the same mod (a batch taking a row
  // whose own toggle is still in flight) replaces the entry, and news about
  // the earlier request must not land on the later one.
  function bind(key, entry, jobID) {
    setPending((prev) => {
      if (prev.get(key)?.request !== entry.request) return prev;
      return new Map(prev).set(key, { ...prev.get(key), jobID });
    });
  }

  function forget(key, entry) {
    setPending((prev) => {
      if (prev.get(key)?.request !== entry.request) return prev;
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
  }

  /** settleBound is what a start reports back per mod: the job it bound, or
   * null when the start never happened (main.js#startToggle turns a failure
   * to START - a 409 over the queue-depth cap, a network error - into a
   * toast and binds no job, so there is nothing hasSettled could ever find
   * and the entry is dropped here instead). */
  function settleBound(key, entry, jobID) {
    if (jobID) bind(key, entry, jobID);
    else forget(key, entry);
  }

  return {
    requestedFor(key) {
      return pending.get(key);
    },

    /** start acknowledges one row's click and runs its job. `mod` is
     * {key, source_id, id, enabled} - the library row, the slide-over's row
     * and the full mod page's own installed mod all carry those four. */
    async start(mod) {
      const want = !mod.enabled;
      const origin = modToggleOrigin(mod.source_id, mod.id);
      const entry = newRequest(want);
      remember([[mod.key, entry]]);
      const jobID = await actions.startToggle({
        action: want ? "enable" : "disable",
        sourceID: mod.source_id,
        modID: mod.id,
        origin,
      });
      settleBound(mod.key, entry, jobID);
    },

    /** startBatch acknowledges a whole multi-select at once - every row
     * moves on the click, not one at a time as the sequenced batch reaches
     * it (main.js#startBatchToggle runs them strictly one after another, so
     * without this the last row of a long selection sat untouched for the
     * whole batch). */
    async startBatch(action, mods) {
      const want = action === "enable";
      const entries = new Map(mods.map((mod) => [mod.key, newRequest(want)]));
      remember(entries);
      // Each row's job is bound as the batch reaches it, one at a time, so a
      // row further down stays pending - on the click, not on its turn -
      // until its OWN job exists and ends.
      await actions.startBatchToggle(action, mods, (mod, jobID) =>
        settleBound(mod.key, entries.get(mod.key), jobID),
      );
    },
  };
}

let requestSeq = 0;

/** newRequest is one pending entry: what was asked for, the identity of
 * this particular asking (so a newer request for the same mod is never
 * mistaken for it), and the job bound to it once there is one. */
function newRequest(want) {
  return { want, request: ++requestSeq, jobID: null };
}

/** hasSettled reports whether a pending request has reached either of its
 * two endings: the server now agrees with it, or the job started for it has
 * failed and it is not going to happen. A request with no job of its own
 * yet cannot have failed. See the module comment. */
function hasSettled(state, entry, actual) {
  if (actual === entry.want) return true;
  if (!entry.jobID) return false;
  const summary = (state.jobsIndex ?? []).find((job) => job.id === entry.jobID);
  return summary?.state === "failed";
}
