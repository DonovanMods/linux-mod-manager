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

/** put issues a PUT with a JSON body - the Setup surface's source editor is
 * this module's only PUT (api_sources.go's PUT /api/v1/sources/{id}). */
export const put = (path, body) => request("PUT", path, body);

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

// -- Setup surface (issue 333): games, auth, custom sources, uploads. None
// of these routes is game-scoped (task-A's own wire note - "they are how a
// game comes to exist"), so none takes `scoped()`/context.

/** Reads every configured game: []core.GameListEntry, the game chooser's
 * first-run signal (an empty array) and the Setup page's Games table. */
export const listGames = () => get("/api/v1/games");

/** Searches sourceID's game catalog for query: core.GameCatalogReport.
 * core.ErrNoGameCatalog surfaces as a 400 the add form reads to swap its
 * search box for a plain identifier field (api_games.go). */
export const gameCatalog = (sourceID, query) => {
  const url = new URL("/api/v1/games/catalog", window.location.origin);
  url.searchParams.set("source", sourceID);
  url.searchParams.set("q", query);
  return get(url.pathname + url.search);
};

/** Creates a game from a gameAddRequest-shaped spec: core.GameListEntry, the
 * same row listGames() returns - splice it straight in rather than
 * re-reading. */
export const addGame = (spec) => post("/api/v1/games", spec);

/** Reads the Steam detect scan's pre-selection listing: core.GameDetectListing. */
export const detectGames = () => get("/api/v1/games/detect");

/** Applies a detect selection (1-based indices or slugs): core.GameDetectResult. */
export const applyGameDetect = (select) =>
  post("/api/v1/games/detect", { select });

/** Reads every auth-capable source's state plus any orphaned token:
 * app.AuthStatusReport (`lmm auth status --json`'s document). */
export const getAuthStatus = () => get("/api/v1/auth");

/** Stores sourceID's API key, live-validated where the source has a
 * validator, and answers with the re-read app.AuthStatusReport. The key
 * itself never comes back - only report.sources[].key_masked does. */
export const authLogin = (sourceID, apiKey) =>
  post(`/api/v1/auth/${encodeURIComponent(sourceID)}`, { api_key: apiKey });

/** Removes sourceID's stored credential (or an orphaned token with no
 * source), answering with the re-read app.AuthStatusReport. */
export const authLogout = (sourceID) =>
  del(`/api/v1/auth/${encodeURIComponent(sourceID)}`);

/** Reads the full source registry, including load/construction errors:
 * []app.SourceInfo (`lmm source list --json`'s document, unscoped). */
export const listSources = () => get("/api/v1/sources");

/** Reads one custom source's raw YAML definition as text (never JSON - the
 * editor wants the file's own bytes, comments and key order included). A
 * built-in source has no definition and 404s. */
export const getSourceDefinition = async (id) => {
  const response = await fetch(
    `/api/v1/sources/${encodeURIComponent(id)}/definition`,
  );
  const text = await response.text();
  if (!response.ok) {
    let payload = null;
    try {
      payload = JSON.parse(text);
    } catch {
      // A non-JSON body (a plain 404) - the status text is all there is.
    }
    throw new ApiError(
      response.status,
      payload?.error || `couldn't load the definition (${response.status})`,
      payload?.details,
    );
  }
  return text;
};

/** The definition download's own URL - a plain GET the browser downloads
 * via <a download>, the same convention profileExportURL follows. */
export const sourceDefinitionURL = (id) =>
  `/api/v1/sources/${encodeURIComponent(id)}/definition`;

/** Validates a draft definition (and optionally probes it live):
 * app.SourceValidationReport. A validation or probe failure is an ApiError
 * whose `details` IS the report - callers render it the same way either
 * side of that split. */
export const validateSource = (yaml, { probe, probeID } = {}) =>
  post("/api/v1/sources/validate", {
    yaml,
    ...(probe ? { probe: true } : {}),
    ...(probeID ? { probe_id: probeID } : {}),
  });

/** Saves id's definition (the yaml's own "id" must equal it) and swaps the
 * constructed source into the running registry with no restart. Answers
 * with the re-read []app.SourceInfo. */
export const saveSource = (id, yaml) =>
  put(`/api/v1/sources/${encodeURIComponent(id)}`, { yaml });

/** Deletes id's definition. 409 (core.SourceInUseError) when a game still
 * maps it. Answers with the re-read []app.SourceInfo. */
export const deleteSource = (id) =>
  del(`/api/v1/sources/${encodeURIComponent(id)}`);

// maxUploadBytes mirrors the server's own cap (uploads.go's maxUploadBytes)
// so an oversized file is refused before a multi-gigabyte upload even
// starts, rather than after streaming it all the way to a 413.
export const maxUploadBytes = 2 * 1024 * 1024 * 1024;

/**
 * Uploads an archive via XHR (fetch has no upload-progress events, which is
 * the one thing this form needs to show for a multi-hundred-megabyte file).
 * onProgress(loaded, total) fires as the browser reports it; total is 0
 * when the browser can't compute it (rare for a same-origin POST with a
 * known Content-Length, kept anyway for honesty).
 *
 * Resolves with the uploadResponse document ({upload_id, filename, size});
 * rejects with an ApiError built from the response the same way request()
 * builds one, so a caller renders either failure identically.
 */
export function uploadArchive(file, { onProgress, signal } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/v1/uploads");
    xhr.setRequestHeader("X-CSRF-Token", csrfToken);

    if (signal) {
      if (signal.aborted) {
        reject(new DOMException("aborted", "AbortError"));
        return;
      }
      signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }

    xhr.upload.onprogress = (e) => {
      onProgress?.(e.loaded, e.lengthComputable ? e.total : 0);
    };
    xhr.onabort = () => reject(new DOMException("aborted", "AbortError"));
    xhr.onerror = () => reject(new ApiError(0, "the upload failed", null));
    xhr.onload = () => {
      let payload = null;
      try {
        payload = xhr.responseText ? JSON.parse(xhr.responseText) : null;
      } catch {
        // Fall through to the envelope-less rejection below.
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(payload);
        return;
      }
      reject(
        new ApiError(
          xhr.status,
          payload?.error || `upload failed (${xhr.status})`,
          payload?.details,
        ),
      );
    };

    const form = new FormData();
    form.append("file", file, file.name);
    xhr.send(form);
  });
}

/** Cancels a staged upload nobody imported: 204, no document. */
export const deleteUpload = (uploadID) =>
  del(`/api/v1/uploads/${encodeURIComponent(uploadID)}`);
