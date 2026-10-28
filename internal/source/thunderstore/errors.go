// Package thunderstore: this file is the two failures this source reports
// that a frontend has to tell apart.
package thunderstore

import "github.com/DonovanMods/linux-mod-manager/v2/internal/source"

// ErrIndexUnavailable reports that there is no usable index on disk and one
// could not be built: the fetch failed, or what is cached is unreadable. It
// IS source.ErrIndexUnavailable, not a second error meaning the same thing -
// internal/core classifies it for a frontend that cannot import this
// package, exactly as it does for a Workshop item Valve will not describe.
//
// A refresh that fails over an index which is STILL usable does not produce
// this: it serves the stale copy and reports the failure alongside a Present
// status (source.LocalIndexSource's own contract).
var ErrIndexUnavailable = source.ErrIndexUnavailable

// ErrCommunityNotConfigured reports that the game maps this source to an
// empty or malformed community slug. It is the user's configuration being
// wrong rather than the source failing, so a frontend answers it as bad
// input and can deep-link the sources editor.
//
// This source deliberately does NOT implement source.GameIdentifierIgnorer:
// an empty mapping is a misconfiguration here, not a legitimate state - the
// slug is not derivable from anything else lmm knows, and a guess would
// silently index the wrong community.
var ErrCommunityNotConfigured = source.ErrGameIdentifierInvalid
