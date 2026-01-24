package nexusmods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"lmm/internal/domain"
)

const (
	defaultBaseURL = "https://api.nexusmods.com"
	oauthAuthorize = "https://www.nexusmods.com/oauth/authorize"
	oauthToken     = "https://www.nexusmods.com/oauth/token"
)

// Client wraps the NexusMods REST API v1
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

// NewClient creates a new NexusMods API client
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
	}
}

// SetAPIKey sets the API key for authentication
func (c *Client) SetAPIKey(key string) {
	c.apiKey = key
}

// IsAuthenticated returns true if an API key is configured
func (c *Client) IsAuthenticated() bool {
	return c.apiKey != ""
}

// ValidateAPIKey validates an API key by calling the NexusMods validate endpoint
func (c *Client) ValidateAPIKey(ctx context.Context, key string) error {
	url := c.baseURL + "/v1/users/validate.json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("apikey", key)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("invalid API key")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, result interface{}) error {
	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	if c.apiKey != "" {
		req.Header.Set("apikey", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: NexusMods API key required", domain.ErrAuthRequired)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

// GetMod fetches a mod by ID
func (c *Client) GetMod(ctx context.Context, gameDomain string, modID int) (*ModData, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d.json", gameDomain, modID)

	var mod ModData
	if err := c.doRequest(ctx, http.MethodGet, path, &mod); err != nil {
		return nil, fmt.Errorf("getting mod: %w", err)
	}

	return &mod, nil
}

// GetLatestAdded fetches the latest added mods for a game
func (c *Client) GetLatestAdded(ctx context.Context, gameDomain string) ([]ModData, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/latest_added.json", gameDomain)

	var mods []ModData
	if err := c.doRequest(ctx, http.MethodGet, path, &mods); err != nil {
		return nil, fmt.Errorf("getting latest added mods: %w", err)
	}

	return mods, nil
}

// GetLatestUpdated fetches the latest updated mods for a game
func (c *Client) GetLatestUpdated(ctx context.Context, gameDomain string) ([]ModData, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/latest_updated.json", gameDomain)

	var mods []ModData
	if err := c.doRequest(ctx, http.MethodGet, path, &mods); err != nil {
		return nil, fmt.Errorf("getting latest updated mods: %w", err)
	}

	return mods, nil
}

// GetTrending fetches the trending mods for a game
func (c *Client) GetTrending(ctx context.Context, gameDomain string) ([]ModData, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/trending.json", gameDomain)

	var mods []ModData
	if err := c.doRequest(ctx, http.MethodGet, path, &mods); err != nil {
		return nil, fmt.Errorf("getting trending mods: %w", err)
	}

	return mods, nil
}

// SearchMods searches for mods by fetching latest mods and filtering client-side.
// Note: NexusMods REST API v1 doesn't have a dedicated search endpoint,
// so we fetch recent mods and filter by name.
func (c *Client) SearchMods(ctx context.Context, gameDomain, query string, limit, offset int) ([]ModData, error) {
	// Fetch latest added mods
	mods, err := c.GetLatestAdded(ctx, gameDomain)
	if err != nil {
		return nil, fmt.Errorf("fetching mods for search: %w", err)
	}

	// Filter by query (case-insensitive substring match)
	query = strings.ToLower(query)
	var results []ModData
	for _, mod := range mods {
		if strings.Contains(strings.ToLower(mod.Name), query) {
			results = append(results, mod)
		}
	}

	// Apply pagination
	if offset >= len(results) {
		return []ModData{}, nil
	}
	results = results[offset:]
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// GetModFiles fetches files for a mod
func (c *Client) GetModFiles(ctx context.Context, gameDomain string, modID int) (*ModFileList, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d/files.json", gameDomain, modID)

	var files ModFileList
	if err := c.doRequest(ctx, http.MethodGet, path, &files); err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}

	return &files, nil
}

// GetDownloadLinks fetches download URLs for a mod file
func (c *Client) GetDownloadLinks(ctx context.Context, gameDomain string, modID, fileID int) ([]DownloadLink, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d/files/%d/download_link.json", gameDomain, modID, fileID)

	var links []DownloadLink
	if err := c.doRequest(ctx, http.MethodGet, path, &links); err != nil {
		return nil, fmt.Errorf("getting download links: %w", err)
	}

	return links, nil
}
