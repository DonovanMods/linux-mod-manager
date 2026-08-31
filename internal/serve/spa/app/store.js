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
    mods: null,
    updates: null,
    health: null,
    conflicts: null,
    profiles: null,
    jobs: {},
    error: null,
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
