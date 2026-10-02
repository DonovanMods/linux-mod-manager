package curseforge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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

// EnvKey implements source.EnvKeyProvider: the legacy environment variable
// name, preserved exactly.
func (c *CurseForge) EnvKey() string {
	return "CURSEFORGE_API_KEY"
}

// ValidateKey implements source.KeyValidator by probing the CurseForge API
// with key via GetGames, discarding the result. Uses a client scoped to this
// call (same underlying HTTP client and base URL as c.client) so validation
// is independent of any key already configured on this source.
func (c *CurseForge) ValidateKey(ctx context.Context, key string) error {
	client := NewClient(c.client.httpClient, key)
	client.SetBaseURL(c.client.rest.BaseURL())
	if _, err := client.GetGames(ctx); err != nil {
		return fmt.Errorf("API validation failed: %w", err)
	}
	return nil
}

// AuthInstructions implements source.AuthInstructionsProvider.
func (c *CurseForge) AuthInstructions() string {
	return "To authenticate with CurseForge:\n" +
		"1. Visit https://console.curseforge.com/\n" +
		"2. Create a project and generate an API key\n" +
		"3. Copy your API key\n"
}

// ListGames implements source.GameCatalog by wrapping the CurseForge games
// listing, mapping the numeric game ID to its string form.
func (c *CurseForge) ListGames(ctx context.Context) ([]source.GameEntry, error) {
	games, err := c.client.GetGames(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing games: %w", err)
	}

	entries := make([]source.GameEntry, len(games))
	for i, g := range games {
		entries[i] = source.GameEntry{ID: strconv.Itoa(g.ID), Name: g.Name, Slug: g.Slug}
	}
	return entries, nil
}

// TypeLabel implements source.TypeLabeler.
func (c *CurseForge) TypeLabel() string {
	return "built-in"
}

// Capabilities implements source.CapabilityReporter. CurseForge supports all
// ModSource operations, and its hits carry UpdatedAt, Downloads and
// Endorsements (thumbsUpCount), so every sort is meaningful.
func (c *CurseForge) Capabilities() source.Capabilities {
	return source.Capabilities{
		Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true,
		Sorts: []domain.SearchSort{domain.SortUpdated, domain.SortDownloads, domain.SortPopular},
	}
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
func (c *CurseForge) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	gameID, err := c.resolveGameID(ctx, query.GameID)
	if err != nil {
		return source.SearchResult{}, err
	}

	// The EFFECTIVE page size, not the requested one: the client clamps
	// anything over CurseForge's own maximum, and an index computed from a
	// size the API refused strides past every row in between - page 1 of a
	// requested 100 would ask for index 100 while page 0 returned rows
	// 0-49 (Track C review, finding 1). Clamping here keeps this source's
	// own offsets contiguous and lets it report the size really in effect,
	// which is what a caller paging it has to page on.
	pageSize := clampSearchPageSize(query.PageSize)
	index := query.Page * pageSize

	// Parse category if provided
	var categoryID int
	if query.Category != "" {
		categoryID, err = strconv.Atoi(query.Category)
		if err != nil {
			return source.SearchResult{}, fmt.Errorf("invalid category ID (expected numeric): %w", err)
		}
	}

	results, pagination, err := c.client.SearchMods(ctx, gameID, query.Query, categoryID, pageSize, index, query.Sort)
	if err != nil {
		return source.SearchResult{}, err
	}

	mods := make([]domain.Mod, len(results))
	for i, r := range results {
		mods[i] = modToDomain(r, query.GameID)
	}

	// Relevance only: prioritize name matches over description/tag matches.
	// Any other sort was applied by the API itself, and re-sorting the page
	// here would undo it.
	if _, sorted := searchSortField(query.Sort); !sorted {
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
	}

	if query.Page == 0 {
		exact, err := c.exactNameHit(ctx, gameID, query, mods)
		if err != nil {
			return source.SearchResult{}, err
		}
		if exact != nil {
			mods = append([]domain.Mod{*exact}, mods...)
		}
	}

	return source.SearchResult{Mods: mods, TotalCount: pagination.TotalCount, Page: query.Page, PageSize: pageSize}, nil
}

