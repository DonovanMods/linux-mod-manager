// collectionoffer.js - the search box's answer to a pasted collection.
//
// A collection's address is not a title, so a search for one finds
// nothing. The search page (issue 269 W2) offered the import a Steam
// Workshop link is actually good for; issue 650 widens that one offer to
// every collection collectionlink.js recognises - a NexusMods collection
// and an lmm collection's https address - and shows it in the omnibar's
// fan-out as well as on the search page, since the omnibar is the search
// box a user actually pastes into.
//
// Each offer opens the same plan as the profiles modal's own form for that
// reference (kind profile_import, with the same origin and title), so the
// two doors cannot drift apart.

import { html } from "../render.js";

/**
 * CollectionOffer renders the offer for link (collectionlink.js's
 * result), or nothing when link is null.
 *
 * noResults is whether the search answered with no mods at all (or failed):
 * a bare NexusMods slug is also an ordinary search word, so it is offered
 * only then.
 *
 * It renders on the search page's ERROR branch too, and that is the point
 * for the Workshop link - a search for a URL against a Workshop source with
 * no key answers "authentication required", which is precisely the moment
 * a user most needs to be told that importing the collection needs no key
 * at all.
 */
export function CollectionOffer({ link, state, actions, noResults }) {
  if (!link) return null;
  switch (link.source) {
    case "steamworkshop":
      return html`<${WorkshopOffer} link=${link} actions=${actions} />`;
    case "nexusmods":
      return html`<${NexusOffer}
        link=${link}
        state=${state}
        actions=${actions}
        noResults=${noResults}
      />`;
    case "url":
      return html`<${URLOffer} link=${link} actions=${actions} />`;
    default:
      return null;
  }
}

/** WorkshopOffer is the Steam Workshop collection offer (issue 269 W2). */
function WorkshopOffer({ link, actions }) {
  return html`
    <p
      class="plan__note"
      data-testid="collection-offer"
      data-source="steamworkshop"
    >
      That looks like a Steam Workshop link.${" "}
      <button
        type="button"
        class="button button--small button--primary"
        onClick=${() =>
          actions.openPlan({
            kind: "profile_import",
            origin: "profile-import-collection",
            title: "Import Steam Workshop collection",
            confirmLabel: "Import",
            options: { workshop_collection: link.ref },
          })}
      >
        Import it as a profile</button
      >${" "}
      <span class="empty-state__hint">No API key needed.</span>
    </p>
  `;
}

/** NexusOffer is the NexusMods collection offer (issue 650). The source
 * resolves a collection against the selected game's NexusMods game, so an
 * address for another game, or a game with no NexusMods source, gets a
 * sentence saying so rather than an import the source would refuse. */
function NexusOffer({ link, state, actions, noResults }) {
  const gameDomain = state?.status?.source_ids?.nexusmods ?? "";
  if (link.tentative && (!noResults || !gameDomain)) return null;
  const gameName = state?.status?.name || state?.route?.game || "this game";

  if (!gameDomain) {
    return html`
      <p
        class="plan__note"
        data-testid="collection-offer"
        data-source="nexusmods"
      >
        That looks like a NexusMods collection, but ${gameName} has no NexusMods
        source to import it from.
      </p>
    `;
  }

  if (link.domain && link.domain !== gameDomain) {
    // The configured game that maps this NexusMods game, if there is one,
    // is named - that is where the import works.
    const other = (state?.games ?? []).find(
      (g) => g.source_ids?.nexusmods === link.domain,
    );
    const sentence = other
      ? `That NexusMods collection is for ${other.name}, not ${gameName}. Choose ${other.name} in the game picker to import it.`
      : `That NexusMods collection is for the NexusMods game ${link.domain}, not ${gameName}, and no game here maps it.`;
    return html`
      <p
        class="plan__note"
        data-testid="collection-offer"
        data-source="nexusmods"
        data-mismatch="true"
      >
        ${sentence}
      </p>
    `;
  }

  const lead = link.tentative
    ? `If “${link.slug}” is a NexusMods collection's slug:`
    : `This is a NexusMods collection${link.revision ? ` (revision ${link.revision})` : ""}:`;
  return html`
    <p
      class="plan__note"
      data-testid="collection-offer"
      data-source="nexusmods"
    >
      ${lead}${" "}
      <button
        type="button"
        class="button button--small button--primary"
        onClick=${() =>
          actions.openPlan({
            kind: "profile_import",
            origin: "profile-import-nexus-collection",
            title: "Import NexusMods collection",
            confirmLabel: "Import",
            options: { nexus_collection: link.ref },
          })}
      >
        Import it…</button
      >${" "}
      <span class="empty-state__hint">
        Imports it as a new profile. Needs a NexusMods API key.
      </span>
    </p>
  `;
}

/** URLOffer is the lmm collection address offer (issue 650): the profiles
 * modal's "From a URL" import, for an address ending .lmmc. */
function URLOffer({ link, actions }) {
  if (!link.https) {
    return html`
      <p class="plan__note" data-testid="collection-offer" data-source="url">
        That looks like a collection link, but lmm fetches a collection over
        https only.
      </p>
    `;
  }
  return html`
    <p class="plan__note" data-testid="collection-offer" data-source="url">
      This is a collection:${" "}
      <button
        type="button"
        class="button button--small button--primary"
        onClick=${() =>
          actions.openPlan({
            kind: "profile_import",
            origin: "profile-import-url",
            title: "Import a collection from a URL",
            confirmLabel: "Import",
            options: { url: link.url },
          })}
      >
        Import it…</button
      >${" "}
      <span class="empty-state__hint">
        Imports it as a new profile. Hooks are never imported from a URL.
      </span>
    </p>
  `;
}
