// router.js - the History API router. The Go server serves the same shell
// for every app route (spa.go), so the URL is read here, in the browser.
//
// The scheme (docs/plans/2026-08-31-serve-spa-design.md §Information
// architecture):
//
//   /                                        game chooser
//   /g/{game}/{profile}                      Mission Control
//   /g/{game}/{profile}/mod/{source}/{id}    full mod page
//   /g/{game}/{profile}/search?q=            search page
//
// The game and profile live in the PATH on purpose: context cannot be lost
// or silently defaulted between views, which is what makes the audit's
// wrong-game bug class structurally impossible rather than merely fixed.
// The slide-over annotates the URL with ?mod=source/id instead of routing,
// so Back closes it.

/** The parsed shape of one URL. view is one of the four names above. */
export function parseLocation(url = window.location) {
  const path = url.pathname.replace(/\/+$/, "") || "/";
  const params = new URLSearchParams(url.search);
  const route = { view: "chooser", game: "", profile: "", query: params };

  const segments = path.split("/").filter(Boolean);
  if (segments[0] !== "g" || segments.length < 3) return route;

  route.game = decodeURIComponent(segments[1]);
  route.profile = decodeURIComponent(segments[2]);
  route.view = "home";

  const rest = segments.slice(3);
  if (rest.length === 0) {
    route.mod = params.get("mod") || "";
    return route;
  }
  if (rest[0] === "search") {
    route.view = "search";
    route.q = params.get("q") || "";
    return route;
  }
  if (rest[0] === "mod" && rest.length >= 3) {
    route.view = "mod";
    route.sourceID = decodeURIComponent(rest[1]);
    route.modID = decodeURIComponent(rest.slice(2).join("/"));
    return route;
  }
  return route;
}

/** Builds the /g/{game}/{profile} prefix every scoped URL hangs off. */
export function contextPath(game, profile) {
  return `/g/${encodeURIComponent(game)}/${encodeURIComponent(profile)}`;
}

/**
 * Navigates without a document load. replace: true is for the URL edits
 * that are not a new place - opening the slide-over annotates the current
 * URL, and stacking a history entry per row click would turn Back into a
 * long walk back through hover-depth decisions.
 */
export function navigate(to, { replace = false } = {}) {
  if (replace) {
    window.history.replaceState(null, "", to);
  } else {
    window.history.pushState(null, "", to);
  }
  window.dispatchEvent(new PopStateEvent("popstate"));
}

/** Subscribes to route changes; returns an unsubscribe function. */
export function onRouteChange(handler) {
  const listener = () => handler(parseLocation());
  window.addEventListener("popstate", listener);
  return () => window.removeEventListener("popstate", listener);
}
