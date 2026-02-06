package curseforge

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// CurseForge implements the ModSource interface
type CurseForge struct {
	client *Client
	// gameIDCache caches resolved game slugs to numeric IDs
	gameIDCache map[string]int
	cacheMu     sync.RWMutex
}

// New creates a new CurseForge source
func New(httpClient *http.Client, apiKey string) *CurseForge {
	return &CurseForge{
		client:      NewClient(httpClient, apiKey),
		gameIDCache: make(map[string]int),
	}
}

// ID returns the source identifier
func (c *CurseForge) ID() string {
	return "curseforge"
}

// Name returns the display name
func (c *CurseForge) Name() string {
	return "CurseForge"
}

// AuthURL returns the authentication URL.
// CurseForge uses API key authentication obtained from console.curseforge.com.
func (c *CurseForge) AuthURL() string {
	return "https://console.curseforge.com/"
}

// SetAPIKey sets the API key for authentication
func (c *CurseForge) SetAPIKey(key string) {
	c.client.SetAPIKey(key)
}

// IsAuthenticated returns true if an API key is configured
func (c *CurseForge) IsAuthenticated() bool {
	return c.client.IsAuthenticated()
}

// ExchangeToken exchanges an OAuth code for tokens.
// CurseForge uses API key authentication instead of OAuth.
func (c *CurseForge) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("CurseForge uses API key authentication, not OAuth")
}

// resolveGameID converts a game identifier (numeric ID or slug) to a numeric ID.
// Results are cached to avoid repeated API calls.
func (c *CurseForge) resolveGameID(ctx context.Context, gameIDOrSlug string) (int, error) {
	// Try parsing as numeric ID first
	if id, err := strconv.Atoi(gameIDOrSlug); err == nil {
		return id, nil
	}

	// Check cache for slug
	c.cacheMu.RLock()
	if id, ok := c.gameIDCache[gameIDOrSlug]; ok {
		c.cacheMu.RUnlock()
		return id, nil
	}
	c.cacheMu.RUnlock()

	// Fetch games from API and find by slug
	games, err := c.client.GetGames(ctx)
	if err != nil {
		return 0, fmt.Errorf("fetching games to resolve slug %q: %w", gameIDOrSlug, err)
	}

	slugLower := strings.ToLower(gameIDOrSlug)
	for _, g := range games {
		if strings.ToLower(g.Slug) == slugLower || strings.ToLower(g.Name) == slugLower {
			// Cache the result
			c.cacheMu.Lock()
			c.gameIDCache[gameIDOrSlug] = g.ID
			c.cacheMu.Unlock()
			return g.ID, nil
		}
	}

	return 0, fmt.Errorf("game not found: %q (tried as numeric ID and slug)", gameIDOrSlug)
}

// Search finds mods matching the query.
// gameID can be either a numeric CurseForge game ID (e.g., "432") or a slug (e.g., "minecraft").
func (c *CurseForge) Search(ctx context.Context, query source.SearchQuery) ([]domain.Mod, error) {
	gameID, err := c.resolveGameID(ctx, query.GameID)
	if err != nil {
		return nil, err
	}

	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	index := query.Page * pageSize

	// Parse category if provided
	var categoryID int
	if query.Category != "" {
		categoryID, err = strconv.Atoi(query.Category)
		if err != nil {
			return nil, fmt.Errorf("invalid category ID (expected numeric): %w", err)
		}
	}

	results, _, err := c.client.SearchMods(ctx, gameID, query.Query, categoryID, pageSize, index)
	if err != nil {
		return nil, err
	}

	mods := make([]domain.Mod, len(results))
	for i, r := range results {
		mods[i] = modToDomain(r, query.GameID)
	}

	// Sort results: prioritize name matches over description/tag matches
	queryLower := strings.ToLower(query.Query)
	sort.SliceStable(mods, func(i, j int) bool {
		iNameMatch := strings.Contains(strings.ToLower(mods[i].Name), queryLower)
		jNameMatch := strings.Contains(strings.ToLower(mods[j].Name), queryLower)
		if iNameMatch && !jNameMatch {
			return true
		}
		if !iNameMatch && jNameMatch {
			return false
		}
		return mods[i].Downloads > mods[j].Downloads
	})

	return mods, nil
}

// GetMod retrieves a specific mod
func (c *CurseForge) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	data, err := c.client.GetMod(ctx, id)
	if err != nil {
		return nil, err
	}

	mod := modToDomain(*data, gameID)
	return &mod, nil
}

// GetDependencies returns mod dependencies from CurseForge.
// Dependencies are extracted from the latest file's dependency list.
func (c *CurseForge) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	files, err := c.client.GetModFiles(ctx, modID)
	if err != nil {
		return nil, fmt.Errorf("fetching files: %w", err)
	}

	if len(files) == 0 {
		return nil, nil
	}

	// Use the first (latest) file's dependencies
	var refs []domain.ModReference
	for _, dep := range files[0].Dependencies {
		// Only include required dependencies
		if dep.RelationType == RelationRequiredDependency {
			refs = append(refs, domain.ModReference{
				SourceID: "curseforge",
				ModID:    strconv.Itoa(dep.ModID),
			})
		}
	}

	return refs, nil
}

