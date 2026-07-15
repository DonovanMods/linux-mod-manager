package custom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// defaultManifestRefresh is the remote-manifest cache TTL when the definition
// does not set manifest.refresh.
const defaultManifestRefresh = 15 * time.Minute

// maxManifestSize bounds how much of a remote manifest we read into memory.
// Real manifests are KBs; 10 MiB is generous and prevents a hostile or broken
// server from exhausting memory.
const maxManifestSize = 10 << 20 // 10 MiB

// Manifest is a ModSource backed by a published mod-list document (design §3).
// Remote manifests are fetched on demand and cached in memory for the
// configured TTL; local paths are read on every operation (cheap).
// Construction is pure: a valid definition always registers, and fetch/parse
// problems surface as operation errors naming the manifest URL.
type Manifest struct {
	id        string
	name      string
	url       string // https URL, or absolute local path (~ expanded)
	isRemote  bool
	refresh   time.Duration
	allowHTTP bool
	auth      *AuthConfig

	apiKey     string
	httpClient *http.Client
	now        func() time.Time // injectable for TTL tests

	mu        sync.Mutex
	cached    *manifestDoc
	fetchedAt time.Time
}

// NewManifest constructs a manifest source from a validated definition. It
// performs no I/O — the manifest is first fetched when an operation needs it.
func NewManifest(def SourceDefinition) (*Manifest, error) {
	cfg := def.Manifest

	refresh := defaultManifestRefresh
	if cfg.Refresh != "" {
		d, err := time.ParseDuration(cfg.Refresh)
		if err != nil {
			return nil, fmt.Errorf("manifest.refresh: %w", err) // unreachable after Validate, kept for safety
		}
		refresh = d
	}

	u := cfg.URL
	isRemote := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !isRemote {
		if strings.HasPrefix(u, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("expanding %q: %w", u, err)
			}
			u = filepath.Join(home, u[2:])
		}
		abs, err := filepath.Abs(u)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", u, err)
		}
		u = abs
	}

	return &Manifest{
		id:         def.ID,
		name:       def.Name,
		url:        u,
		isRemote:   isRemote,
		refresh:    refresh,
		allowHTTP:  def.AllowHTTP,
		auth:       cfg.Auth,
		httpClient: http.DefaultClient,
		now:        time.Now,
	}, nil
}

// ID returns the source ID.
func (m *Manifest) ID() string { return m.id }

// Name returns the source name.
func (m *Manifest) Name() string { return m.name }

// SetAPIKey provides the API key resolved at startup (env var or token store).
func (m *Manifest) SetAPIKey(key string) { m.apiKey = key }

// IsAuthenticated reports whether an API key is configured. Only meaningful
// when the definition declares auth (Capabilities().Auth).
func (m *Manifest) IsAuthenticated() bool { return m.apiKey != "" }

// fetch returns the parsed manifest, honoring the TTL cache for remote URLs.
// Errors name the source and manifest URL so users can act on them.
func (m *Manifest) fetch(ctx context.Context) (*manifestDoc, error) {
	if !m.isRemote {
		data, err := os.ReadFile(m.url)
		if err != nil {
			return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
		}
		doc, err := parseManifest(data, m.allowHTTP)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		return doc, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cached != nil && m.now().Sub(m.fetchedAt) < m.refresh {
		return m.cached, nil
	}

	doc, err := m.fetchRemote(ctx)
	if err != nil {
		return nil, err
	}
	m.cached = doc
	m.fetchedAt = m.now()
	return doc, nil
}

// fetchRemote downloads and parses the manifest document. Callers hold m.mu.
func (m *Manifest) fetchRemote(ctx context.Context) (*manifestDoc, error) {
	reqURL := m.url
	if m.auth != nil && m.auth.APIKey.In == "query" && m.apiKey != "" {
		u, err := addQueryParam(reqURL, m.auth.APIKey.Name, m.apiKey)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		reqURL = u
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	if m.auth != nil && m.auth.APIKey.In == "header" && m.apiKey != "" {
		req.Header.Set(m.auth.APIKey.Name, m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		// *url.Error's Error() embeds the request URL verbatim, which for
		// query-mode auth contains the API key. Unwrap to the transport
		// error before reporting so the key never reaches the message; the
		// (unauthenticated) m.url is still named via the format string.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("source %q: fetching manifest %s: %w", m.id, m.url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %q: fetching manifest %s: HTTP %d", m.id, m.url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("source %q: manifest %s exceeds %d bytes", m.id, m.url, maxManifestSize)
	}

	doc, err := parseManifest(data, m.allowHTTP)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	return doc, nil
}

// addQueryParam returns rawURL with name=value appended to its query string,
// preserving existing parameters. Values are URL-escaped.
func addQueryParam(rawURL, name, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	q := u.Query()
	q.Set(name, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// AuthURL implements source.ModSource; manifest sources use API keys, not OAuth.
func (m *Manifest) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (m *Manifest) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", m.id, source.ErrNotSupported)
}

// Capabilities implements source.CapabilityReporter. Auth reflects whether the
// definition declares an auth block.
func (m *Manifest) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: m.auth != nil}
}

// toMod converts a manifest entry to a domain.Mod. GameID is stamped by
// searchMods / the callers, not here.
func (m *Manifest) toMod(mm manifestMod) domain.Mod {
	mod := domain.Mod{
		ID:          mm.ID,
		SourceID:    m.id,
		Name:        mm.Name,
		Version:     mm.Version,
		Author:      mm.Author,
		Summary:     mm.Summary,
		Description: mm.Summary,
		SourceURL:   mm.URL,
	}
	if mm.UpdatedAt != "" {
		if ts, err := time.Parse(time.RFC3339, mm.UpdatedAt); err == nil {
			mod.UpdatedAt = ts // unparseable -> zero value, by design
		}
	}
	for _, dep := range mm.Dependencies {
		mod.Dependencies = append(mod.Dependencies, domain.ModReference{SourceID: m.id, ModID: dep})
	}
	return mod
}

// gameMatches reports whether a manifest entry applies to gameID: an empty
// game_ids list matches every game, and an empty gameID matches every entry.
func gameMatches(mm manifestMod, gameID string) bool {
	if len(mm.GameIDs) == 0 || gameID == "" {
		return true
	}
	for _, g := range mm.GameIDs {
		if g == gameID {
			return true
		}
	}
	return false
}

// Search implements source.ModSource with the shared client-side semantics
// (design §5), filtered by the manifest's per-mod game_ids.
func (m *Manifest) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return source.SearchResult{}, err
	}
	mods := make([]domain.Mod, 0, len(doc.Mods))
	for _, mm := range doc.Mods {
		if !gameMatches(mm, query.GameID) {
			continue
		}
		mods = append(mods, m.toMod(mm))
	}
	return searchMods(mods, query), nil
}

