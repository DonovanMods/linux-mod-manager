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
}

// CapabilityReporter is implemented by sources that support only a subset of
// ModSource operations. Sources that do not implement it are assumed fully
// capable.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// CapabilitiesOf returns src's capabilities, assuming full capability for
// sources that do not implement CapabilityReporter (all built-in sources).
func CapabilitiesOf(src ModSource) Capabilities {
	if cr, ok := src.(CapabilityReporter); ok {
		return cr.Capabilities()
	}
	return Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true}
}
