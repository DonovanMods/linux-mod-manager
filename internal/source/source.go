package source

import (
	"context"
	"errors"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Token represents an OAuth token
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// SearchResult contains paginated search results.
type SearchResult struct {
	Mods       []domain.Mod
	TotalCount int // Total results available (0 if unknown)
	Page       int
	PageSize   int
}

// SearchQuery contains parameters for searching mods.
type SearchQuery struct {
	GameID   string
	Query    string
	Category string   // Optional category filter (source-specific: ID or name)
	Tags     []string // Optional tag filters (source-specific)
	Page     int
	PageSize int
}

// ModSource is the interface for mod repositories
type ModSource interface {
	// Identity
	ID() string
	Name() string

	// Authentication
	AuthURL() string
	ExchangeToken(ctx context.Context, code string) (*Token, error)

	// Discovery
	Search(ctx context.Context, query SearchQuery) (SearchResult, error)
	GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error)
	GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error)

	// Downloads
	GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error)
	GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error)

	// Updates
	CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error)
}

// UpdateProgressFunc reports one per-mod tick during an update check:
// n is the 1-based index within the batch handed to the source, total the
// batch size.
type UpdateProgressFunc func(n, total int, modName string)

// UpdateProgressReporter is implemented by sources that check mods one at
// a time and can report progress. Core uses it when present and falls
// back to ModSource.CheckUpdates otherwise.
type UpdateProgressReporter interface {
	CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report UpdateProgressFunc) ([]domain.Update, error)
}

// ChangelogProvider is implemented by sources that can supply a mod's
// changelog text for a specific version - the same optional-capability
// pattern as UpdateProgressReporter (#87). Core calls it when present and
// leaves ModDetail.Changelog empty otherwise; a call error degrades to a
// Note rather than failing the caller. Unlike Mod.Description (which stays
// raw source markup all the way to `--json` by existing precedent, #86),
// an implementation MUST return plain text - strip any markup itself (see
// nexusmods.stripChangelogHTML) - so ModDetail.Changelog never needs
// sanitizing by a downstream HTML renderer (e.g. a future `lmm serve` page).
type ChangelogProvider interface {
	Changelog(ctx context.Context, sourceGameID, modID, version string) (string, error)
}

// LocalFileServer marks a source that may legitimately return file:// download
// URLs (a directory source). core refuses file:// URLs from any source that
// does not implement it or whose ServesLocalFiles returns false: a remote
// source (NexusMods, CurseForge, a compromised custom API/manifest source,
// ...) returning file:// must never be trusted to read arbitrary paths off
// disk into the cache (#300).
type LocalFileServer interface{ ServesLocalFiles() bool }

// ErrNotSupported indicates a source does not support the requested operation.
// Callers should branch with errors.Is(err, ErrNotSupported) and degrade
// gracefully (hide the action, show a notice) rather than treat it as a failure.
var ErrNotSupported = errors.New("operation not supported by this source")

// Capabilities reports which optional operations a source supports.
type Capabilities struct {
	Search       bool
	Dependencies bool
	Updates      bool
	Auth         bool
	// Versions: the source CAN carry per-file Version strings usable for
	// exact version->file resolution (#96). Advisory, not a guarantee:
	// resolution itself degrades dynamically per mod - a file list with no
	// version data yields ErrNotSupported even when this is true (see
	// core.ResolveVersionFiles).
	Versions bool
}

// CapabilityReporter is implemented by sources that support only a subset of
// ModSource operations. Sources that do not implement it are assumed fully
// capable.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// EnvKeyProvider names the environment variable consulted for this source's
// API key. Absent: the derived LMM_<ID>_API_KEY convention applies.
type EnvKeyProvider interface{ EnvKey() string }

// KeyValidator performs a live API-key check at auth-login time.
// Absent: keys are stored and validated on first use.
type KeyValidator interface {
	ValidateKey(ctx context.Context, key string) error
}

// AuthInstructionsProvider supplies human setup steps for obtaining a key.
// Absent: generic instructions naming the env var.
type AuthInstructionsProvider interface{ AuthInstructions() string }

// GameEntry is one game known to a source's catalog, for interactive
// game-creation flows.
type GameEntry struct{ ID, Name, Slug string }

// GameCatalog lists the games a source knows about, for interactive
// game-creation flows. Absent: manual identifier entry.
type GameCatalog interface {
	ListGames(ctx context.Context) ([]GameEntry, error)
}

// TypeLabeler names the source's kind for listings (directory/manifest/api/
// built-in). Absent: "unknown".
type TypeLabeler interface{ TypeLabel() string }

