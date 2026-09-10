// Package thunderstore: this file is the path-safety gate.
//
// The community slug comes from games.yaml, which the user edits by hand,
// and it is joined onto a filesystem path AND onto a request URL. Both
// happen only after it has passed the pattern below.
package thunderstore

import (
	"fmt"
	"regexp"
)

// communityPattern is the shape Thunderstore itself gives a community slug:
// lowercase alphanumerics and hyphens, starting with an alphanumeric, 64
// characters at most. Deliberately a strict allow-list rather than a
// traversal blacklist - "..", "/", a NUL, an absolute path and a 300-
// character name are all refused by not matching, without anyone having to
// have thought of them.
var communityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// validateCommunity refuses a slug before it is joined onto any path or
// URL. An empty value is reported separately from a malformed one: the
// first is a game with no Thunderstore mapping at all, the second is a
// mapping the user got wrong, and the frontends word them differently.
func validateCommunity(slug string) error {
	if slug == "" {
		return fmt.Errorf("source %q: this game has no Thunderstore community configured (games.yaml: sources: {thunderstore: <community>}): %w",
			sourceID, ErrCommunityNotConfigured)
	}
	if !communityPattern.MatchString(slug) {
		return fmt.Errorf("source %q: %q is not a valid Thunderstore community slug: %w",
			sourceID, slug, ErrCommunityNotConfigured)
	}
	return nil
}
