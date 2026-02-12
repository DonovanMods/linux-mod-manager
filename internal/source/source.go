package source

import (
	"context"
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
