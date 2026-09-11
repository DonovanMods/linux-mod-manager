// Package adapter is the game-adapter seam (#353): the one place lmm says
// "what this GAME does with mod content", as opposed to internal/source's
// "where the bytes came from".
//
// The seam already existed before this package - spelled three different
// ways and hung off the wrong noun. adapter.MergeCompiler made compilation a
// property of a source, so an Icarus .pak downloaded from NexusMods could
// not compile; archive layout was a bool parameter threaded through core;
// .EXMODZ's wrapper strip lived inside a format parser. Each is really a
// property of the game, and GameAdapter is that noun.
//
// The division of labour is the design's load-bearing decision
// (docs/plans/2026-09-10-game-adapter-design.md §1): an adapter supplies
// pure RULE TABLES and read-only REPORTS; internal/core keeps every side
// effect it has today - it applies the rewrites, writes the files, runs the
// repairs, emits the events. So an adapter is a table test, a bug in the
// rewriting is a bug in exactly one place, and this package imports neither
// internal/core nor internal/linker (internal/adapter/boundary_test.go is
// the ratchet).
//
// Only the three GameAdapter methods are required. Everything else is an
// optional capability interface core type-asserts for - the idiom
// internal/source already proves with CapabilityReporter, MergeCompiler and
// WorkshopScanner - so no adapter pays for a capability it does not have
// and core never imports a concrete adapter. Dispatch goes through this
// package's nil-safe helpers (Route, CheckPreconditions, Verify, Guidance,
// Compiler), which is what lets a core seam be one unconditional call
// instead of a type switch. NormalizeArchive needs no such helper: it is a
// required method, so every adapter answers it.
package adapter