// GetModFiles returns the available download files for a mod
func (c *CurseForge) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	fileList, err := c.client.GetModFiles(ctx, modID)
	if err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}

	files := make([]domain.DownloadableFile, len(fileList))
	for i, f := range fileList {
		files[i] = domain.DownloadableFile{
			ID:          strconv.Itoa(f.ID),
			Name:        f.DisplayName,
			FileName:    f.FileName,
			Version:     extractVersion(f.DisplayName, f.FileName),
			Size:        f.FileLength,
			IsPrimary:   i == 0, // First file is typically the latest/main
			Category:    releaseTypeName(f.ReleaseType),
			Description: "", // CurseForge doesn't have per-file descriptions
		}
	}

	return files, nil
}

// GetDownloadURL gets the download URL for a mod file
func (c *CurseForge) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return "", fmt.Errorf("invalid mod ID: %w", err)
	}

	fID, err := strconv.Atoi(fileID)
	if err != nil {
		return "", fmt.Errorf("invalid file ID: %w", err)
	}

	url, err := c.client.GetDownloadURL(ctx, modID, fID)
	if err != nil {
		return "", fmt.Errorf("getting download URL: %w", err)
	}

	return url, nil
}

// CheckUpdates checks for available updates.
func (c *CurseForge) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	var fetchErrs []error

	for i, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}

		if fn, ok := ctx.Value(domain.UpdateProgressContextKey).(domain.UpdateProgressFunc); ok && fn != nil {
			fn(i+1, len(installed), inst.Name)
		}

		remoteMod, err := c.GetMod(ctx, inst.GameID, inst.ID)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Errorf("%s (id %s): %w", inst.Name, inst.ID, err))
			continue
		}

		// Compare versions
		if !isNewerVersion(inst.Version, remoteMod.Version) {
			continue
		}

		updates = append(updates, domain.Update{
			InstalledMod: inst,
			NewVersion:   remoteMod.Version,
			Changelog:    "", // CurseForge changelog requires separate fetch
		})
	}

	if len(fetchErrs) > 0 {
		return updates, fmt.Errorf("update check skipped %d mod(s): %w", len(fetchErrs), joinErrors(fetchErrs))
	}
	return updates, nil
}

// modToDomain converts a CurseForge Mod to domain.Mod
func modToDomain(data Mod, gameID string) domain.Mod {
	var author string
	if len(data.Authors) > 0 {
		author = data.Authors[0].Name
	}

	var pictureURL string
	if data.Logo != nil {
		pictureURL = data.Logo.ThumbnailURL
	}

	var category string
	if data.PrimaryCategoryID > 0 {
		category = strconv.Itoa(data.PrimaryCategoryID)
	}

	// Extract version from latest file if available
	version := ""
	if len(data.LatestFiles) > 0 {
		version = extractVersion(data.LatestFiles[0].DisplayName, data.LatestFiles[0].FileName)
	}

	return domain.Mod{
		ID:           strconv.Itoa(data.ID),
		SourceID:     "curseforge",
		Name:         data.Name,
		Version:      version,
		Author:       author,
		Summary:      data.Summary,
		Description:  data.Summary, // CurseForge API doesn't include full description in mod response
		GameID:       gameID,
		Category:     category,
		Downloads:    data.DownloadCount,
		Endorsements: int64(data.ThumbsUpCount),
		PictureURL:   pictureURL,
		UpdatedAt:    data.DateModified,
	}
}

// versionRegex matches semantic version patterns like 1.2.3, v1.2.3, 1.2.3-beta, etc.
// The optional suffix must start with a letter (to avoid matching 1.20.1-15.3.0 as one version).
var versionRegex = regexp.MustCompile(`[vV]?(\d+\.\d+(?:\.\d+)?(?:\.\d+)?(?:[-+][a-zA-Z][\w.]*)?)`)

// extractVersion attempts to extract a version string from a display name or filename
// Returns the last version-like pattern found (mod version typically comes after MC version)
func extractVersion(displayName, fileName string) string {
	// Try to extract version from displayName first, then fileName
	for _, s := range []string{displayName, fileName} {
		if s == "" {
			continue
		}
		// Strip file extension for cleaner matching
		base := strings.TrimSuffix(s, ".jar")
		base = strings.TrimSuffix(base, ".zip")
		base = strings.TrimSuffix(base, ".7z")
		base = strings.TrimSuffix(base, ".rar")

		// Find all version matches and take the last one
		// (mod version typically comes after MC version in filenames like "jei-1.20.1-15.3.0.4")
		matches := versionRegex.FindAllStringSubmatch(base, -1)
		if len(matches) > 0 {
			return matches[len(matches)-1][1]
		}
	}
	return "" // No version found
}

// releaseTypeName converts a release type code to a name
func releaseTypeName(releaseType int) string {
	switch releaseType {
	case ReleaseTypeRelease:
		return "Release"
	case ReleaseTypeBeta:
		return "Beta"
	case ReleaseTypeAlpha:
		return "Alpha"
	default:
		return "Unknown"
	}
}

// isNewerVersion returns true if newVersion is newer than currentVersion
// Simple string comparison - CurseForge versions are often file names
func isNewerVersion(currentVersion, newVersion string) bool {
	return currentVersion != newVersion && newVersion != ""
}

// joinErrors joins multiple errors into one
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	// Go 1.20+ has errors.Join, but let's be compatible
	msg := errs[0].Error()
	for _, e := range errs[1:] {
		msg += "; " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
