package source

import (
	"context"
	"errors"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
	// Versions: GetModFiles carries per-file Version strings usable for
	// exact version->file resolution (#96)
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