// exactNameHit finds the mod whose name IS the query when the fetched first
// page does not already hold one (#503): CurseForge's default ordering can
// bury a mod's own exact name below page 1, and core puts exact-name hits
// first, so the page has to contain it. It spends one slug request, only on a
// non-empty query with a usable slug, and keeps the hit only when its own
// name passes the exact-name rule, so a slug collision cannot promote an
// unrelated mod. The lookup is best-effort: any failure yields no hit and no
// error, except a cancelled or expired context, which is returned.
func (c *CurseForge) exactNameHit(ctx context.Context, gameID int, query source.SearchQuery, page []domain.Mod) (*domain.Mod, error) {
	if query.Query == "" || slices.ContainsFunc(page, func(m domain.Mod) bool { return nameMatchesQuery(m.Name, query.Query) }) {
		return nil, nil
	}
	slug := slugForQuery(query.Query)
	if slug == "" {
		return nil, nil
	}

	hit, err := c.client.SearchModBySlug(ctx, gameID, slug)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || hit == nil || !nameMatchesQuery(hit.Name, query.Query) {
		return nil, nil
	}
	if slices.ContainsFunc(page, func(m domain.Mod) bool { return m.ID == strconv.Itoa(hit.ID) }) {
		return nil, nil
	}
	mod := modToDomain(*hit, query.GameID)
	return &mod, nil
}

// nameMatchesQuery is the exact-name rule core orders search results by
// (source packages cannot import core, so it is restated here): equal
// ignoring case and surrounding space, or equal after folding both to their
// lowercase letters and digits. An empty query matches nothing.
func nameMatchesQuery(name, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(name), q) {
		return true
	}
	fq := foldAlnum(q)
	return fq != "" && fq == foldAlnum(name)
}

// foldAlnum lowercases s and drops everything but letters and digits.
func foldAlnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// slugForQuery is the CurseForge slug a mod named query would have:
// lowercase, each run of non-alphanumerics a single '-', no leading or
// trailing '-'. It is empty when the query has no letters or digits.
func slugForQuery(query string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(query) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		} else {
			pendingDash = true
		}
	}
	return b.String()
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

// Description implements source.DescriptionFetcher (#246): CurseForge's mod
// document has no full-description field - only a Summary, which #235
// stopped aliasing into Description - so the real one comes from its own
// endpoint. Returns the source's raw HTML, which is what Mod.Description
// has always carried (the terminal display path runs it through
// core.CleanChangelog; `mod show --json` keeps the markup).
//
// sourceGameID is unused: the endpoint is keyed on the mod alone.
func (c *CurseForge) Description(ctx context.Context, _, modID string) (string, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return "", fmt.Errorf("invalid mod ID: %w", err)
	}
	return c.client.GetModDescription(ctx, id)
}