// TypeLabelOf returns src's self-reported kind ("directory"/"manifest"/
// "api" for custom sources, "built-in" for NexusMods/CurseForge), falling
// back to "unknown" when src implements no TypeLabeler. Mirrors
// CapabilitiesOf's optional-interface pattern; the fallback is unreachable
// in production (every real source implements TypeLabeler), reachable only
// by bare test doubles.
func TypeLabelOf(src ModSource) string {
	if tl, ok := src.(TypeLabeler); ok {
		return tl.TypeLabel()
	}
	return "unknown"
}

// CapabilitiesOf returns src's capabilities. Sources that do not implement
// CapabilityReporter are assumed fully capable — a default kept for test
// doubles; production sources should implement CapabilityReporter
// explicitly rather than rely on this fallback.
func CapabilitiesOf(src ModSource) Capabilities {
	if cr, ok := src.(CapabilityReporter); ok {
		return cr.Capabilities()
	}
	return Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}
}

// DownloadHeaderProvider is implemented by sources whose file downloads need
// extra HTTP headers (e.g. header-mode API-key auth on a manifest source).
// Service.DownloadModToCache consults it with the resolved download URL so
// the source can scope credentials (e.g. same-origin only). A nil map means
// no extra headers.
type DownloadHeaderProvider interface {
	DownloadHeaders(fileURL string) map[string]string
}

// MergeCompiler is implemented by sources whose compile-eligible files must
// be merged across every enabled mod into ONE profile-level artifact rather
// than compiled per-mod (#197: Icarus's cross-mod table merge - a whole-pak
// last-wins deploy would silently drop one mod's table rows whenever two
// mods patch the same table). Replaces #196's Compiler interface, which
// this source no longer implements: there is no more per-mod compiled
// artifact to produce.
//
// This interface is the complete contract a DeployCompile game must
// implement (#256): the merge operations (ValidateSource, MergeCompile)
// plus the format vocabulary core needs to orchestrate them without knowing
// the game's artifact format itself - where the base artifact lives
// (ResolveBaseArtifact), how to fingerprint it (FingerprintBase), which
// files are the source's native merge format (IsNativeMergeSource), which
// are convertible raw artifacts (IsConvertibleArtifact,
// ClassifyMergeSource), what the merged output is called
// (MergedArtifactName, MergedArtifactLabel), and what a healed raw-fallback
// copy is called (RestoredArtifactName). A second compile-mode game is
// a new source package implementing these methods plus one registration
// line; internal/core never changes.
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

	// IsNativeMergeSource reports whether fileName names this source's
	// NATIVE merge-source format (Icarus: a ".exmodz" archive) - the diff
	// format MergeCompile consumes directly, with no other valid
	// interpretation at ingest. Pure format test, the mirror of
	// IsConvertibleArtifact - core owns the DeployCompile policy gates and
	// routes native files into validate+retain instead of extract/copy.
	IsNativeMergeSource(fileName string) bool

	// IsConvertibleArtifact reports whether fileName names a raw, prebuilt
	// game artifact this source can convert into a merge source (#221;
	// Icarus: a ".pak" file). Pure format test - core owns the
	// DeployCompile/ConvertPaks policy gates that decide whether such a
	// file actually enters the merge-convert pipeline.
	IsConvertibleArtifact(fileName string) bool

	// ClassifyMergeSource maps a retained-source identity - a fileID, an
	// imported archive's filename, or a Kind string previously recorded on
	// a merge fingerprint - to the source-defined kind string core
	// round-trips (MergeSource.Kind, fingerprint entries) and whether that
	// kind is a convertible raw artifact (subject to the ConvertPaks
	// opt-out and per-mod conversion outcomes) as opposed to the source's
	// native mergeable format. Must accept the empty string (legacy
	// fingerprints recorded no Kind) and classify it as the native kind.
	ClassifyMergeSource(id string) (kind string, convertible bool)

	// MergedArtifactName names the single merged output artifact core
	// deploys into the game's mod directory. The name is a deploy contract:
	// it must stay stable across merges (core stats/replaces it by name),
	// and any load-order significance it carries (Icarus: sorts last so it
	// wins) is entirely the source's concern.
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
// must be applied (profile load order).
type MergeSource struct {
	ModRef     string // "sourceID:modID" - machine identity (MergeFailure, ownership tracking)
	ModName    string // display name preferred over ModRef in user-facing warnings; may be empty
	SourcePath string // the retained source archive to read (native diff, or a convertible raw artifact - #221)
	Kind       string // source-defined kind from ClassifyMergeSource; empty means the source's native kind (#256)
}

// MergeFailure records one source that could not participate in a merge
// (#221: an irreconcilable pak). The merge itself still succeeds - the
// failed mod is skipped and falls back to raw deploy; core uses this list
// to reconcile cache manifests and record outcomes in the fingerprint.
type MergeFailure struct {
	ModRef string
	Reason string
}
