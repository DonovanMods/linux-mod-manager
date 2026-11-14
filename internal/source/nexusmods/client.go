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
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

const (
	defaultBaseURL    = "https://api.nexusmods.com"
	defaultGraphQLURL = "https://api.nexusmods.com/v2/graphql"
	oauthAuthorize    = "https://www.nexusmods.com/oauth/authorize"
	oauthToken        = "https://www.nexusmods.com/oauth/token"
)

// Client wraps the NexusMods REST API v1 and GraphQL v2 APIs.
// REST traffic flows through httpclient.Client; GraphQL traffic uses the
// underlying *http.Client directly because its envelope and error shape
// differ from the REST endpoints.
type Client struct {
	httpClient *http.Client
	rest       *httpclient.Client
	graphqlURL string

	// apiKey is read and written only under keyMu (#601): lmm serve
	// re-keys a live source (SetAPIKey) while requests are in flight.
	keyMu  sync.RWMutex
	apiKey string

	// knownGames is the game domains the games endpoint has confirmed, so a
	// 404 from a mod endpoint is checked against its game once (#540).
	knownGamesMu sync.Mutex
	knownGames   map[string]bool

	// categories caches each game domain's category id -> name list
	// (#592), read from the same games endpoint gameExists asks: one fetch
	// per domain for the client's lifetime. categoryMisses holds when a
	// domain's fetch last failed, so a failing endpoint is not asked again
	// on every GetMod of an update check (see categoryRetryAfter).
	categories     map[string]map[int]string
	categoryMisses map[string]time.Time
	now            func() time.Time

	// tier caches AccountTier's answer for tierKey, the key it was asked
	// with (#601), until tierAt+tierTTL (#607: a plan gates on it now, and
	// an account can be upgraded or lapse under a long-running `lmm
	// serve`). A changed key asks again, and so does a download the API
	// refuses as Premium-only (forgetTier). A failed check is never cached.
	tierMu  sync.Mutex
	tierKey string
	tier    source.Tier
	tierAt  time.Time
}

// tierTTL is how long an account tier is trusted before it is asked again.
const tierTTL = 15 * time.Minute

// clock is the client's time source (now, for tests), else time.Now.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// forgetTier drops the cached account tier: the API just refused a download
// as Premium-only, which a cached Premium answer contradicts.
func (c *Client) forgetTier() {
	c.tierMu.Lock()
	defer c.tierMu.Unlock()
	c.tier, c.tierKey, c.tierAt = "", "", time.Time{}
}

// categoryRetryAfter is how long a game domain whose category list could not
// be fetched is left alone before it is asked again: long enough that an
// update check over hundreds of mods costs one failed request, short enough
// that a long-running `lmm serve` recovers from a transient outage.
const categoryRetryAfter = 10 * time.Minute

// gameResponse is the part of GET /v1/games/{domain}.json lmm reads: the
// game's category list. parent_category is false or an id, so it is not
// decoded.
type gameResponse struct {
	Categories []struct {
		CategoryID int    `json:"category_id"`
		Name       string `json:"name"`
	} `json:"categories"`
}

// NewClient creates a new NexusMods API client
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		httpClient: httpClient,
		rest: httpclient.New(httpclient.Options{
			HTTPClient:  httpClient,
			BaseURL:     defaultBaseURL,
			APIKey:      apiKey,
			AuthHeader:  "apikey",
			AuthLabel:   "NexusMods",
			ErrorMapper: mapStatusError,
		}),
		apiKey:     apiKey,
		graphqlURL: defaultGraphQLURL,
	}
}

// SetAPIKey sets the API key for authentication
func (c *Client) SetAPIKey(key string) {
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	c.apiKey = key
	c.rest.SetAPIKey(key)
}

// key returns the configured API key.
func (c *Client) key() string {
	c.keyMu.RLock()
	defer c.keyMu.RUnlock()
	return c.apiKey
}

