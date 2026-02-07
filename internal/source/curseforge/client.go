package curseforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

const (
	defaultBaseURL = "https://api.curseforge.com"
)

// Client wraps the CurseForge REST API v1
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

// NewClient creates a new CurseForge API client
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

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, result interface{}) (err error) {
	reqURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: CurseForge API key required", domain.ErrAuthRequired)
	}

	if resp.StatusCode == http.StatusForbidden {
		// 403 can mean: no API key, invalid key, OR mod author disabled third-party distribution
		if c.apiKey == "" {
			return fmt.Errorf("%w: CurseForge API key required", domain.ErrAuthRequired)
		}
		// Read body to determine error type
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		bodyStr := string(body)
		// If it looks like an auth error (common CurseForge patterns)
		if strings.Contains(path, "/files/") && strings.Contains(path, "/download-url") {
			// This is a file download endpoint - 403 means distribution disabled
			return fmt.Errorf("mod author has disabled third-party downloads; visit CurseForge website to download manually")
		}
		// For other endpoints, 403 with valid key likely means invalid/expired key
		if bodyStr != "" {
			return fmt.Errorf("%w: access denied (check API key): %s", domain.ErrAuthRequired, bodyStr)
		}
		return fmt.Errorf("%w: access denied (check API key is valid)", domain.ErrAuthRequired)
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: resource not found", domain.ErrModNotFound)
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 10*1024)) // Limit error body to 10KB
		if readErr != nil {
			return fmt.Errorf("API error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

// GetGames fetches all available games with pagination
func (c *Client) GetGames(ctx context.Context) ([]Game, error) {
	const pageSize = 50

	var allGames []Game
	index := 0

	for {
		params := url.Values{}
		params.Set("pageSize", strconv.Itoa(pageSize))
		params.Set("index", strconv.Itoa(index))

		path := "/v1/games?" + params.Encode()

		var resp PaginatedResponse[[]Game]
		if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
			return nil, fmt.Errorf("getting games: %w", err)
		}

		allGames = append(allGames, resp.Data...)

		p := resp.Pagination
		if len(resp.Data) == 0 || p.Index+p.PageSize >= p.TotalCount {
			break
		}

		index += p.PageSize
	}

	return allGames, nil
}

// GetGame fetches a single game by ID
func (c *Client) GetGame(ctx context.Context, gameID int) (*Game, error) {
	path := fmt.Sprintf("/v1/games/%d", gameID)

	var resp APIResponse[Game]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, fmt.Errorf("getting game: %w", err)
	}
	return &resp.Data, nil
}

// SearchMods searches for mods with the given parameters
func (c *Client) SearchMods(ctx context.Context, gameID int, query string, categoryID int, pageSize, index int) ([]Mod, *Pagination, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50 // API max
	}

	params := url.Values{}
	params.Set("gameId", strconv.Itoa(gameID))
	if query != "" {
		params.Set("searchFilter", query)
	}
	if categoryID > 0 {
		params.Set("categoryId", strconv.Itoa(categoryID))
	}
	params.Set("pageSize", strconv.Itoa(pageSize))
	params.Set("index", strconv.Itoa(index))

	path := "/v1/mods/search?" + params.Encode()

	var resp PaginatedResponse[[]Mod]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, nil, fmt.Errorf("searching mods: %w", err)
	}

	return resp.Data, &resp.Pagination, nil
}

// GetMod fetches a single mod by ID
func (c *Client) GetMod(ctx context.Context, modID int) (*Mod, error) {
	path := fmt.Sprintf("/v1/mods/%d", modID)

	var resp APIResponse[Mod]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, fmt.Errorf("getting mod: %w", err)
	}
	return &resp.Data, nil
}

// GetMods fetches multiple mods by ID (batch request)
func (c *Client) GetMods(ctx context.Context, modIDs []int) ([]Mod, error) {
	if len(modIDs) == 0 {
		return nil, nil
	}

	// CurseForge expects POST with body for batch requests
	// For simplicity, we'll fetch one at a time for now
	// TODO: Implement batch POST /v1/mods
	var mods []Mod
	var errs []error

	for _, id := range modIDs {
		mod, err := c.GetMod(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("mod %d: %w", id, err))
			continue
		}
		mods = append(mods, *mod)
	}

	if len(errs) > 0 {
		return mods, errors.Join(errs...)
	}
	return mods, nil
}

// GetModFiles fetches files for a mod
func (c *Client) GetModFiles(ctx context.Context, modID int) ([]File, error) {
	path := fmt.Sprintf("/v1/mods/%d/files", modID)

	var resp PaginatedResponse[[]File]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}
	return resp.Data, nil
}

// GetModFile fetches a specific file for a mod
func (c *Client) GetModFile(ctx context.Context, modID, fileID int) (*File, error) {
	path := fmt.Sprintf("/v1/mods/%d/files/%d", modID, fileID)

	var resp APIResponse[File]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, fmt.Errorf("getting mod file: %w", err)
	}
	return &resp.Data, nil
}

// GetDownloadURL fetches the download URL for a mod file
func (c *Client) GetDownloadURL(ctx context.Context, modID, fileID int) (string, error) {
	path := fmt.Sprintf("/v1/mods/%d/files/%d/download-url", modID, fileID)

	var resp StringDownloadURL
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return "", fmt.Errorf("getting download URL: %w", err)
	}
	return resp.Data, nil
}

// GetCategories fetches categories for a game
func (c *Client) GetCategories(ctx context.Context, gameID int) ([]Category, error) {
	path := fmt.Sprintf("/v1/categories?gameId=%d", gameID)

	var resp APIResponse[[]Category]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return nil, fmt.Errorf("getting categories: %w", err)
	}
	return resp.Data, nil
}
