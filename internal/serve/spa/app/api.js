// api.js - the one place this application talks to /api/v1.
//
// Two rules it exists to keep in a single place:
//
//  1. CSRF. The token arrives in the shell's meta tag and goes back as
//     X-CSRF-Token on every state-changing request - the same header the
//     middleware has always accepted (middleware.go).
//  2. Failures are the CLI's {"error","details"} envelope. Every non-2xx
//     answer under /api/v1 is that envelope, so it is decoded here once and
//     thrown as an ApiError carrying both halves, rather than each caller
//     inventing its own reading of a failure.

const csrfToken =
  document.querySelector('meta[name="csrf-token"]')?.getAttribute("content") ||
  "";

/** A non-2xx answer, carrying the envelope's message and its details. */
export class ApiError extends Error {
  constructor(status, message, details) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.details = details;
  }
}

async function request(method, path, body) {
  const init = { method, headers: {} };
  if (method !== "GET") {
    init.headers["X-CSRF-Token"] = csrfToken;
  }
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  const response = await fetch(path, init);
  const text = await response.text();
  const payload = text ? JSON.parse(text) : null;

  if (!response.ok) {
    throw new ApiError(
      response.status,
      payload?.error || `${method} ${path} failed (${response.status})`,
      payload?.details,
    );
  }
  return payload;
}

/** Appends the game/profile pair every scoped endpoint resolves from. */
export function scoped(path, { game, profile } = {}) {
  if (!game) return path;
  const url = new URL(path, window.location.origin);
  url.searchParams.set("game", game);
  if (profile) url.searchParams.set("profile", profile);
  return url.pathname + url.search;
}

export const get = (path) => request("GET", path);
export const post = (path, body) => request("POST", path, body);
export const del = (path) => request("DELETE", path);

/**
 * Computes a mutation's plan and returns {plan_id, kind, plan}. The plan
 * DOCUMENT is the frozen core type the CLI's --dry-run --json emits; the
 * handle is single-use and expires, which is why nothing caches it.
 */
export const plan = (kind, options, context) =>
  post(scoped(`/api/v1/plans/${kind}`, context), options ?? {});

/** Redeems a plan handle, starting its Apply as a job. Returns {job_id}. */
export const startJob = (planID, options) =>
  post("/api/v1/jobs", { plan_id: planID, ...(options ? { options } : {}) });

/** Reads one job's status document - callerless since Unit 3 landed it,
 * until issue 330's per-mod job history (jobhistory.js) became its first
 * consumer: the tray's own jobsIndex is deliberately Result-less
 * (activity.go), so a caller that needs to know what a FINISHED job's own
 * result document said has to read this. */
export const jobStatus = (id) => get(`/api/v1/jobs/${encodeURIComponent(id)}`);

/** modPath builds one mod's /api/v1/mods/{source}/{id} base path - shared
 * by every per-mod read/write endpoint below. */
function modPath(sourceID, modID) {
  return `/api/v1/mods/${encodeURIComponent(sourceID)}/${encodeURIComponent(modID)}`;
}

/** Reads one mod's core.ModDetail document (name, description, changelog). */
export const getModDetail = (sourceID, modID, context) =>
  get(scoped(modPath(sourceID, modID), context));

/** Reads one installed mod's core.ModFilesReport document. */
export const getModFiles = (sourceID, modID, context) =>
  get(scoped(`${modPath(sourceID, modID)}/files`, context));

/** Reads one mod's versions document ({versions, supported}). */
export const getModVersions = (sourceID, modID, context) =>
  get(scoped(`${modPath(sourceID, modID)}/versions`, context));

/**
 * Runs a search against the game's configured sources: GET /api/v1/search,
 * returning core.SearchReport verbatim. opts.page/pageSize are issue 331's
 * pagination params (the omnibar's fan-out never sets either - a live
 * filter has no "next page"; the dedicated search page does). opts.category/
 * source are the search page's own filters, forwarded server-side (Important
 * 1b, unit 5 fix wave) rather than sliced client-side over one page's own
 * Mods - a category/source change re-queries page 0 with the new filter, so
 * the count it renders answers the CATALOG, not one page. Sort stays
 * client-side (searchpage.js): it only reorders what a page already holds,
 * which needs no round trip.
 */
