package nexusmods

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

// GetModFiles returns the available download files for a mod
func (n *NexusMods) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	fileList, err := n.client.GetModFiles(ctx, mod.GameID, modID)
	if err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}

	files := make([]domain.DownloadableFile, len(fileList.Files))
	for i, f := range fileList.Files {
		size := f.Size
		if f.SizeInBytes != nil {
			size = *f.SizeInBytes
		}

		files[i] = domain.DownloadableFile{
			ID:          strconv.Itoa(f.FileID),
			Name:        f.Name,
			FileName:    f.FileName,
			Version:     f.Version,
			Size:        size,
			IsPrimary:   f.IsPrimary,
			Category:    f.CategoryName,
			Description: f.Description,
		}
	}

	return files, nil
}

// GetDownloadURL gets the download URL for a mod file
func (n *NexusMods) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return "", fmt.Errorf("invalid mod ID: %w", err)
	}

	fID, err := strconv.Atoi(fileID)
	if err != nil {
		return "", fmt.Errorf("invalid file ID: %w", err)
	}

	links, err := n.client.GetDownloadLinks(ctx, mod.GameID, modID, fID)
	if err != nil {
		return "", fmt.Errorf("getting download links: %w", err)
	}

	if len(links) == 0 {
		return "", fmt.Errorf("no download links available")
	}

	// Return the first available CDN URL
	return links[0].URI, nil
}

// CheckUpdates checks for available updates by comparing installed versions against NexusMods
func (n *NexusMods) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update

	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}

		// Fetch current mod info from NexusMods
		remoteMod, err := n.GetMod(ctx, inst.GameID, inst.ID)
		if err != nil {
			// Skip mods that can't be fetched (deleted, private, etc.)
			continue
		}

		// Compare versions
		if isNewerVersion(inst.Version, remoteMod.Version) {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   remoteMod.Version,
				Changelog:    "", // Could fetch from mod files if needed
			})
		}
	}

	return updates, nil
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

// isNewerVersion returns true if newVersion is newer than currentVersion
func isNewerVersion(currentVersion, newVersion string) bool {
	return compareVersions(currentVersion, newVersion) < 0
}

// compareVersions compares two version strings
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func compareVersions(v1, v2 string) int {
	parts1 := parseVersion(v1)
	parts2 := parseVersion(v2)

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int
		if i < len(parts1) {
			p1 = parts1[i]
		}
		if i < len(parts2) {
			p2 = parts2[i]
		}

		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}

// parseVersion splits a version string into numeric parts
func parseVersion(v string) []int {
	// Remove common prefixes
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		// Extract numeric portion (handle things like "1.0.0-beta")
		numStr := ""
		for _, c := range part {
			if c >= '0' && c <= '9' {
				numStr += string(c)
			} else {
				break
			}
		}

		if numStr == "" {
			result = append(result, 0)
		} else {
			n, _ := strconv.Atoi(numStr)
			result = append(result, n)
		}
	}

	return result
}