// SetBaseURL overrides the REST API base URL — primarily used by tests that
// front the client with an httptest server.
func (c *Client) SetBaseURL(u string) {
	c.rest.SetBaseURL(u)
}

// IsAuthenticated returns true if an API key is configured
func (c *Client) IsAuthenticated() bool {
	return c.key() != ""
}

// validateResponse is the part of GET /v1/users/validate.json lmm reads: the
// account's tier (#601). The endpoint also sends "is_premium?" duplicates,
// which are not decoded.
type validateResponse struct {
	IsPremium bool `json:"is_premium"`
}

// validateBodyLimit bounds how much of the validate endpoint's answer is
// read: the real one is a few hundred bytes.
const validateBodyLimit = 64 << 10

// errInvalidKey is the validate endpoint's 401.
var errInvalidKey = errors.New("invalid API key")

// errUndecodable marks a 200 from the validate endpoint whose body did not
// decode: the key is valid, only the tier is unknown.
var errUndecodable = errors.New("undecodable validate response")

// ValidateAPIKey validates an API key by calling the NexusMods validate
// endpoint. A 200 is a valid key whatever its body says: only AccountTier
// needs the body.
func (c *Client) ValidateAPIKey(ctx context.Context, key string) error {
	_, err := c.validate(ctx, key)
	if errors.Is(err, errUndecodable) {
		return nil
	}
	return err
}

// validate asks the validate endpoint about key and decodes its answer.
func (c *Client) validate(ctx context.Context, key string) (v validateResponse, err error) {
	url := c.rest.BaseURL() + "/v1/users/validate.json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return v, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("apikey", key)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return v, fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return v, errInvalidKey
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, validateBodyLimit))
	if resp.StatusCode != http.StatusOK {
		if readErr != nil {
			return v, fmt.Errorf("API error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
		return v, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}
	if readErr != nil {
		return v, fmt.Errorf("%w: reading response: %w", errUndecodable, readErr)
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, fmt.Errorf("%w: %w", errUndecodable, err)
	}
	return v, nil
}

// AccountTier reports the configured key's account tier from the validate
// endpoint's is_premium, cached per key for tierTTL (see Client.tier), and
// forgotten on a Premium-only download refusal (forgetTier). No key, or a
// key the endpoint rejects, is domain.ErrAuthRequired.
func (c *Client) AccountTier(ctx context.Context) (source.Tier, error) {
	key := c.key()
	if key == "" {
		return "", fmt.Errorf("NexusMods: %w", domain.ErrAuthRequired)
	}
	c.tierMu.Lock()
	defer c.tierMu.Unlock()
	if c.tier != "" && c.tierKey == key && c.clock().Sub(c.tierAt) < tierTTL {
		return c.tier, nil
	}
	v, err := c.validate(ctx, key)
	if errors.Is(err, errInvalidKey) {
		return "", fmt.Errorf("NexusMods rejected the API key: %w", domain.ErrAuthRequired)
	}
	if err != nil {
		return "", fmt.Errorf("checking the NexusMods account tier: %w", err)
	}
	tier := source.TierFree
	if v.IsPremium {
		tier = source.TierPremium
	}
	c.tierKey, c.tier, c.tierAt = key, tier, c.clock()
	return tier, nil
}

// doRequest performs an authenticated REST request and JSON-decodes the response.
// Thin wrapper around httpclient.Client.DoJSON, kept for callsite stability.
func (c *Client) doRequest(ctx context.Context, method, path string, result interface{}) error {
	return c.rest.DoJSON(ctx, method, path, result)
}

// apiStatusError is a REST endpoint's non-2xx answer with the status kept
// as data, so a caller can classify it (a mod endpoint's 404) rather than
// match the message. The message is the shared client's own.
type apiStatusError struct {
	status int
	body   string
}

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.status, e.body)
}

