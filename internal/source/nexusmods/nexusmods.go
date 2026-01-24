package nexusmods

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"lmm/internal/domain"
	"lmm/internal/source"
)

// NexusMods implements the ModSource interface
type NexusMods struct {
	client *Client
}

// New creates a new NexusMods source
func New(httpClient *http.Client, apiKey string) *NexusMods {
	return &NexusMods{
		client: NewClient(httpClient, apiKey),
	}
}

// ID returns the source identifier
func (n *NexusMods) ID() string {
	return "nexusmods"
}

// Name returns the display name
func (n *NexusMods) Name() string {
	return "Nexus Mods"
}

// AuthURL returns the OAuth authorization URL
func (n *NexusMods) AuthURL() string {
	return oauthAuthorize
}

// SetAPIKey sets the API key for authentication
func (n *NexusMods) SetAPIKey(key string) {
	n.client.SetAPIKey(key)
}

// IsAuthenticated returns true if an API key is configured
func (n *NexusMods) IsAuthenticated() bool {
	return n.client.IsAuthenticated()
}

// ValidateAPIKey validates an API key with the NexusMods API
func (n *NexusMods) ValidateAPIKey(ctx context.Context, key string) error {
	return n.client.ValidateAPIKey(ctx, key)
}

// ExchangeToken exchanges an OAuth code for tokens
func (n *NexusMods) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	// TODO: Implement OAuth token exchange
	return nil, fmt.Errorf("OAuth not yet implemented")
}

// Search finds mods matching the query
func (n *NexusMods) Search(ctx context.Context, query source.SearchQuery) ([]domain.Mod, error) {
	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	offset := query.Page * pageSize

	results, err := n.client.SearchMods(ctx, query.GameID, query.Query, pageSize, offset)
	if err != nil {
		return nil, err
	}

	mods := make([]domain.Mod, len(results))
	for i, r := range results {
		mods[i] = modDataToDomain(r, query.GameID)
	}

	return mods, nil
}

// GetMod retrieves a specific mod
func (n *NexusMods) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	data, err := n.client.GetMod(ctx, gameID, id)
	if err != nil {
		return nil, err
	}

	mod := modDataToDomain(*data, gameID)
	return &mod, nil
}

// GetDependencies returns mod dependencies
func (n *NexusMods) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	// TODO: Implement dependency fetching from NexusMods
	return nil, nil
}

// GetDownloadURL gets the download URL for a mod file
func (n *NexusMods) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	// TODO: Implement download URL generation
	return "", fmt.Errorf("download URLs not yet implemented")
}

// CheckUpdates checks for available updates
func (n *NexusMods) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	// TODO: Implement update checking
	return nil, nil
}

func modDataToDomain(data ModData, gameID string) domain.Mod {
	return domain.Mod{
		ID:           strconv.Itoa(data.ModID),
		SourceID:     "nexusmods",
		Name:         data.Name,
		Version:      data.Version,
		Author:       data.Author,
		Summary:      data.Summary,
		Description:  data.Description,
		GameID:       gameID,
		Category:     strconv.Itoa(data.CategoryID),
		Endorsements: int64(data.EndorsementCount),
		UpdatedAt:    data.UpdatedTime,
	}
}
