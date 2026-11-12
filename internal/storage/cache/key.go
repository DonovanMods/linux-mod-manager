package cache

// VariantSeparator joins a version to its variant in a cache entry's
// directory name: <version>@<variant>. Not "+", which is semver build
// metadata and which version comparison ignores - a stray parse of the
// directory name must never read a variant as part of a version
// (collections design §4, #603).
const VariantSeparator = "@"

// Key identifies one cache entry: a mod's files at one version, from one
// source, for one game, optionally narrowed to one variant of that version.
//
// The on-disk spelling is <base>/<GameID>/<SourceID>-<ModID>/<Version> (a
// per-game cache_path omits the <GameID> segment), with a non-empty Variant
// as a SIBLING directory, <Version>@<Variant>. An empty Variant - every key
// today - spells exactly the path the four loose strings did before Key
// existed.
//
// The spelling is not injective. Source versions are free text and may
// contain VariantSeparator, so a plain key can name the same directory as a
// variant key: version "1.0@abcdef123456" with no variant, and version "1.0"
// with variant "abcdef123456". The cache does not refuse such versions (a
// version a source publishes stays installable), so preventing the collision
// is the variant minter's job (F2, #614), in both directions - a variant
// entry minted where a plain version's directory already exists, and a plain
// version installed where a variant entry already exists.
type Key struct {
	// GameID is the game the entry belongs to. A game-scoped cache (a
	// per-game cache_path) leaves it out of the path.
	GameID string
	// SourceID is the mod source the entry came from.
	SourceID string
	// ModID is the mod's ID within its source.
	ModID string
	// Version is the source's version string, compared and displayed
	// unchanged; it never carries the variant.
	Version string
	// Variant distinguishes two trees built from the same source file
	// (e.g. different installer choices). Empty means the plain entry.
	Variant string
}

// versionDir is the last path segment of k's entry: the version, or
// <version>@<variant> for a variant.
func (k Key) versionDir() string {
	if k.Variant == "" {
		return k.Version
	}
	return k.Version + VariantSeparator + k.Variant
}

// modDir is the per-mod container segment every version of the mod lives
// under.
func (k Key) modDir() string {
	return k.SourceID + "-" + k.ModID
}