// mapStatusError is the REST client's ErrorMapper: every non-2xx answer but
// 401 becomes an apiStatusError. 401 is left to the shared client, which
// maps it to domain.ErrAuthRequired.
func mapStatusError(status int, body []byte, _ string) error {
	if status == http.StatusUnauthorized {
		return nil
	}
	return &apiStatusError{status: status, body: string(body)}
}

// isStatus reports whether err is a REST answer with the given status.
func isStatus(err error, status int) bool {
	var se *apiStatusError
	return errors.As(err, &se) && se.status == status
}

// modNotFound classifies a mod endpoint's error (#540). A 404 is NexusMods
// saying it has no such mod - but an unknown game domain answers 404 too, so
// the game is confirmed first: only then is the miss domain.ErrModNotFound.
// An unknown game is reported as such, and a game lookup that fails leaves
// the original error untyped, since a failed read must never mark a mod
// missing. Any other error is returned unchanged.
func (c *Client) modNotFound(ctx context.Context, gameDomain string, modID int, err error) error {
	if !isStatus(err, http.StatusNotFound) {
		return err
	}
	known, gerr := c.gameExists(ctx, gameDomain)
	switch {
	case gerr != nil:
		return err
	case !known:
		return fmt.Errorf("%q is not a Nexus Mods game: %w", gameDomain, err)
	}
	return fmt.Errorf("mod %d is not on Nexus Mods (HTTP 404): %w", modID, domain.ErrModNotFound)
}

