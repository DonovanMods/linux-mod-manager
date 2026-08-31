package nexusmods

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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

// EnvKey implements source.EnvKeyProvider: the legacy environment variable
// name, preserved exactly.
func (n *NexusMods) EnvKey() string {
	return "NEXUSMODS_API_KEY"
}

// ValidateKey implements source.KeyValidator by checking key against the
// NexusMods validate endpoint, independent of any key already configured on
// this source.
func (n *NexusMods) ValidateKey(ctx context.Context, key string) error {
	return n.client.ValidateAPIKey(ctx, key)
}

// AuthInstructions implements source.AuthInstructionsProvider.
func (n *NexusMods) AuthInstructions() string {
	return "To authenticate with NexusMods:\n" +
		"1. Visit https://www.nexusmods.com/users/myaccount?tab=api\n" +
		"2. Click \"Request an API Key\" if you don't have one\n" +
		"3. Copy your Personal API Key\n"
}

// TypeLabel implements source.TypeLabeler.
func (n *NexusMods) TypeLabel() string {
	return "built-in"
}

// Capabilities implements source.CapabilityReporter. NexusMods supports all
// ModSource operations.
func (n *NexusMods) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}
}

// ExchangeToken exchanges an OAuth code for tokens.
// NexusMods uses API key authentication instead of OAuth.
// Use SetAPIKey() or the NEXUSMODS_API_KEY environment variable.
func (n *NexusMods) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("NexusMods uses API key authentication, not OAuth")
}

// Search finds mods matching the query
func (n *NexusMods) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	offset := query.Page * pageSize

	results, err := n.client.SearchMods(ctx, query.GameID, query.Query, query.Category, query.Tags, pageSize, offset)
	if err != nil {
		return source.SearchResult{}, err
	}

	mods := make([]domain.Mod, len(results))
	for i, r := range results {
		mods[i] = modDataToDomain(r, query.GameID)
	}

	return source.SearchResult{Mods: mods, TotalCount: 0, Page: query.Page, PageSize: pageSize}, nil
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

// GetDependencies returns mod dependencies from NexusMods
func (n *NexusMods) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	requirements, err := n.client.GetModRequirements(ctx, mod.GameID, modID)
	if err != nil {
		return nil, fmt.Errorf("fetching requirements: %w", err)
	}

	refs := make([]domain.ModReference, len(requirements))
	for i, req := range requirements {
		refs[i] = domain.ModReference{
			SourceID: "nexusmods",
			ModID:    strconv.Itoa(req.ModID),
		}
	}

	return refs, nil
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
			FileName:    sanitizeFileName(f.FileName),
			Version:     f.Version,
			Size:        size,
			IsPrimary:   f.IsPrimary,
			Category:    f.CategoryName,
			Description: f.Description,
		}
	}

	return files, nil
}

// Changelog implements source.ChangelogProvider (#87): the REST files
// endpoint's FileData.Changelog (types.go's changelog_html field) is the
// same data CheckUpdatesWithProgress already reads, exposed here by mod
// version. Selection: an exact version match wins, checked across every
// file regardless of IsPrimary; otherwise the PRIMARY file's changelog, if
// it has one. A non-primary file's changelog is never used as a fallback,
// even when it's the only file that has one - narrower than
// CheckUpdatesWithProgress's own fallback (which accepts any file's
// non-empty changelog when no primary file has one), and deliberately so:
// this method exists to answer "what changed in this specific version",
// where a non-matching non-primary file's changelog is more likely to
// mislead than help. Returns "" when nothing matches - not an error, since
// "nothing to show" is ordinary and only a failed lookup is a real error.
//
// The returned text is always plain: stripChangelogHTML removes markup and
// decodes entities before this returns, so ModDetail.Changelog never
// carries raw changelog_html - unlike Mod.Description, which stays raw HTML
// all the way to `--json` by existing, accepted precedent (#86). Changelog
// is a new field with no such precedent to honor, so a future HTML renderer
// (e.g. `lmm serve`) never has to sanitize it itself.
func (n *NexusMods) Changelog(ctx context.Context, sourceGameID, modID, version string) (string, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return "", fmt.Errorf("invalid mod ID: %w", err)
	}

	fileList, err := n.client.GetModFiles(ctx, sourceGameID, id)
	if err != nil {
		return "", fmt.Errorf("getting mod files: %w", err)
	}

	primary := ""
	for _, f := range fileList.Files {
		if f.Changelog == "" {
			continue
		}
		if version != "" && f.Version == version {
			return stripChangelogHTML(f.Changelog), nil
		}
		if f.IsPrimary && primary == "" {
			primary = f.Changelog
		}
	}
	return stripChangelogHTML(primary), nil
}

// changelogBreakRE/changelogTagRE back stripChangelogHTML. Duplicated from
// internal/core's CleanChangelog (rather than imported) because
// internal/source sits below internal/core in the project's layering
// (cmd/lmm -> core -> source -> domain, per the repo CLAUDE.md) - source has
// no business depending on the layer that depends on it.
var (
	changelogBreakRE = regexp.MustCompile(`(?i)<br\s*/?>|</p>|<p[^>]*>`)
	changelogTagRE   = regexp.MustCompile(`<[^>]*>`)
)

