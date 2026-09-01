// store.js - the application's single small store.
//
// No external state library (docs/plans/2026-08-31-serve-spa-design.md
// §Architecture). This is a plain observable object: components read
// state, actions replace slices of it, and every subscriber is told. It is
// deliberately not a general-purpose reducer framework - what this UI needs
// is one place the /api/v1 documents live so two views can never disagree
// about which game they are looking at.

/**
 * The initial state. Every slice that holds a server document starts null
 * rather than empty, so "not fetched yet" and "fetched and empty" stay
 * distinguishable - the difference between a spinner and an empty state.
 */
export function initialState() {
  return {
    route: { view: "chooser", game: "", profile: "" },
    status: null,
    // games is every configured game (core.StatusReport's own "games"),
    // refetched unscoped alongside every route - the game chooser's cards
    // and the top bar's game picker both need the full list, not just the
    // one status is currently scoped to.
    games: null,
    mods: null,
    updates: null,
    health: null,
    conflicts: null,
    profiles: null,
    // modPage is the full mod page's own state (route.view === "mod"):
    // {key, filesReport, error, detail, detailError, versions,
    // versionsError}. filesReport (core.ModFilesReport) is the PRIMARY
    // read - error is fatal, and only ever set from IT, never from detail/
    // versions, which are optional (main.js's hydrateModPage doc comment
    // explains why). key is "source/id" - the same fencing modalSeq gives
    // a slow plan, applied to a fetch that resolves after the user has
    // already arrowed or linked away to a different mod. null until that
    // route is visited at least once.
    modPage: null,
    // jobsIndex is the activity tray's rows: every job the registry still
    // retains, newest first. It is seeded by GET /api/v1/events's snapshot
    // frame and maintained by that stream's job_started/job_done frames
    // (activity.js) - the ONE place it is written, so a poll and a live
    // frame can never disagree about what the machine is doing.
    jobsIndex: null,
    // jobProgress is the latest jobProgressFrame per job id - what a
    // morphing control, a tray row and the library's live count all read to
    // say how far along a job is. Only the newest frame is kept: it is a
    // POSITION, not a log (the log is the per-job stream, one click away).
    jobProgress: {},
    // origins maps a control's origin key ("deploy", later
    // "install:fake/123") to the job id it started, which is what lets the
    // control that was clicked morph into that job's progress and no other.
    origins: {},
    // toasts are the completions whose origin was NOT on screen when they
    // landed (design doc §Jobs: "never for things in view").
    toasts: [],
    // activityError is the multiplexed stream's own failure, kept apart
    // from `error` (which blanks the page): losing the live stream must
    // leave every already-loaded document on screen and say so in the tray,
    // not tear Mission Control down.
    activityError: null,
    // modal is the ONE modal this application ever has open (design doc
    // §Modals: "Modals stack at most one deep"), null when none is. Today
    // that is always the confirm-plan modal; a later unit's reorder or
    // profiles modal is another shape in this same slot, not another slot.
    modal: null,
    error: null,
    // fetchErrors carries the rejection message for each of Mission
    // Control's four supplementary reads (mods/updates/health/conflicts),
    // independently of the null the failed slice itself gets set to - so a
    // component can tell "nothing to report" from "couldn't check" and
    // render an explicit error state instead of the all-clear empty one.
    // `status` is the same idea applied to a RE-hydrate (main.js's
    // onJobDone): the first load's failure is still fatal (there is nothing
    // on screen yet), but a later one must not blank a page that already
    // loaded - it reports here instead, same as the four supplementary
    // reads (I3).
    fetchErrors: {
      mods: null,
      updates: null,
      health: null,
      conflicts: null,
      status: null,
    },
  };
}

/** Creates a store over an initial state. */
export function createStore(state = initialState()) {
  let current = state;
  const subscribers = new Set();

  const notify = () => {
    for (const subscriber of subscribers) subscriber(current);
  };

  return {
    /** The current state. Treat it as immutable. */
    get() {
      return current;
    },

    /**
     * Merges a patch into the state and notifies subscribers. A patch is a
     * shallow set of slices, which is all this store's shape ever needs.
     */
    set(patch) {
      current = { ...current, ...patch };
      notify();
    },

    /** Subscribes to every change; returns an unsubscribe function. */
    subscribe(subscriber) {
      subscribers.add(subscriber);
      return () => subscribers.delete(subscriber);
    },
  };
}