import (
	"context"
	"errors"
	"path"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// GenericID is the adapter every game gets when games.yaml says nothing:
// the identity, implemented by Generic in this package (see identity.go
// for why it does not live in a subpackage of its own). It is the config
// spelling as well as the registry key, so an explicit `adapter:
// generic-files` and an absent key resolve to the same adapter.
const GenericID = "generic-files"

// GameAdapter is the whole required surface. An adapter that implements
// only these three methods is a legal adapter and behaves exactly as lmm
// behaved before #353.
type GameAdapter interface {
	// ID is the games.yaml value this adapter is selected by
	// ("generic-files", "icarus", "bepinex"), and its registry key.
	ID() string

	// Label is the display name a frontend shows
	// ("BepInEx (Unity plugin loader)"). Display only - nothing branches
	// on it.
	Label() string

	// NormalizeArchive is the adapter's rule TABLE, not its executor: it
	// maps an archive's member list to the cache-entry-relative paths
	// those members take, and says nothing about disk. Core owns the tree
	// rewriter that applies the answer.
	NormalizeArchive(req NormalizeRequest) (Layout, error)
}

// NormalizeRequest is NormalizeArchive's whole input. It carries no
// filesystem handle on purpose: an adapter's layout rules are a pure
// function of the archive's member list and the game's configuration, which
// is what makes them table-testable without a temp directory.
type NormalizeRequest struct {
	// Game is the game the archive is being ingested for, including any
	// loader block it carries. Never mutated by an adapter.
	Game *domain.Game
	// ModName is the mod's name, for adapters that name a directory after
	// the mod.
	//
	// On the archive-import path it is derived from the archive's own
	// shape BEFORE any rewrite, and the plan and the ingest derive it the
	// same way, so an adapter that consults it lays the same tree out on
	// both sides. It is EMPTY on the download path, where the mod's name
	// comes from its source rather than from the archive - an adapter
	// that names a directory after the mod must tolerate that and fall
	// back to a name it can derive from Members.
	ModName string
	// Members are the archive's members: SLASH-separated, archive-relative,
	// files only, and SORTED - core normalises once, at the seam. An
	// adapter that string-matches a member can therefore write
	// "BepInEx/config/" without caring what separator the host filesystem
	// uses, and one whose rules depend on order ("the first .dll is the
	// plugin") gets the same order from a plan and from the ingest that
	// applies it, which the plan/ingest agreement property needs.
	Members []string
}

// Layout is one answer from NormalizeArchive: how an archive's members are
// renamed, dropped, or left alone on their way into the cache entry.
//
// A ZERO Layout is the identity - Applies reports false and Rewrite returns
// its argument unchanged - which is why core can hold one unconditionally
// instead of branching at every member, and why the generic adapter needs
// no code at all to reproduce today's behaviour.
type Layout struct {
	// Kind is an adapter-defined diagnostic string
	// ("game-root-relative"). It is never a wire value and nothing
	// branches on it; it exists so a log line or a test failure can say
	// which rule fired.
	Kind string
	// Warnings are surfaced verbatim to the user. An adapter warns; it
	// never guesses.
	Warnings []string

	// rewrites maps an archive member to the cache-entry-relative path it
	// takes. A member absent from the map keeps its own name; a member
	// mapped to "" is dropped.
	rewrites map[string]string
	// applies is set by NewLayout, so a Layout with an empty-but-non-nil
	// rewrite table still reports the adapter as having had an opinion.
	applies bool
}

// NewLayout builds a Layout from an adapter's rewrite table: member ->
// cache-entry-relative path, with "" meaning "drop this member". kind is
// the diagnostic label. Passing a nil table yields the identity Layout,
// which is how an adapter says "these members are already where they
// belong".
func NewLayout(kind string, rewrites map[string]string) Layout {
	if rewrites == nil {
		return Layout{Kind: kind}
	}
	return Layout{Kind: kind, rewrites: rewrites, applies: true}
}

// Applies reports whether this Layout rewrites anything at all. False is
// the identity, and core skips the tree rewriter entirely for it - the
// cheap path every generic-files game takes.
func (l Layout) Applies() bool { return l.applies }

// Rewrite maps one archive member to the cache-entry-relative path it takes
// and reports whether it is kept. A member the Layout says nothing about
// keeps its own name, so an adapter's table only lists what it MOVES.
//
// The destination is CANONICALISED here, once, with path.Clean (#411, R1):
// "./out/a.dll", "out//a.dll", "out/./a.dll" and "out/a.dll/" are all the
// same destination, and core's validator, its kept-member list, its plan
// rewriter and its executor must every one of them see the same string -
// the executor writes through filepath.Join, which cleans, so any half that
// kept the adapter's raw spelling disagreed with the file that actually
// appeared. Canonicalising at this single point is what makes "the
// destination you get back is the destination core writes" a guarantee an
// adapter author can rely on rather than an unstated precondition.
//
// A destination whose canonical form is UNUSABLE - empty, ".", absolute, or
// escaping the cache entry with a leading ".." - is returned in that
// canonical form and refused by core, which is the layer that owns the
// write and can name the offending member in a typed error.
func (l Layout) Rewrite(member string) (string, bool) {
	if !l.applies {
		return member, true
	}
	dest, ok := l.rewrites[member]
	if !ok {
		return member, true
	}
	if dest == "" {
		return "", false
	}
	return path.Clean(dest), true
}

// FileRoute says what happens to one deployable file. It is how "this file
// is not ordinary mod content" is said, and RouteLink - the zero value - is
// what every file of every generic game gets.
type FileRoute int

const (
	// RouteLink is the default: the linker deploys it, exactly as lmm has
	// always deployed every file.
	RouteLink FileRoute = iota
	// RouteCopyOnce is a real file, copied on first deploy, never
	// overwritten, and never entered into deployed_files - the semantics
	// applyProfileOverrides already implements, reused rather than
	// reinvented (a user hand-edits the file after first run; a symlink
	// would push that edit back into the shared cache entry).
	RouteCopyOnce
	// RouteSkip is ingested and cached, but never deployed.
	RouteSkip
)

// String returns the route's diagnostic name.
func (r FileRoute) String() string {
	switch r {
	case RouteLink:
		return "link"
	case RouteCopyOnce:
		return "copy-once"
	case RouteSkip:
		return "skip"
	default:
		return "unknown"
	}
}

// FileRouter is the optional capability that classifies deployable files.
// An adapter without one gets RouteLink for every file, which is today's
// behaviour exactly.
type FileRouter interface {
	// RouteFile classifies one cache-entry-relative file of a mod being
	// deployed into g. rel is SLASH-separated, like
	// NormalizeRequest.Members and for the same reason: core converts
	// once, at the seam, so an adapter's own path rules are written one
	// way.
	RouteFile(g *domain.Game, rel string) FileRoute
}

// Preconditioner is the optional capability that refuses a flow before it
// starts - a loader that has to be installed first, a game directory that
// is not what the adapter expects. Its error is wrapped by core into a
// typed error carrying the remedy to the frontend.
type Preconditioner interface {
	// CheckPreconditions inspects the game and the mods a flow is about to
	// act on. It is read-only: it never touches the game directory.
	CheckPreconditions(g *domain.Game, mods []domain.InstalledMod) error
}

// Finding is one read-only observation from an adapter's Verify. Its fields
// map one-for-one onto core.VerifyFinding, so an adapter row renders as an
// ordinary verify row in every frontend that already exists.
type Finding struct {
	// Status is the finding's machine status, in VerifyFinding's
	// vocabulary ("ok", "skipped", or an adapter-defined status).
	Status string
	// Note is the human-facing explanation.
	Note string
	// Fixable reports whether `verify --fix` would attempt a repair. No
	// adapter finding is fixable in 2.0 (design §1): the one BepInEx check
	// that could be repaired is core's own per-file deployment pass, not
	// an adapter check.
	Fixable bool
	// FixableReason says why a non-fixable finding cannot be repaired -
	// which for an adapter finding is an instruction to the user.
	FixableReason string
}

// VerifyRequest is Verify's whole input: the game and the mods installed in
// the profile being verified.
type VerifyRequest struct {
	// Game is the game being verified.
	Game *domain.Game
	// Mods are the profile's installed mods, in profile order.
	Mods []domain.InstalledMod
}

// Verifier is the optional capability that adds adapter-specific verify
// checks - the set core cannot see (a preloader present, declared-version
// drift, bootstrap files matching the declared mode). Adapters report; core
// repairs.
type Verifier interface {
	// Verify is read-only and must not write to the game directory.
	Verify(ctx context.Context, req VerifyRequest) ([]Finding, error)
}

// GuidanceNote is one piece of launch/bootstrap advice an adapter offers
// for a game - the text `lmm game list`, `lmm verify` and the web game card
// render after their own output.
type GuidanceNote struct {
	// Title is the note's one-line heading.
	Title string
	// Body is the note's text.
	Body string
}

// Guide is the optional capability that supplies GuidanceNotes.
type Guide interface {
	// Guidance returns the notes for g, or nil when there is nothing to
	// say.
	Guidance(g *domain.Game) []GuidanceNote
}

// MergeCompiler is the optional capability of an adapter whose
// compile-eligible files must be merged across every enabled mod into ONE
// profile-level artifact rather than compiled per-mod (#197: Icarus's
// cross-mod table merge - a whole-pak last-wins deploy would silently drop
// one mod's table rows whenever two mods patch the same table).
//
// It was adapter.MergeCompiler until U2 (#412), which is the mistake #353
// exists to correct: compilation was a property of WHERE THE BYTES CAME
// FROM, so an Icarus .pak downloaded from NexusMods could not compile while
// the same file from Project Daedalus could. It is a property of the GAME,
// and this is the noun that says so. The declaration moved here unchanged
// but for that framing; internal/source no longer has it.
//
// This interface is the complete contract a DeployCompile game must
// implement (#256): the merge operations (ValidateSource, MergeCompile)
// plus the format vocabulary core needs to orchestrate them without knowing
// the game's artifact format itself - where the base artifact lives
// (ResolveBaseArtifact), how to fingerprint it (FingerprintBase), which
// files are the adapter's native merge format (IsNativeMergeSource), which
// are convertible raw artifacts (IsConvertibleArtifact,
// ClassifyMergeSource), what the merged output is called
// (MergedArtifactName, MergedArtifactLabel), and what a healed raw-fallback
// copy is called (RestoredArtifactName). A second compile-mode game is
// a new package under internal/adapter implementing these methods plus one
// registration line; internal/core never changes.
type MergeCompiler interface {
	// ValidateSource parses/validates sourceFilePath (the retained,
	// not-yet-merged source archive) without compiling anything - called at
	// ingest time (download/import) so a malformed archive fails loud
	// immediately rather than at the next merge.
	ValidateSource(sourceFilePath string) error

	// MergeCompile applies every entry in sources, in order (profile load
	// order), against the base artifact's tables, and writes the merged
	// result to outputPath. Returns non-fatal warnings (e.g. same-path
	// asset collisions - last-applied wins) alongside a nil error; a nil
	// error with warnings is still a fully-written, deployable artifact.
	// Convertible-kind sources that cannot be converted are skipped per-mod
	// and reported in failed (#221) - only native-source errors and I/O
	// failures are fatal.
	MergeCompile(ctx context.Context, baseArtifactPath string, sources []MergeSource, outputPath string) (warnings []string, failed []MergeFailure, err error)

	// ResolveBaseArtifact locates the installed game's base artifact - the
	// input every merge applies against (Icarus: the game's own
	// Content/Data/data.pak). Errors when the artifact cannot be found
	// under the game's install path.
	ResolveBaseArtifact(game *domain.Game) (string, error)

	// FingerprintBase returns an opaque fingerprint of the base artifact at
	// baseArtifactPath: cheap to compute, changing exactly when the base
	// content changes. Core stores it in merge fingerprints to detect a
	// game-update-invalidated merge; it never interprets the value.
	FingerprintBase(baseArtifactPath string) (string, error)

	// IsNativeMergeSource reports whether fileName names this adapter's
	// NATIVE merge-source format (Icarus: a ".exmodz" archive) - the diff
	// format MergeCompile consumes directly, with no other valid
	// interpretation at ingest. Pure format test, the mirror of
	// IsConvertibleArtifact - core owns the DeployCompile policy gates and
	// routes native files into validate+retain instead of extract/copy.
	IsNativeMergeSource(fileName string) bool

	// IsConvertibleArtifact reports whether fileName names a raw, prebuilt
	// game artifact this adapter can convert into a merge source (#221;
	// Icarus: a ".pak" file). Pure format test - core owns the
	// DeployCompile/ConvertPaks policy gates that decide whether such a
	// file actually enters the merge-convert pipeline.
	IsConvertibleArtifact(fileName string) bool

	// ClassifyMergeSource maps a retained-source identity - a fileID, an
	// imported archive's filename, or a Kind string previously recorded on
	// a merge fingerprint - to the adapter-defined kind string core
	// round-trips (MergeSource.Kind, fingerprint entries) and whether that
	// kind is a convertible raw artifact (subject to the ConvertPaks
	// opt-out and per-mod conversion outcomes) as opposed to the adapter's
	// native mergeable format. Must accept the empty string (legacy
	// fingerprints recorded no Kind) and classify it as the native kind.
	ClassifyMergeSource(id string) (kind string, convertible bool)

	// MergedArtifactName names the single merged output artifact core
	// deploys into the game's mod directory. The name is a deploy contract:
	// it must stay stable across merges (core stats/replaces it by name),
	// and any load-order significance it carries (Icarus: sorts last so it
	// wins) is entirely the adapter's concern.
	MergedArtifactName() string

	// MergedArtifactLabel is the user-facing display name for the merged
	// artifact's synthetic mod row (verify/update output).
	MergedArtifactLabel() string

	// RestoredArtifactName names the deployable raw-fallback copy core
	// synthesizes when healing a prune-damaged cache entry whose original
	// artifact name is unrecoverable (#250; Icarus: "<modID>_P.pak").
	// Deterministic per mod - the same mod must always restore to the same
	// name, since the name is on-disk state existing installs depend on.
	// Core passes a path-safe (Base'd) modID and uses the result as a
	// filename within the mod's own cache entry.
	RestoredArtifactName(modID string) string
}

// MergeSource identifies one mod's contribution to a merge, in the order it
// must be applied (profile load order). Moved here from internal/source in
// U2 (#412) with MergeCompiler.
type MergeSource struct {
	ModRef     string // "sourceID:modID" - machine identity (MergeFailure, ownership tracking)
	ModName    string // display name preferred over ModRef in user-facing warnings; may be empty
	SourcePath string // the retained source archive to read (native diff, or a convertible raw artifact - #221)
	Kind       string // adapter-defined kind from ClassifyMergeSource; empty means the adapter's native kind (#256)
}

// MergeFailure records one mod's source archive that could not participate in a merge
// (#221: an irreconcilable pak). Moved here from internal/source in U2
// (#412) with MergeCompiler. The merge itself still succeeds - the
// failed mod is skipped and falls back to raw deploy; core uses this list
// to reconcile cache manifests and record outcomes in the fingerprint.
type MergeFailure struct {
	ModRef string
	Reason string
}

// ErrNotAMod is the refusal an adapter makes for an archive that is the
// loader or the base game rather than a mod for it (#358's framework-pack
// refusal, generalised). Adapters wrap it with a message naming the remedy;
// core translates it into a typed error carrying that message to the
// frontend.
var ErrNotAMod = errors.New("this archive is not a mod for this game")

// ErrPreconditionUnmet is the sentinel a Preconditioner wraps when a flow
// cannot proceed until the user does something first.
var ErrPreconditionUnmet = errors.New("the game's adapter refused this operation")

// Route classifies one deployable file: an adapter that implements no
// FileRouter routes every file RouteLink - today's behaviour exactly.
func Route(a GameAdapter, g *domain.Game, rel string) FileRoute {
	rt, ok := a.(FileRouter)
	if !ok {
		return RouteLink
	}
	return rt.RouteFile(g, rel)
}

// CheckPreconditions runs the adapter's precondition check, or reports
// success when it has none.
func CheckPreconditions(a GameAdapter, g *domain.Game, mods []domain.InstalledMod) error {
	pc, ok := a.(Preconditioner)
	if !ok {
		return nil
	}
	return pc.CheckPreconditions(g, mods)
}

// Verify runs the adapter's verify pass, or returns no findings when it has
// none.
func Verify(ctx context.Context, a GameAdapter, req VerifyRequest) ([]Finding, error) {
	v, ok := a.(Verifier)
	if !ok {
		return nil, nil
	}
	return v.Verify(ctx, req)
}

// Guidance returns the adapter's notes for g, or nil when it offers none.
func Guidance(a GameAdapter, g *domain.Game) []GuidanceNote {
	gd, ok := a.(Guide)
	if !ok {
		return nil
	}
	return gd.Guidance(g)
}

// Compiler reports whether the adapter is the compile capability, and
// returns it. This is the expression #353 replaces "walk the game's sources
// looking for one that implements MergeCompiler" with: one game, one
// adapter, zero ambiguity.
func Compiler(a GameAdapter) (MergeCompiler, bool) {
	mc, ok := a.(MergeCompiler)
	return mc, ok
}