// GetMod implements source.ModSource. gameID does not filter (install-by-ID
// works from any game); it is echoed onto the returned mod for attribution.
func (m *Manifest) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	mm, err := m.findMod(ctx, modID)
	if err != nil {
		return nil, err
	}
	mod := m.toMod(*mm)
	mod.GameID = gameID
	return &mod, nil
}

// GetModFiles implements source.ModSource, mapping manifest file entries —
// including declared sha256 checksums — onto DownloadableFiles.
func (m *Manifest) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return nil, err
	}
	files := make([]domain.DownloadableFile, 0, len(mm.Files))
	for _, f := range mm.Files {
		files = append(files, domain.DownloadableFile{
			ID:        f.ID,
			Name:      f.Name,
			FileName:  f.Filename,
			Version:   f.Version,
			Size:      f.Size,
			IsPrimary: f.Primary,
			SHA256:    f.SHA256,
		})
	}
	return files, nil
}

// GetDownloadURL implements source.ModSource. Query-mode auth is appended
// here; header-mode auth rides via DownloadHeaders (see DownloadHeaderProvider).
func (m *Manifest) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return "", err
	}
	for _, f := range mm.Files {
		if f.ID != fileID {
			continue
		}
		u := f.URL
		if m.auth != nil && m.auth.APIKey.In == "query" && m.apiKey != "" {
			withKey, err := addQueryParam(u, m.auth.APIKey.Name, m.apiKey)
			if err != nil {
				return "", fmt.Errorf("source %q: file %q: %w", m.id, fileID, err)
			}
			u = withKey
		}
		return u, nil
	}
	return "", fmt.Errorf("source %q: mod %q: file not found: %s", m.id, mod.ID, fileID)
}

// findMod fetches the manifest and returns the entry with the given ID.
func (m *Manifest) findMod(ctx context.Context, modID string) (*manifestMod, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return nil, err
	}
	for i := range doc.Mods {
		if doc.Mods[i].ID == modID {
			return &doc.Mods[i], nil
		}
	}
	return nil, fmt.Errorf("source %q: mod not found: %s", m.id, modID)
}

// GetDependencies implements source.ModSource: manifest dependencies are IDs
// within this source, returned as ModReferences for the resolver.
func (m *Manifest) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]domain.ModReference, 0, len(mm.Dependencies))
	for _, dep := range mm.Dependencies {
		refs = append(refs, domain.ModReference{SourceID: m.id, ModID: dep})
	}
	return refs, nil
}

// CheckUpdates implements source.ModSource by comparing installed versions to
// the current manifest.
func (m *Manifest) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]manifestMod, len(doc.Mods))
	for _, mm := range doc.Mods {
		byID[mm.ID] = mm
	}

	var updates []domain.Update
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, ok := byID[inst.ID]
		if !ok {
			continue // mod removed from the manifest; nothing to offer
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   current.Version,
			})
		}
	}
	return updates, nil
}

// DownloadHeaders implements source.DownloadHeaderProvider: header-mode auth
// applies the same key to file downloads as to manifest fetches (design §6).
func (m *Manifest) DownloadHeaders() map[string]string {
	if m.auth == nil || m.auth.APIKey.In != "header" || m.apiKey == "" {
		return nil
	}
	return map[string]string{m.auth.APIKey.Name: m.apiKey}
}

var _ source.ModSource = (*Manifest)(nil)
var _ source.CapabilityReporter = (*Manifest)(nil)
var _ source.DownloadHeaderProvider = (*Manifest)(nil)