export function search(query, opts, context) {
  const url = new URL(
    scoped("/api/v1/search", context),
    window.location.origin,
  );
  url.searchParams.set("q", query);
  if (opts?.page != null) url.searchParams.set("page", String(opts.page));
  if (opts?.pageSize != null)
    url.searchParams.set("page_size", String(opts.pageSize));
  if (opts?.limit != null) url.searchParams.set("limit", String(opts.limit));
  if (opts?.category) url.searchParams.set("category", opts.category);
  if (opts?.source) url.searchParams.set("source", opts.source);
  return get(url.pathname + url.search);
}

/** Starts an enable/disable job directly - the one sanctioned plan-free
 * mutation path (kind_toggle.go); no plan step, so there is nothing to
 * confirm before it runs. Returns {job_id}, the same shape startJob does. */
export const startToggle = (action, sourceID, modID, context) =>
  post(scoped(`${modPath(sourceID, modID)}/${action}`, context));

/** Sets sourceID/modID's lock, at version (empty locks at whatever is
 * currently installed). Returns core.ModSettingResult directly - this is a
 * thin, synchronous mutation, not a job (api_mod_settings.go's own doc
 * comment: nothing meaningful to show in flight for a single DB write). */
export const setModLock = (sourceID, modID, version, context) =>
  post(
    scoped(`${modPath(sourceID, modID)}/lock`, context),
    version ? { version } : {},
  );

/** Clears sourceID/modID's lock. Returns core.ModSettingResult directly. */
export const clearModLock = (sourceID, modID, context) =>
  post(scoped(`${modPath(sourceID, modID)}/unlock`, context), {});

/** Sets sourceID/modID's update policy ("notify"/"auto"/"pinned"). Returns
 * core.ModSettingResult directly. */
export const setModUpdatePolicy = (sourceID, modID, policy, context) =>
  post(scoped(`${modPath(sourceID, modID)}/update-policy`, context), {
    policy,
  });

// profilePath builds one profile's /api/v1/profiles/{name} base path -
// shared by every named-profile route below (api_profiles.go's own doc
// comment: the profile is named in the PATH, never ?profile=).
function profilePath(name) {
  return `/api/v1/profiles/${encodeURIComponent(name)}`;
}

/** Reads the game's core.ProfileListing document (the profiles modal's own
 * list - name, mod count, default marker) - only the game half of context
 * resolves (api_profiles.go). */
export const listProfiles = (context) =>
  get(scoped("/api/v1/profiles", context));

/** Creates a profile. Returns core.ProfileResult; 409 if the name is taken. */
export const createProfile = (name, context) =>
  post(scoped("/api/v1/profiles", context), { name });

/** Deletes a profile. Returns core.ProfileResult (the profile as it stood
 * immediately before the delete). */
export const deleteProfile = (name, context) =>
  del(scoped(profilePath(name), context));

/** Renames a profile. Returns core.ProfileResult under the new name; 409 if
 * newName is taken. */
export const renameProfile = (name, newName, context) =>
  post(scoped(`${profilePath(name)}/rename`, context), { name: newName });

/** Marks a profile as the game's default. Returns core.ProfileResult,
 * re-read after the write - callers should refetch listProfiles too, since
 * SetDefault clears the flag on every OTHER profile server-side. */
export const setDefaultProfile = (name, context) =>
  post(scoped(`${profilePath(name)}/set-default`, context), {});

/** The profile export download's URL - a plain GET the browser downloads
 * via <a download>, never fetched through this module: the server sets
 * Content-Disposition itself (api_profiles.go), so there is no blob to
 * build and no filename to invent. */
export const profileExportURL = (name, context) =>
  scoped(`${profilePath(name)}/export`, context);

/** Commits a new load order. Returns core.ProfileResult, re-read after the
 * write, so profile.mods IS the persisted order. ids are lowest-priority
 * first (api_profiles.go); a partial order is fine - what is left unnamed
 * keeps its existing relative order after everything named. */
export const reorderProfile = (name, ids, context) =>
  post(scoped(`${profilePath(name)}/reorder`, context), { ids });

/** Previews a proposed load order without committing it: GET
 * /api/v1/conflicts?order=... - the same core.ConflictReport shape the
 * unordered read returns, so the reorder modal renders it with the exact
 * same component. order omitted/empty previews the profile's SAVED order
 * (today's plain conflicts read). */
export function conflictsForOrder(order, context) {
  const url = new URL(
    scoped("/api/v1/conflicts", context),
    window.location.origin,
  );
  if (order && order.length > 0) url.searchParams.set("order", order.join(","));
  return get(url.pathname + url.search);
}
