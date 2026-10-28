package domain

import (
	"slices"
	"strings"
)

// ValidAdapterName reports whether s is syntactically a legal games.yaml
// `adapter:` value (#353): a non-empty lowercase slug of letters, digits
// and single interior hyphens.
//
// It is a SYNTAX check only, deliberately ignorant of the adapter registry,
// which is how design §2 splits adapter validation by layer -
// internal/storage/config refuses a malformed NAME with ErrInvalidAdapter,
// and whether the named adapter EXISTS is core's question, asked when it
// resolves the game.
//
// It lives here, beside ErrInvalidAdapter, rather than in internal/adapter,
// so the config layer can ask it without importing the seam - and, through
// it, internal/source (#411, M7).
func ValidAdapterName(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	if strings.Contains(s, "--") {
		return false
	}
	return !slices.ContainsFunc([]byte(s), func(c byte) bool {
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		return !lower && !digit && c != '-'
	})
}