// GetModFiles returns the available download files for a mod
func (c *CurseForge) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	modID, err := strconv.Atoi(mod.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	// The whole list, not the API's first page: the update check advertises
	// files by id (some only latestFilesIndexes names), and applying one - or
	// installing an older version - resolves that id in this list.
	fileList, err := c.client.GetAllModFiles(ctx, modID)
	if err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}

	files := make([]domain.DownloadableFile, len(fileList))
	for i, f := range fileList {
		files[i] = domain.DownloadableFile{
			ID:          strconv.Itoa(f.ID),
			Name:        f.DisplayName,
			FileName:    f.FileName,
			Version:     fileVersion(f),
			Size:        f.FileLength,
			IsPrimary:   i == 0, // First file is typically the latest/main
			Category:    releaseTypeName(f.ReleaseType),
			Description: "", // CurseForge doesn't have per-file descriptions
			UploadedAt:  f.FileDate,
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
	return c.CheckUpdatesWithProgress(ctx, installed, nil)
}

// CheckUpdatesWithProgress is CheckUpdates plus a per-mod progress callback
// (source.UpdateProgressReporter); report is called once per installed mod,
// with a 1-based index, in the order given. report may be nil.
//
// Since #28 the whole batch is fetched in ONE round trip per chunk of 50
// (Client.GetMods' POST /v1/mods) instead of one GET per mod, so the
// progress tick now fires as each mod's result is COMPARED rather than
// before its own request - one call per mod, same order, same arguments.
//
// A mod whose id is not a number, and one the API omits from its batch
// answer, are both skipped rather than fatal: the check reports the ones it
// could make and names the ones it could not, as before.
func (c *CurseForge) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	var updates []domain.Update
	// skipped holds ONE reason per mod that could not be checked - never
	// two for the same mod, and never the whole batch error repeated per
	// mod (Track C review, finding 8).
	var skipped []error

	ids := make([]int, 0, len(installed))
	unparseable := make(map[string]bool)
	for _, inst := range installed {
		id, err := strconv.Atoi(inst.ID)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("%s (id %s): invalid mod ID: %w", inst.Name, inst.ID, err))
			unparseable[inst.ID] = true
			continue
		}
		ids = append(ids, id)
	}

	// GetMods' own error already names every id it could not resolve; it is
	// kept only to explain a mod's absence below, never to abort the check.
	fetched, fetchErr := c.client.GetMods(ctx, ids)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	byID := make(map[string]Mod, len(fetched))
	for _, m := range fetched {
		byID[strconv.Itoa(m.ID)] = m
	}

	for i, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}

		if report != nil {
			report(i+1, len(installed), inst.Name)
		}

		if unparseable[inst.ID] {
			continue // already reported above; it was never in the batch
		}

		data, ok := byID[inst.ID]
		if !ok {
			// Its own reason, not the batch's: fetchErr names every id the
			// whole call could not resolve, so stapling it here once per
			// absent mod turned one failed chunk of 50 into 50 copies of a
			// 50-id string. The batch error is attached once, below.
			skipped = append(skipped, fmt.Errorf("%s (id %s): %w", inst.Name, inst.ID, domain.ErrModNotFound))
			continue
		}

		latest, err := c.latestUpdate(ctx, data, inst)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return updates, cerr
			}
			skipped = append(skipped, fmt.Errorf("%s (id %s): %w", inst.Name, inst.ID, err))
			continue
		}
		if latest == nil {
			continue
		}

		upd := domain.Update{
			InstalledMod: inst,
			NewVersion:   fileVersion(*latest),
			Changelog:    "", // CurseForge changelog requires separate fetch
		}
		if ids := installedFileIDs(inst.FileIDs); len(ids) > 0 {
			// Name the file the check advertised: ApplyUpdate installs the
			// listed file with this id rather than re-deriving "the latest"
			// from a version label, which can match another flavor's file.
			upd.FileIDReplacements = map[string]string{
				strconv.Itoa(slices.Max(ids)): strconv.Itoa(latest.ID),
			}
		}
		updates = append(updates, upd)
	}

	if len(skipped) > 0 {
		errs := skipped
		if fetchErr != nil {
			// Why the ids are missing - a chunk whose REQUEST failed reads
			// as "not found" per mod above, which is true but not the
			// reason. Attached once, and outside the count, which counts
			// mods.
			errs = append(slices.Clone(skipped), fmt.Errorf("batch fetch: %w", fetchErr))
		}
		return updates, fmt.Errorf("update check skipped %d mod(s): %w", len(skipped), errors.Join(errs...))
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

	// The version shown is the newest file's, not latestFiles' first entry:
	// that list is the newest file per game flavor in no promised order (#504).
	version := ""
	if newest := newestFile(installableFiles(data.LatestFiles)); newest != nil {
		version = fileVersion(*newest)
	}

	return domain.Mod{
		ID:       strconv.Itoa(data.ID),
		SourceID: "curseforge",
		Name:     data.Name,
		Version:  version,
		Author:   author,
		Summary:  data.Summary,
		// Description deliberately left empty: the mod response has no full
		// description, and copying Summary made every surface showing both
		// render the same text twice (#235).
		GameID:       gameID,
		Category:     category,
		Downloads:    data.DownloadCount,
		SourceURL:    data.Links.WebsiteURL,
		Endorsements: int64Ptr(int64(data.ThumbsUpCount)),
		PictureURL:   pictureURL,
		UpdatedAt:    data.DateModified,
	}
}

