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
//     slug@revision, or a revision's own page (…/collections/<slug>/
//     revisions/<n>, read as slug@n), over http or https - exactly the
//     forms nexusmods.parseCollectionRef accepts. Both parsers are held to
//     one fixture, internal/source/nexusmods/testdata/collection_refs.json
//     (TestParseCollectionRef, and TestE2E_CollectionLink_AgreesWithTheSourceParser
//     here);
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
  const nexus = nexusRef(trimmed);
  if (nexus) return nexus;

  let url;
  try {
    url = new URL(trimmed);
  } catch {
    return null;
  }
  if (/\.lmmc$/i.test(url.pathname)) {
    return {
      source: "url",
      url: trimmed,
      https: url.protocol === "https:",
    };
  }
  return null;
}

/** positiveInt is strconv.Atoi(s) when that is above zero, and 0
 * otherwise - the revision rule nexusmods.parseCollectionRef applies. */
function positiveInt(s) {
  if (!/^[+-]?[0-9]+$/.test(s)) return 0;
  const n = Number(s);
  return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

/** splitRevision splits a trailing "@<revision>" off text, as
 * nexusmods.parseCollectionRef does before anything else: an "@" whose
 * tail is not a positive number makes the whole reference invalid (null). */
function splitRevision(text) {
  const at = text.lastIndexOf("@");
  if (at < 0) return { body: text, revision: 0 };
  const revision = positiveInt(text.slice(at + 1));
  return revision ? { body: text.slice(0, at), revision } : null;
}

/** nexusRef reads an http(s) address as a NexusMods collection page (or a
 * revision's page), or returns null - nexusmods.parseCollectionRef's URL
 * branch, rule for rule. */
function nexusRef(trimmed) {
  const split = splitRevision(trimmed);
  if (!split) return null;
  let revision = split.revision;

  let url;
  try {
    url = new URL(split.body);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return null;

  // Go's strings.Split(strings.Trim(path, "/"), "/"): an empty segment in
  // the middle is kept, and makes the address no collection page.
  let parts = url.pathname.replace(/^\/+|\/+$/g, "").split("/");
  const host = url.hostname.toLowerCase();
  if (NEXUS_HOSTS.has(host)) {
    if (parts[0] !== "games") return null;
    parts = parts.slice(1);
  } else if (host !== NEXUS_NEXT_HOST) {
    return null;
  }

  if (
    parts.length === 5 &&
    parts[1] === "collections" &&
    parts[3] === "revisions"
  ) {
    // A revision's own page; naming one with "@" as well is refused.
    if (revision) return null;
    revision = positiveInt(parts[4]);
    if (!revision) return null;
  } else if (parts.length !== 3 || parts[1] !== "collections") {
    return null;
  }

  const domain = parts[0];
  const slug = parts[2].toLowerCase();
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
  const split = splitRevision(trimmed);
  if (!split) return null;
  const slug = split.body.toLowerCase();
  if (!NEXUS_SLUG.test(slug)) return null;
  return {
    source: "nexusmods",
    domain: "",
    slug,
    revision: split.revision,
    ref: trimmed,
    // A word on its own is offered only once the search found nothing for
    // it; slug@revision is no search anyone types.
    tentative: split.revision === 0,
  };
}
