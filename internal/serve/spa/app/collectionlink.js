// collectionlink.js - is what the user just typed into search a collection?
//
// Issue 650: the search box is where a user pastes a link, and a
// collection's address is not a title, so searching for it finds nothing.
// workshopcollection.js answered that for a Steam Workshop link (issue 269
// W2); this widens the same question to every collection the profile
// import takes from a reference:
//
//   - a NexusMods collection: its page address (www.nexusmods.com/games/
//     <game>/collections/<slug>, or next.nexusmods.com/<game>/collections/
//     <slug>), optionally suffixed "@<revision>", or a bare slug or
//     slug@revision - the forms nexusmods.parseCollectionRef accepts. A
//     revision page (…/collections/<slug>/revisions/<n>) is the website's
//     own address for one revision, and is read as slug@n, the form the
//     source accepts for it;
//   - an lmm collection's address: an http(s) URL whose path ends .lmmc.
//     Only https imports (core.parseCollectionURL); a plain-http one is
//     still recognised, so the offer can say why it will not import;
//   - a Steam Workshop link, exactly as workshopCollectionRef reads it.
//
// It stays the NARROW parser workshopcollection.js is: a bare slug is also
// an ordinary search word, so it is marked tentative and the offer for it
// waits until the search found nothing. The server re-parses every
// reference for itself; this is an affordance, never a validation.

import { workshopCollectionRef } from "./workshopcollection.js";

/** NEXUS_SLUG is a collection slug as nexusmods.slugPattern takes it. */
const NEXUS_SLUG = /^[a-z0-9]{1,32}$/;

/** NEXUS_DOMAIN is a NexusMods game domain as nexusmods.domainPattern
 * takes it. */
const NEXUS_DOMAIN = /^[a-z0-9][a-z0-9_-]{0,63}$/;

/** NEXUS_HOSTS are the hosts a collection page lives on. */
const NEXUS_HOSTS = new Set(["nexusmods.com", "www.nexusmods.com"]);

/** NEXUS_NEXT_HOST is the older collection site's host. */
const NEXUS_NEXT_HOST = "next.nexusmods.com";

/**
 * collectionLink classifies text as a collection reference, or returns
 * null when it is not one.
 *
 * The NexusMods result's ref is what the import is planned with: the page
 * address rebuilt in its canonical https form (query and fragment, which
 * the source never reads, dropped), "@<revision>" appended when one was
 * named, or the slug as typed.
 *
 * @param {string} text
 * @returns {null
 *   | {source: "steamworkshop", ref: string}
 *   | {source: "nexusmods", domain: string, slug: string, revision: number,
 *      ref: string, tentative: boolean}
 *   | {source: "url", url: string, https: boolean}}
 */
export function collectionLink(text) {
  const trimmed = (text ?? "").trim();
  if (!trimmed) return null;

  const workshop = workshopCollectionRef(trimmed);
  if (workshop) return { source: "steamworkshop", ref: workshop };

  if (/^https?:\/\//i.test(trimmed)) return urlLink(trimmed);
  return bareSlug(trimmed);
}

/** urlLink reads an http(s) address: a NexusMods collection page or a
 * .lmmc file. */
function urlLink(trimmed) {
  // nexusmods.parseCollectionRef splits a trailing "@<revision>" off the
  // whole reference before parsing the URL; so does this.
  let body = trimmed;
  let revision = 0;
  const at = body.lastIndexOf("@");
  if (at >= 0 && /^[1-9][0-9]*$/.test(body.slice(at + 1))) {
    revision = Number(body.slice(at + 1));
    body = body.slice(0, at);
  }

  let url;
  try {
    url = new URL(body);
  } catch {
    return null;
  }

  const nexus = nexusPage(url, revision);
  if (nexus) return nexus;

  if (revision === 0 && /\.lmmc$/i.test(url.pathname)) {
    return {
      source: "url",
      url: trimmed,
      https: url.protocol === "https:",
    };
  }
  return null;
}

/** nexusPage reads url as a NexusMods collection page, or returns null. */
function nexusPage(url, atRevision) {
  const host = url.hostname.toLowerCase();
  const parts = url.pathname.split("/").filter((p) => p !== "");
  let domain, slug, rest;
  if (NEXUS_HOSTS.has(host)) {
    if (parts[0] !== "games" || parts[2] !== "collections") return null;
    [domain, slug, rest] = [parts[1], parts[3], parts.slice(4)];
  } else if (host === NEXUS_NEXT_HOST) {
    if (parts[1] !== "collections") return null;
    [domain, slug, rest] = [parts[0], parts[2], parts.slice(3)];
  } else {
    return null;
  }

  let revision = atRevision;
  if (rest.length === 2 && rest[0] === "revisions" && atRevision === 0) {
    if (!/^[1-9][0-9]*$/.test(rest[1])) return null;
    revision = Number(rest[1]);
  } else if (rest.length !== 0) {
    return null;
  }

  domain = (domain ?? "").toLowerCase();
  slug = (slug ?? "").toLowerCase();
  if (!NEXUS_DOMAIN.test(domain) || !NEXUS_SLUG.test(slug)) return null;

  const page = `https://www.nexusmods.com/games/${domain}/collections/${slug}`;
  return {
    source: "nexusmods",
    domain,
    slug,
    revision,
    ref: revision ? `${page}@${revision}` : page,
    tentative: false,
  };
}

/** bareSlug reads a slug or slug@revision typed on its own. */
function bareSlug(trimmed) {
  const m = /^([A-Za-z0-9]{1,32})(?:@([1-9][0-9]*))?$/.exec(trimmed);
  if (!m) return null;
  const revision = m[2] ? Number(m[2]) : 0;
  return {
    source: "nexusmods",
    domain: "",
    slug: m[1].toLowerCase(),
    revision,
    ref: trimmed,
    // A word on its own is offered only once the search found nothing for
    // it; slug@revision is no search anyone types.
    tentative: revision === 0,
  };
}
