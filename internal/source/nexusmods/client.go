package nexusmods

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hasura/go-graphql-client"
)

const (
	graphqlEndpoint = "https://api.nexusmods.com/v2/graphql"
	oauthAuthorize  = "https://www.nexusmods.com/oauth/authorize"
	oauthToken      = "https://www.nexusmods.com/oauth/token"
)

// Client wraps the NexusMods GraphQL API
type Client struct {
	gql        *graphql.Client
	httpClient *http.Client
	apiKey     string
}

// NewClient creates a new NexusMods API client
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// Create transport that adds API key header
	transport := &apiKeyTransport{
		base:   httpClient.Transport,
		apiKey: apiKey,
	}
	authedClient := &http.Client{Transport: transport}

	return &Client{
		gql:        graphql.NewClient(graphqlEndpoint, authedClient),
		httpClient: httpClient,
		apiKey:     apiKey,
	}
}

type apiKeyTransport struct {
	base   http.RoundTripper
	apiKey string
}

func (t *apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiKey != "" {
		req.Header.Set("apikey", t.apiKey)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// GetMod fetches a mod by ID
func (c *Client) GetMod(ctx context.Context, gameID string, modID int) (*ModData, error) {
	var query struct {
		Mod ModData `graphql:"mod(gameId: $gameId, modId: $modId)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"modId":  graphql.Int(modID),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("querying mod: %w", err)
	}

	return &query.Mod, nil
}

// SearchMods searches for mods
func (c *Client) SearchMods(ctx context.Context, gameID, search string, limit, offset int) ([]ModData, error) {
	var query struct {
		Mods struct {
			Nodes []ModData `graphql:"nodes"`
		} `graphql:"mods(gameId: $gameId, filter: {name: $name}, first: $first, offset: $offset)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"name":   graphql.String(search),
		"first":  graphql.Int(limit),
		"offset": graphql.Int(offset),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("searching mods: %w", err)
	}

	return query.Mods.Nodes, nil
}

// GetModFiles fetches files for a mod
func (c *Client) GetModFiles(ctx context.Context, gameID string, modID int) ([]FileData, error) {
	var query struct {
		ModFiles struct {
			Nodes []FileData `graphql:"nodes"`
		} `graphql:"modFiles(modId: $modId, gameId: $gameId)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"modId":  graphql.Int(modID),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("querying mod files: %w", err)
	}

	return query.ModFiles.Nodes, nil
}
