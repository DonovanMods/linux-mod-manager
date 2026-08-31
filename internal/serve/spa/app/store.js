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
    // jobsIndex is GET /api/v1/jobs's own "jobs" array (newest first) - the
    // activity bell's read-only tray. Distinct from `jobs` below: that slice
    // is reserved for a later unit's per-job SSE-tracked state, keyed by id.
    jobsIndex: null,
    jobs: {},
    error: null,
    // fetchErrors carries the rejection message for each of Mission
    // Control's four supplementary reads (mods/updates/health/conflicts),
    // independently of the null the failed slice itself gets set to - so a
    // component can tell "nothing to report" from "couldn't check" and
    // render an explicit error state instead of the all-clear empty one.
    fetchErrors: { mods: null, updates: null, health: null, conflicts: null },
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

    /** Replaces one job's tracked state, keyed by job id. */
    setJob(id, job) {
      current = { ...current, jobs: { ...current.jobs, [id]: job } };
      notify();
    },

    /** Subscribes to every change; returns an unsubscribe function. */
    subscribe(subscriber) {
      subscribers.add(subscriber);
      return () => subscribers.delete(subscriber);
    },
  };
}
