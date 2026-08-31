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

/** Reads one job's status document. */
export const jobStatus = (id) => get(`/api/v1/jobs/${encodeURIComponent(id)}`);
