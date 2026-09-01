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
