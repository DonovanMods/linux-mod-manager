// workshopcollection.js - is what the user just typed a Steam Workshop
// collection link?
//
// The search box is where a user pastes a Workshop URL, because that is
// where you paste things. Searching for it finds nothing (it is not a
// title), so the page offers the thing they actually meant instead: import
// the collection as a profile (issue 269 W2, design §4).
//
// It is the JS twin of steamworkshop.ParseCollectionURL, and deliberately
// the NARROW parser both are: only an unmistakable Workshop link qualifies.
// A bare 19-digit id does not - that is a perfectly ordinary search term,
// and turning every numeric query into an import offer would be noise. The
// server re-parses the reference for itself; this is an affordance, never a
// validation.

/** WORKSHOP_HOST is the only host a collection link comes from. */
const WORKSHOP_HOST = "steamcommunity.com";

/** PUBLISHED_FILE_ID matches Steam's decimal published-file id. Loose at
 * both ends on purpose: ids were 8 digits in 2012 and are 10 now, and this
 * has no business predicting when they grow again. */
const PUBLISHED_FILE_ID = /^[0-9]{6,20}$/;

/**
 * workshopCollectionRef returns the published-file id in text when text is a
 * Steam Workshop item URL, and "" otherwise.
 *
 * @param {string} text
 * @returns {string}
 */
export function workshopCollectionRef(text) {
  const trimmed = (text ?? "").trim();
  if (!/^https?:\/\//i.test(trimmed)) return "";
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    return "";
  }
  const host = url.hostname.toLowerCase().replace(/^www\./, "");
  if (host !== WORKSHOP_HOST) return "";
  const id = url.searchParams.get("id") ?? "";
  return PUBLISHED_FILE_ID.test(id) ? id : "";
}