// versionRegex matches semantic version patterns like 1.2.3, v1.2.3, 1.2.3-beta, etc.
// The optional suffix must start with a letter (to avoid matching 1.20.1-15.3.0 as one version).
var versionRegex = regexp.MustCompile(`[vV]?(\d+\.\d+(?:\.\d+)?(?:\.\d+)?(?:[-+][a-zA-Z][\w.]*)?)`)

// maxBareVersionLen bounds a bare version label: a real one ("339-1-g23f0261")
// is short, and anything much longer is a title that merely starts with a digit.
const maxBareVersionLen = 32

// fileExtensions are the archive extensions stripped from a file name before
// it is read for a version.
var fileExtensions = []string{".jar", ".zip", ".7z", ".rar"}

// stripFileExtension removes one known archive extension from name.
func stripFileExtension(name string) string {
	for _, ext := range fileExtensions {
		if trimmed, ok := strings.CutSuffix(name, ext); ok {
			return trimmed
		}
	}
	return name
}

// bareVersionLabel returns s as a version when it is nothing but a version
// label: one token (no whitespace) that starts with a digit, or a v/V then a
// digit, of reasonable length. A leading v/V is dropped, as the dotted
// extraction does. Otherwise "".
func bareVersionLabel(s string) string {
	if s == "" || len(s) > maxBareVersionLen || strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	label := strings.TrimLeft(s, "vV")
	if len(s)-len(label) > 1 || label == "" || label[0] < '0' || label[0] > '9' {
		return ""
	}
	return label
}

// fileNameVersionLabel reads a bare version label out of a file name:
// "Auctionator-339-1-g23f0261.zip" is "339-1-g23f0261". Everything before the
// first digit must be a prefix ending in a '-' or '_' (the mod's name), so a
// digit inside a word ("Mod2-Pro") is not taken for a version.
func fileNameVersionLabel(fileName string) string {
	base := stripFileExtension(fileName)
	i := strings.IndexAny(base, "0123456789")
	if i < 0 {
		return ""
	}
	// A 'v' right before the digit belongs to the label ("Thing-v12").
	if i > 0 && (base[i-1] == 'v' || base[i-1] == 'V') {
		i--
	}
	if i > 0 && base[i-1] != '-' && base[i-1] != '_' {
		return ""
	}
	return bareVersionLabel(base[i:])
}

// extractVersion attempts to extract a version string from a display name or filename.
//
// A dotted version wins: it returns the last version-like pattern found in the
// display name, else in the file name (the mod version typically comes after
// the game version in "jei-1.20.1-15.3.0.4"). A file whose version is not
// dotted - a build number ("339") or a git-describe label ("339-1-g23f0261") -
// has none, so then the display name itself is the version when it is a bare
// label, and otherwise the label the file name carries after the mod's name
// (#510). A prose display name ("Auctionator for Classic") is never a version.
func extractVersion(displayName, fileName string) string {
	for _, s := range []string{displayName, fileName} {
		if s == "" {
			continue
		}
		base := stripFileExtension(s)

		// Find all version matches and take the last one.
		matches := versionRegex.FindAllStringSubmatch(base, -1)
		if len(matches) > 0 {
			return matches[len(matches)-1][1]
		}
	}
	if v := bareVersionLabel(displayName); v != "" {
		return v
	}
	return fileNameVersionLabel(fileName)
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

// int64Ptr returns a pointer to the given int64 value.
func int64Ptr(v int64) *int64 { return &v }
