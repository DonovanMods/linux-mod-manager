package adapter

// Generic is the identity game adapter: the one every game gets when
// games.yaml says nothing about an adapter, and the one that makes
// "byte-for-byte identical behaviour for every existing game" a provable
// claim rather than an aspiration (docs/plans/2026-09-10-game-adapter-design.md
// §1, §6).
//
// It implements ONLY GameAdapter's three required methods. It has no
// FileRouter, no Preconditioner, no Verifier, no Guide and no
// MergeCompiler, so every capability core type-asserts for is absent and
// every seam takes its identity branch. Its NormalizeArchive returns the
// zero Layout for every input, whose Applies is false and whose Rewrite is
// the identity function - so an archive lands in the cache exactly where
// the extractor put it.
//
// It lives in this package, not a subpackage of its own, because it is the
// registry's BUILT-IN default: NewRegistry always resolves GenericID to it,
// so Resolve never hands core a nil adapter and no seam has to remember
// that nil means identity. That implicit contract - one every future seam
// call would have to re-remember - is the class of bug this design exists
// to prevent (coordinator ruling, 2026-09-10). The composition root
// registers only NAMED adapters on top of it.
//
// There is deliberately nothing to configure here. The day this type grows
// a rule is the day the claim above stops being provable.
type Generic struct{}

// ID returns GenericID ("generic-files"), the games.yaml value and registry
// key.
func (Generic) ID() string { return GenericID }

// Label returns the display name frontends show for this adapter.
func (Generic) Label() string { return "Generic files" }

// NormalizeArchive returns the zero Layout for every request: no rewrite,
// no drop, no warning. This is the identity, and identity_test.go pins it
// over every archive shape internal/core's import tests exercise.
func (Generic) NormalizeArchive(NormalizeRequest) (Layout, error) {
	return Layout{}, nil
}
