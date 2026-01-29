package nexusmods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

const (
	defaultBaseURL    = "https://api.nexusmods.com"
	defaultGraphQLURL = "https://api.nexusmods.com/v2/graphql"
	oauthAuthorize    = "https://www.nexusmods.com/oauth/authorize"
	oauthToken        = "https://www.nexusmods.com/oauth/token"
)

// Client wraps the NexusMods REST API v1 and GraphQL v2 APIs
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	graphqlURL string
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
		graphqlURL: defaultGraphQLURL,
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
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("API error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
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
		body, readErr := io.ReadAll(resp.Body)
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

// graphqlSearchQuery is the GraphQL query for searching mods
const graphqlSearchQuery = `
query SearchMods($filter: ModsFilter, $count: Int, $offset: Int) {
  mods(filter: $filter, count: $count, offset: $offset) {
    nodes {
      modId
      name
      summary
      version
      uploader { name }
    }
  }
}`

// graphqlRequirementsQuery is the GraphQL query for mod dependencies
const graphqlRequirementsQuery = `
query ModRequirements($modId: Int!, $gameDomainName: String!) {
  modRequirements(modId: $modId, gameDomainName: $gameDomainName) {
    nexusRequirements {
      nodes {
        modId
        modName
      }
    }
  }
}`

// graphqlRequest represents a GraphQL request payload
type graphqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

// graphqlModsResponse represents the GraphQL response for mods search
type graphqlModsResponse struct {
	Data struct {
		Mods struct {
			Nodes []struct {
				ModID    int    `json:"modId"`
				Name     string `json:"name"`
				Summary  string `json:"summary"`
				Version  string `json:"version"`
				Uploader struct {
					Name string `json:"name"`
				} `json:"uploader"`
			} `json:"nodes"`
		} `json:"mods"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// graphqlRequirementsResponse represents the GraphQL response for mod requirements
type graphqlRequirementsResponse struct {
	Data struct {
		ModRequirements struct {
			NexusRequirements struct {
				Nodes []struct {
					ModID   int    `json:"modId"`
					ModName string `json:"modName"`
				} `json:"nodes"`
			} `json:"nexusRequirements"`
		} `json:"modRequirements"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// SearchMods searches for mods using the NexusMods GraphQL v2 API.
// category and tags are optional filters (source-specific; NexusMods may support categoryId and tag names).
func (c *Client) SearchMods(ctx context.Context, gameDomain, query, category string, tags []string, limit, offset int) ([]ModData, error) {
	if limit <= 0 {
		limit = 20
	}

	filter := map[string]interface{}{
		"gameDomainName": []map[string]interface{}{
			{"value": gameDomain, "op": "EQUALS"},
		},
		"name": []map[string]interface{}{
			{"value": query, "op": "WILDCARD"},
		},
	}
	if category != "" {
		filter["categoryId"] = []map[string]interface{}{
			{"value": category, "op": "EQUALS"},
		}
	}
	if len(tags) > 0 {
		// NexusMods GraphQL may support tag filter; pass first tag for now
		filter["tagNames"] = []map[string]interface{}{
			{"value": tags[0], "op": "EQUALS"},
		}
	}

	reqBody := graphqlRequest{
		Query: graphqlSearchQuery,
		Variables: map[string]interface{}{
			"filter": filter,
			"count":  limit,
			"offset": offset,
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("apikey", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("GraphQL error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("GraphQL error (status %d): %s", resp.StatusCode, string(body))
	}

	var gqlResp graphqlModsResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		var msgs []string
		for _, e := range gqlResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	}

	// Convert GraphQL response to ModData
	results := make([]ModData, len(gqlResp.Data.Mods.Nodes))
	for i, node := range gqlResp.Data.Mods.Nodes {
		results[i] = ModData{
			ModID:   node.ModID,
			Name:    node.Name,
			Summary: node.Summary,
			Version: node.Version,
			Author:  node.Uploader.Name,
		}
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

// ModRequirement represents a dependency returned from the GraphQL API
type ModRequirement struct {
	ModID   int
	ModName string
}

// GetModRequirements fetches mod dependencies using the GraphQL API
func (c *Client) GetModRequirements(ctx context.Context, gameDomain string, modID int) ([]ModRequirement, error) {
	reqBody := graphqlRequest{
		Query: graphqlRequirementsQuery,
		Variables: map[string]interface{}{
			"modId":          modID,
			"gameDomainName": gameDomain,
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("apikey", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("GraphQL error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("GraphQL error (status %d): %s", resp.StatusCode, string(body))
	}

	var gqlResp graphqlRequirementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		var msgs []string
		for _, e := range gqlResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	}

	// Convert to ModRequirement slice
	nodes := gqlResp.Data.ModRequirements.NexusRequirements.Nodes
	requirements := make([]ModRequirement, len(nodes))
	for i, node := range nodes {
		requirements[i] = ModRequirement{
			ModID:   node.ModID,
			ModName: node.ModName,
		}
	}

	return requirements, nil
}
