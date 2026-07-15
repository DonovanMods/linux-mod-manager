package custom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// apiRequestTimeout bounds every request to a declarative REST source.
const apiRequestTimeout = 30 * time.Second

// maxAPIResponseSize bounds how much of an API response we read into memory
// (same defense class as maxManifestSize).
const maxAPIResponseSize = 10 << 20 // 10 MiB

// API is a ModSource backed by a declaratively-described GET+JSON REST API
// (design §4). Endpoints that the definition omits surface as ErrNotSupported
// capability gaps rather than errors at load time.
type API struct {
	id        string
	name      string
	baseURL   string
	pageStart int
	auth      *AuthConfig
	endpoints APIEndpoints
	mappings  APIMappings

	apiKey     string
	httpClient *http.Client
}

// NewAPI constructs an api source from a validated definition. It performs
// no I/O — a valid definition always registers; request problems surface as
// operation errors.
func NewAPI(def SourceDefinition) (*API, error) {
	cfg := def.API
	pageStart := 1
	if cfg.PageStart != nil {
		pageStart = *cfg.PageStart
	}
	return &API{
		id:         def.ID,
		name:       def.Name,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		pageStart:  pageStart,
		auth:       cfg.Auth,
		endpoints:  cfg.Endpoints,
		mappings:   cfg.Mappings,
		httpClient: &http.Client{Timeout: apiRequestTimeout},
	}, nil
}

// ID implements source.ModSource.
func (a *API) ID() string { return a.id }

// Name implements source.ModSource.
func (a *API) Name() string { return a.name }

// AuthURL implements source.ModSource; api sources use API keys, not OAuth.
func (a *API) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (a *API) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", a.id, source.ErrNotSupported)
}

// GetDependencies implements source.ModSource; always unsupported in v1
// (design §4).
func (a *API) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", a.id, source.ErrNotSupported)
}

// SetAPIKey provides the API key resolved at startup (env var or token store).
func (a *API) SetAPIKey(key string) { a.apiKey = key }

// IsAuthenticated reports whether an API key is configured.
func (a *API) IsAuthenticated() bool { return a.apiKey != "" }

// Capabilities implements source.CapabilityReporter: an undefined endpoint is
// an unsupported capability (design §4/§7).
func (a *API) Capabilities() source.Capabilities {
	return source.Capabilities{
		Search:       a.endpoints.Search != nil,
		Dependencies: false,
		Updates:      a.endpoints.GetMod != nil,
		Auth:         a.auth != nil,
	}
}

// DownloadHeaders implements source.DownloadHeaderProvider: header-mode keys
// go only to downloads on the API's own origin (design §9).
func (a *API) DownloadHeaders(fileURL string) map[string]string {
	if a.auth == nil || a.auth.APIKey.In != "header" || a.apiKey == "" {
		return nil
	}
	if !sameOriginURLs(fileURL, a.baseURL) {
		return nil
	}
	return map[string]string{a.auth.APIKey.Name: a.apiKey}
}

// buildEndpointURL substitutes {placeholder} tokens in an endpoint path
// template with URL-escaped values. Placeholders without a value are left
// intact (they will typically 404 loudly rather than silently matching).
func buildEndpointURL(pathTemplate string, vals map[string]string) string {
	out := pathTemplate
	for name, value := range vals {
		out = strings.ReplaceAll(out, "{"+name+"}", url.QueryEscape(value))
	}
	return out
}

// getJSON performs an authenticated GET against rawURL and decodes the JSON
// response. 401 maps to domain.ErrAuthRequired; other non-200s surface the
// status. Errors never contain the request URL's query string (keys ride
// there in query mode) — the inner *url.Error is unwrapped, mirroring the
// manifest fetcher's redaction.
func (a *API) getJSON(ctx context.Context, rawURL string) (any, error) {
	reqURL := rawURL
	if a.auth != nil && a.auth.APIKey.In == "query" && a.apiKey != "" {
		u, err := addQueryParam(reqURL, a.auth.APIKey.Name, a.apiKey)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", a.id, err)
		}
		reqURL = u
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source %q: building request: %w", a.id, err)
	}
	if a.auth != nil && a.auth.APIKey.In == "header" && a.apiKey != "" {
		req.Header.Set(a.auth.APIKey.Name, a.apiKey)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // strip the URL (and any query-mode key) from the message
		}
		return nil, fmt.Errorf("source %q: requesting %s: %w", a.id, redactedURL(rawURL), err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("source %q: %w", a.id, domain.ErrAuthRequired)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %q: requesting %s: HTTP %d", a.id, redactedURL(rawURL), resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("source %q: reading response: %w", a.id, err)
	}
	if len(data) > maxAPIResponseSize {
		return nil, fmt.Errorf("source %q: response from %s exceeds %d bytes", a.id, redactedURL(rawURL), maxAPIResponseSize)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("source %q: parsing response from %s: %w", a.id, redactedURL(rawURL), err)
	}
	return doc, nil
}

// redactedURL strips the query string from a URL for error messages.
func redactedURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparseable URL)"
	}
	u.RawQuery = ""
	return u.String()
}

var (
	_ source.ModSource              = (*API)(nil)
	_ source.CapabilityReporter     = (*API)(nil)
	_ source.DownloadHeaderProvider = (*API)(nil)
)

// Search is implemented in the search task (replaces this stub).
func (a *API) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", a.id, source.ErrNotSupported)
}

// GetMod is implemented in the read-ops task (replaces this stub).
func (a *API) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, fmt.Errorf("source %q: fetching mod: %w", a.id, source.ErrNotSupported)
}

// GetModFiles is implemented in the read-ops task (replaces this stub).
func (a *API) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, fmt.Errorf("source %q: listing files: %w", a.id, source.ErrNotSupported)
}

// GetDownloadURL is implemented in the read-ops task (replaces this stub).
func (a *API) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", fmt.Errorf("source %q: download URL: %w", a.id, source.ErrNotSupported)
}

// CheckUpdates is implemented in the update-check task (replaces this stub).
func (a *API) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, fmt.Errorf("source %q: update checks: %w", a.id, source.ErrNotSupported)
}