// gameExists asks the games endpoint whether gameDomain is a Nexus Mods
// game: true on 200, false on 404, an error for anything else. A confirmed
// game is remembered for the client's lifetime.
func (c *Client) gameExists(ctx context.Context, gameDomain string) (bool, error) {
	c.knownGamesMu.Lock()
	known := c.knownGames[gameDomain]
	c.knownGamesMu.Unlock()
	if known {
		return true, nil
	}

	_, err := c.fetchGame(ctx, gameDomain)
	switch {
	case isStatus(err, http.StatusNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// fetchGame reads gameDomain from the games endpoint, remembering both that
// the game exists and its category names, so gameExists and CategoryName
// share one request per domain.
func (c *Client) fetchGame(ctx context.Context, gameDomain string) (map[int]string, error) {
	var game gameResponse
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/games/%s.json", gameDomain), &game); err != nil {
		return nil, err
	}
	names := make(map[int]string, len(game.Categories))
	for _, cat := range game.Categories {
		if cat.Name != "" {
			names[cat.CategoryID] = cat.Name
		}
	}

	c.knownGamesMu.Lock()
	defer c.knownGamesMu.Unlock()
	if c.knownGames == nil {
		c.knownGames = make(map[string]bool)
	}
	c.knownGames[gameDomain] = true
	if c.categories == nil {
		c.categories = make(map[string]map[int]string)
	}
	c.categories[gameDomain] = names
	delete(c.categoryMisses, gameDomain)
	return names, nil
}

// CategoryName is the name of categoryID in gameDomain's category list
// (#592), or "" when the id is not in it or the list could not be read. The
// list is fetched once per domain for the client's lifetime; a failed fetch
// is not retried for categoryRetryAfter. It never returns an error: a
// category name is display metadata, and no caller may fail over it.
func (c *Client) CategoryName(ctx context.Context, gameDomain string, categoryID int) string {
	if categoryID <= 0 || gameDomain == "" {
		return ""
	}
	clock := c.now
	if clock == nil {
		clock = time.Now
	}
	c.knownGamesMu.Lock()
	names, cached := c.categories[gameDomain]
	missed, failed := c.categoryMisses[gameDomain]
	c.knownGamesMu.Unlock()
	if cached {
		return names[categoryID]
	}
	if failed && clock().Sub(missed) < categoryRetryAfter {
		return ""
	}

	names, err := c.fetchGame(ctx, gameDomain)
	if err != nil {
		if ctx.Err() != nil {
			// The caller gave up (a closed tab, a Ctrl-C): that says
			// nothing about NexusMods, so it starts no back-off.
			return ""
		}
		c.knownGamesMu.Lock()
		if c.categoryMisses == nil {
			c.categoryMisses = make(map[string]time.Time)
		}
		c.categoryMisses[gameDomain] = clock()
		c.knownGamesMu.Unlock()
		return ""
	}
	return names[categoryID]
}

// GetMod fetches a mod by ID. A mod NexusMods has no record of is
// domain.ErrModNotFound (#540); a removed one still answers 200, with its
// Status saying so.
func (c *Client) GetMod(ctx context.Context, gameDomain string, modID int) (*ModData, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d.json", gameDomain, modID)

	var mod ModData
	if err := c.doRequest(ctx, http.MethodGet, path, &mod); err != nil {
		return nil, fmt.Errorf("getting mod: %w", c.modNotFound(ctx, gameDomain, modID, err))
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
query SearchMods($filter: ModsFilter, $sort: [ModsSort!], $count: Int, $offset: Int) {
  mods(filter: $filter, sort: $sort, count: $count, offset: $offset) {
    nodes {
      modId
      name
      summary
      version
      updatedAt
      downloads
      endorsements
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
				ModID   int    `json:"modId"`
				Name    string `json:"name"`
				Summary string `json:"summary"`
				Version string `json:"version"`
				// UpdatedAt is the Mod type's `updatedAt: DateTime!` (RFC
				// 3339), requested so a search hit carries the date the
				// search surfaces show (#433). Zero when absent.
				UpdatedAt time.Time `json:"updatedAt"`
				// Downloads and Endorsements are the Mod type's `downloads:
				// Int!` and `endorsements: Int!`, requested so a search hit
				// carries the counts the downloads and popular sorts key on
				// (#503) rather than a fabricated zero.
				Downloads    int `json:"downloads"`
				Endorsements int `json:"endorsements"`
				Uploader     struct {
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
//
// category and tags are optional filters, and their names are a CONTRACT
// with NexusMods' ModsFilter input object, not free choices: `categoryName`
// and `tag`, each a list of {value, op} entries, with the ops drawn from
// FilterComparisonOperator. testdata/modsfilter-schema.json records that
// input object and schema_contract_test.go checks this very filter map
// against it offline - which is what #337/#343 lacked, letting `tagNames`
// and `categoryId` sit here long after NexusMods removed them, failing
// every filtered search with "Field is not defined on ModsFilter".
//
// category is the category NAME as NexusMods spells it ("Armour"): the
// schema offers no id-keyed category filter.
//
// sort selects the upstream ordering (#503), so the PAGE fetched is the
// right one rather than the first page re-ordered: updated, downloads and
// popular map to the ModsSort members updatedAt, downloads and endorsements,
// each DESCending. Relevance and the empty value send no `sort` variable at
// all, which leaves the upstream's default ordering untouched.
// testdata/modssort-schema.json records ModsSort and sort_contract_test.go
// checks this very variable against it offline.
func (c *Client) SearchMods(ctx context.Context, gameDomain, query, category string, tags []string, sort domain.SearchSort, limit, offset int) (mods []ModData, err error) {
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
		// categoryNAME, not categoryId (#343): ModsFilter has no id-keyed
		// category filter at all, so `--category` takes the category's
		// name as NexusMods spells it ("Armour"). The retired categoryId
		// made every category-filtered search fail server-side with
		// "Field is not defined on ModsFilter".
		filter["categoryName"] = []map[string]interface{}{
			{"value": category, "op": "EQUALS"},
		}
	}
	if len(tags) > 0 {
		// Build filter entries for all tags (ANDed - mod must have all specified tags).
		// `tag`, not the retired tagNames (#337) - same server-side failure.
		tagFilters := make([]map[string]interface{}, len(tags))
		for i, tag := range tags {
			tagFilters[i] = map[string]interface{}{"value": tag, "op": "EQUALS"}
		}
		filter["tag"] = tagFilters
	}

	variables := map[string]interface{}{
		"filter": filter,
		"count":  limit,
		"offset": offset,
	}
	if member := modsSortMember(sort); member != "" {
		variables["sort"] = []map[string]interface{}{
			{member: map[string]interface{}{"direction": "DESC"}},
		}
	}

	reqBody := graphqlRequest{
		Query:     graphqlSearchQuery,
		Variables: variables,
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
	if key := c.key(); key != "" {
		req.Header.Set("apikey", key)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

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
			ModID:       node.ModID,
			Name:        node.Name,
			Summary:     node.Summary,
			Version:     node.Version,
			Author:      node.Uploader.Name,
			UpdatedTime: node.UpdatedAt,

			DownloadCount:    node.Downloads,
			EndorsementCount: node.Endorsements,
		}
	}

	return results, nil
}

// modsSortMember names the ModsSort member a search sort orders by, or "" for
// relevance (and the empty value), which sends no sort at all.
func modsSortMember(sort domain.SearchSort) string {
	switch sort {
	case domain.SortUpdated:
		return "updatedAt"
	case domain.SortDownloads:
		return "downloads"
	case domain.SortPopular:
		return "endorsements"
	}
	return ""
}

// GetModFiles fetches files for a mod. A mod NexusMods has no record of is
// domain.ErrModNotFound, as for GetMod (#540).
func (c *Client) GetModFiles(ctx context.Context, gameDomain string, modID int) (*ModFileList, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d/files.json", gameDomain, modID)

	var files ModFileList
	if err := c.doRequest(ctx, http.MethodGet, path, &files); err != nil {
		return nil, fmt.Errorf("getting mod files: %w", c.modNotFound(ctx, gameDomain, modID, err))
	}

	return &files, nil
}

// GetDownloadLinks fetches download URLs for a mod file
func (c *Client) GetDownloadLinks(ctx context.Context, gameDomain string, modID, fileID int) ([]DownloadLink, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/%d/files/%d/download_link.json", gameDomain, modID, fileID)

	var links []DownloadLink
	if err := c.doRequest(ctx, http.MethodGet, path, &links); err != nil {
		if isPremiumOnly(err) {
			c.forgetTier()
			return nil, &source.AccountRestrictedError{Reason: premiumOnlyReason}
		}
		return nil, fmt.Errorf("getting download links: %w", err)
	}

	return links, nil
}

// premiumOnlyReason is what a free account's download refusal says (#601):
// the API's own sentence carries a typo and embedded indentation, so lmm
// says it plainly instead.
const premiumOnlyReason = "NexusMods serves downloads through its API to Premium accounts only, and this API key's account is not Premium: download the file from the mod's page instead"

// isPremiumOnly reports whether err is the download_link endpoint refusing a
// free account (#601): a 403 whose body says the links are "for premium users
// only". Both are required - a 403 alone is also a removed or hidden file -
// and the wording is matched, not the whole message, which carries the
// API's own typo ("visting") and embedded indentation.
func isPremiumOnly(err error) bool {
	var se *apiStatusError
	return errors.As(err, &se) && se.status == http.StatusForbidden &&
		strings.Contains(strings.ToLower(se.body), "premium users only")
}

// ModRequirement represents a dependency returned from the GraphQL API
type ModRequirement struct {
	ModID   int
	ModName string
}

// GetModRequirements fetches mod dependencies using the GraphQL API
func (c *Client) GetModRequirements(ctx context.Context, gameDomain string, modID int) (requirements []ModRequirement, err error) {
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
	if key := c.key(); key != "" {
		req.Header.Set("apikey", key)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

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
	requirements = make([]ModRequirement, len(nodes))
	for i, node := range nodes {
		requirements[i] = ModRequirement{
			ModID:   node.ModID,
			ModName: node.ModName,
		}
	}

	return requirements, nil
}
