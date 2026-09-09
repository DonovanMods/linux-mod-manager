package curseforge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

const (
	defaultBaseURL = "https://api.curseforge.com"

	// maxResponseSize bounds how much of any successful CurseForge response
	// is read into memory. The largest thing this client asks for is a
	// modBatchSize-sized batch of mod documents or a mod's full HTML
	// description (#246); 10 MiB is orders of magnitude above either and
	// matches the cap the custom api source already applies to its own
	// responses.
	maxResponseSize = 10 << 20
)

// Client wraps the CurseForge REST API v1
type Client struct {
	httpClient *http.Client
	rest       *httpclient.Client
	apiKey     string
}

// NewClient creates a new CurseForge API client
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	c := &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
	}
	c.rest = httpclient.New(httpclient.Options{
		HTTPClient:       httpClient,
		BaseURL:          defaultBaseURL,
		APIKey:           apiKey,
		AuthHeader:       "x-api-key",
		AuthLabel:        "CurseForge",
		ErrorMapper:      c.mapError,
		MaxResponseBytes: maxResponseSize,
	})
	return c
}

// SetAPIKey sets the API key for authentication
func (c *Client) SetAPIKey(key string) {
	c.apiKey = key
	c.rest.SetAPIKey(key)
}

// SetBaseURL overrides the REST API base URL — primarily used by tests that
// front the client with an httptest server.
func (c *Client) SetBaseURL(u string) {
	c.rest.SetBaseURL(u)
}

// IsAuthenticated returns true if an API key is configured
func (c *Client) IsAuthenticated() bool {
	return c.apiKey != ""
}

// mapError translates CurseForge-specific status codes (403 disambiguation,
// 404 -> ErrModNotFound) before the shared client falls back to its default
// 401 / generic mapping. Returning nil defers to the default.
func (c *Client) mapError(status int, body []byte, path string) error {
	switch status {
	case http.StatusForbidden:
		if c.apiKey == "" {
			return fmt.Errorf("%w: CurseForge API key required", domain.ErrAuthRequired)
		}
		// File-download endpoints answer 403 when the mod author has opted out
		// of third-party distribution; everything else is treated as auth.
		if strings.Contains(path, "/files/") && strings.Contains(path, "/download-url") {
			return fmt.Errorf("mod author has disabled third-party downloads; visit CurseForge website to download manually")
		}
		if len(body) > 0 {
			return fmt.Errorf("%w: access denied (check API key): %s", domain.ErrAuthRequired, string(body))
		}
		return fmt.Errorf("%w: access denied (check API key is valid)", domain.ErrAuthRequired)
	case http.StatusNotFound:
		return fmt.Errorf("%w: resource not found", domain.ErrModNotFound)
	}
	return nil
}

// doRequest performs an authenticated REST request and JSON-decodes the response.
// Thin wrapper around httpclient.Client.DoJSON, kept for callsite stability.
func (c *Client) doRequest(ctx context.Context, method, path string, result interface{}) error {
	return c.rest.DoJSON(ctx, method, path, result)
}

// doRequestWithBody is doRequest for the endpoints that send a JSON body
// (the batch POST /v1/mods, #28). Thin wrapper around
// httpclient.Client.DoJSONBody, so auth, size caps and error mapping are
// the same ones every other CurseForge call goes through.
func (c *Client) doRequestWithBody(ctx context.Context, method, path string, body, result interface{}) error {
	return c.rest.DoJSONBody(ctx, method, path, body, result)
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

// modBatchSize is how many mod IDs GetMods puts in one POST /v1/mods body.
// CurseForge documents no hard ceiling for the batch endpoint, so this is a
// deliberately conservative chunk: 50 matches the page size the paginated
// endpoints (GetGames, SearchMods) already cap at, keeps a single request
// and its response comfortably small, and still turns a 200-mod update
// check into 4 round trips instead of 200.
const modBatchSize = 50

// GetMods fetches multiple mods by ID through the batch endpoint
// (POST /v1/mods with a {"modIds": [...]} body), in chunks of modBatchSize
// (#28). One request per chunk, not one per id.
//
// Two partial-failure shapes are both non-fatal to the mods that DID come
// back, and both surface as a joined error alongside them:
//
//   - a chunk whose request fails (transport error, non-2xx): the other
//     chunks are still attempted;
//   - an id the API simply OMITS from its response, which is how CurseForge
//     answers for an unknown, delisted or unavailable mod: reported per id
//     as domain.ErrModNotFound, so a caller can errors.Is it exactly as it
//     could when this was a per-id fan-out over GetMod's own 404 mapping.
func (c *Client) GetMods(ctx context.Context, modIDs []int) ([]Mod, error) {
	if len(modIDs) == 0 {
		return nil, nil
	}

	var mods []Mod
	var errs []error

	for chunk := range slices.Chunk(modIDs, modBatchSize) {
		body := struct {
			ModIDs []int `json:"modIds"`
		}{ModIDs: chunk}

		var resp APIResponse[[]Mod]
		if err := c.doRequestWithBody(ctx, http.MethodPost, "/v1/mods", body, &resp); err != nil {
			errs = append(errs, fmt.Errorf("mods %v: %w", chunk, err))
			continue
		}

		returned := make(map[int]bool, len(resp.Data))
		for _, m := range resp.Data {
			returned[m.ID] = true
		}
		for _, id := range chunk {
			if !returned[id] {
				errs = append(errs, fmt.Errorf("mod %d: %w", id, domain.ErrModNotFound))
			}
		}
		mods = append(mods, resp.Data...)
	}

	if len(errs) > 0 {
		return mods, errors.Join(errs...)
	}
	return mods, nil
}

// GetModDescription fetches a mod's full description as the source's own
// HTML (GET /v1/mods/{modId}/description, #246). The mod document itself
// carries only a Summary, so this is a second round trip and is made only
// where exactly one mod is being shown.
func (c *Client) GetModDescription(ctx context.Context, modID int) (string, error) {
	path := fmt.Sprintf("/v1/mods/%d/description", modID)

	var resp APIResponse[string]
	if err := c.doRequest(ctx, http.MethodGet, path, &resp); err != nil {
		return "", fmt.Errorf("getting mod description: %w", err)
	}
	return resp.Data, nil
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