// stripChangelogHTML converts a NexusMods changelog_html value to plain
// text: <br> and <p>/</p> become newlines, every other tag is removed
// outright, and the five HTML entities a changelog typically contains are
// decoded. Mirrors internal/core.CleanChangelog's behavior exactly, so
// double-cleaning downstream (cmd/lmm's plain-text render already calls
// CleanChangelog on every ModDetail.Changelog) is a harmless no-op.
func stripChangelogHTML(html string) string {
	html = changelogBreakRE.ReplaceAllString(html, "\n")
	html = changelogTagRE.ReplaceAllString(html, "")
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	return strings.TrimSpace(html)
}

func sanitizeFileName(name string) string {
	const fallbackFileName = "download"

	name = strings.TrimSpace(name)
	if name == "" {
		return fallbackFileName
	}

	safe := path.Base(strings.ReplaceAll(name, "\\", "/"))
	if safe == "." || safe == ".." || safe == "/" || safe == "" {
		return fallbackFileName
	}

	return safe
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

// CheckUpdates checks for available updates by comparing installed mod version and
// installed file IDs against NexusMods (mod version and FileUpdates). Each file has its
// own version; a mod is considered to have an update if the mod version is newer or if
// any installed file ID has been superseded by a new file (NexusMods FileUpdates).
// Returns partial updates plus a joined error when one or more mods fail to fetch.
func (n *NexusMods) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return n.CheckUpdatesWithProgress(ctx, installed, nil)
}

// CheckUpdatesWithProgress is CheckUpdates plus a per-mod progress callback
// (source.UpdateProgressReporter); report is called with a 1-based index
// before each mod's remote lookup. report may be nil.
func (n *NexusMods) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	var updates []domain.Update
	var fetchErrs []error

	for i, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}

		if report != nil {
			report(i+1, len(installed), inst.Name)
		}

		remoteMod, err := n.GetMod(ctx, inst.GameID, inst.ID)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Errorf("%s (id %s): %w", inst.Name, inst.ID, err))
			continue
		}

		modID, err := strconv.Atoi(inst.ID)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Errorf("%s (id %s): invalid mod ID: %w", inst.Name, inst.ID, err))
			continue
		}

		fileList, err := n.client.GetModFiles(ctx, inst.GameID, modID)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Errorf("%s (id %s): %w", inst.Name, inst.ID, err))
			continue
		}

		// Build map: old file ID -> new file ID from NexusMods FileUpdates (superseded files)
		oldToNew := make(map[string]string)
		for _, fu := range fileList.FileUpdates {
			oldToNew[strconv.Itoa(fu.OldFileID)] = strconv.Itoa(fu.NewFileID)
		}

		// New version file ID -> FileData for picking new version string and changelog
		newFileIDs := make(map[string]FileData)
		for _, f := range fileList.Files {
			newFileIDs[strconv.Itoa(f.FileID)] = f
		}

		// Consider update if mod version is newer OR any installed file was superseded
		modVersionNewer := domain.IsNewerVersion(inst.Version, remoteMod.Version)
		var fileReplacements map[string]string
		for _, fid := range inst.FileIDs {
			if newID, ok := oldToNew[fid]; ok {
				if fileReplacements == nil {
					fileReplacements = make(map[string]string)
				}
				fileReplacements[fid] = newID
			}
		}
		hasFileUpdate := len(fileReplacements) > 0

		if !modVersionNewer && !hasFileUpdate {
			continue
		}

		// Pick NewVersion: prefer mod version when it changed; else use new file's version
		newVersion := remoteMod.Version
		if hasFileUpdate && !modVersionNewer {
			for _, newID := range fileReplacements {
				if f, ok := newFileIDs[newID]; ok && f.Version != "" {
					newVersion = f.Version
					break
				}
			}
		}

		changelog := ""
		for _, f := range fileList.Files {
			if f.IsPrimary && f.Changelog != "" {
				changelog = f.Changelog
				break
			}
			if changelog == "" && f.Changelog != "" {
				changelog = f.Changelog
			}
		}

		updates = append(updates, domain.Update{
			InstalledMod:       inst,
			NewVersion:         newVersion,
			Changelog:          changelog,
			FileIDReplacements: fileReplacements,
		})
	}

	if len(fetchErrs) > 0 {
		return updates, fmt.Errorf("update check skipped %d mod(s): %w", len(fetchErrs), errors.Join(fetchErrs...))
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
		Endorsements: int64Ptr(int64(data.EndorsementCount)),
		PictureURL:   data.PictureURL,
		SourceURL:    fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", data.DomainName, data.ModID),
		UpdatedAt:    data.UpdatedTime,
	}
}

// int64Ptr returns a pointer to the given int64 value.
func int64Ptr(v int64) *int64 { return &v }
