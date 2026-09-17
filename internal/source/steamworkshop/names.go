// Package steamworkshop: this file resolves a Workshop item's creator - a
// raw steamid64 - to the persona name a person recognises (#420).
//
// Two routes, the same answer:
//
//   - With the user's own Steam Web API key registered, ISteamUser's
//     GetPlayerSummaries answers up to 100 ids in one request.
//   - Without one, each id's public community profile is read as XML
//     (steamcommunity.com/profiles/<id>/?xml=1), one request per author,
//     through its own backoff transport - so a throttled community host can
//     never trip the breaker that guards Valve's Web API - and at most
//     maxKeylessLookups per call.
//
// Every answer is cached on disk beside the item metadata, under the same
// TTLs, and every failure falls back to the id itself: a name is a
// nicety, and no lookup failure may fail the call that wanted it.
package steamworkshop

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// DefaultCommunityURL is the Steam Community host the keyless name lookup
// reads. Tests must override it (Options.CommunityURL) with an httptest
// server, exactly as they override DefaultBaseURL.
const DefaultCommunityURL = "https://steamcommunity.com"

// playerSummariesPath is the keyed batch persona endpoint.
const playerSummariesPath = "/ISteamUser/GetPlayerSummaries/v2/"

// maxKeylessLookups caps how many uncached authors ONE call resolves
// through the community profile, which costs a request each. A detail view
// asks for one; the cap only bites a keyless caller with many new authors,
// whose remaining rows show the id this time and a name once the cache has
// caught up.
const maxKeylessLookups = 20

// maxProfileBytes caps a community profile document. A real one is a few
// kilobytes; the cap is what keeps a hostile or broken answer from being
// read without bound.
const maxProfileBytes = 256 << 10

// keylessLookupTimeout bounds one community profile request, so a slow
// host costs a detail view seconds, not the client's full 30.
const keylessLookupTimeout = 10 * time.Second

// maxPersonaRunes caps a stored name. Steam's own limit is 32 characters;
// the margin allows for what Steam counts differently.
const maxPersonaRunes = 64

var _ source.AuthorNameCache = (*Source)(nil)

// newCommunityClient builds the keyless profile reader: its own retry
// transport (and so its own circuit breaker), no key of any kind, and a
// size cap on every answer.
func newCommunityClient(httpClient *http.Client, baseURL string, now func() time.Time) *httpclient.Client {
	own := *httpClient
	own.Transport = newRetryTransport(httpClient.Transport, now)
	return httpclient.New(httpclient.Options{
		HTTPClient: &own,
		BaseURL:    baseURL,
		// Never given a key: declared only because httpclient requires an
		// auth mode.
		AuthQueryParam:   "key",
		AuthLabel:        "Steam Community",
		MaxResponseBytes: maxProfileBytes,
	})
}

// communityURL is the community host for opts: CommunityURL when set;
// otherwise the real host only when BaseURL is Valve's own too. An
// overridden BaseURL - a test's httptest server, or a proxy - with no
// community host of its own sends the name lookup to that same host, so a
// test that points the source away from Valve can never reach the live
// community site by forgetting the second option.
func communityURL(opts Options) string {
	switch {
	case opts.CommunityURL != "":
		return opts.CommunityURL
	case opts.BaseURL == "" || opts.BaseURL == DefaultBaseURL:
		return DefaultCommunityURL
	default:
		return opts.BaseURL
	}
}

// isSteamID64 reports whether id is an individual account's steamid64 -
// 17 digits in the 7656119... range. Anything else (a creator field Valve
// left empty, a group id) is shown as it is and never asked about, and
// the check is also what keeps a hostile value out of a URL and a cache
// path.
func isSteamID64(id string) bool {
	if len(id) != 17 || !strings.HasPrefix(id, "7656119") {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// csiSequence matches an ANSI control sequence (ESC [ params final), so
// cleanPersona drops a colour code whole rather than leaving its "[31m"
// behind once the ESC byte is gone.
var csiSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// cleanPersona makes a persona name safe to print: control sequences and
// control and format characters (terminal escapes, bidi overrides) are
// dropped, whitespace is collapsed, and the result is capped. A name that
// cleans to nothing is no name.
func cleanPersona(name string) string {
	var b strings.Builder
	for _, r := range csiSequence.ReplaceAllString(name, "") {
		switch {
		case r == utf8.RuneError, unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsControl(r), unicode.IsSpace(r):
			r = ' '
		}
		b.WriteRune(r)
	}
	cleaned := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(cleaned) > maxPersonaRunes {
		cleaned = string([]rune(cleaned)[:maxPersonaRunes])
	}
	return cleaned
}

// authorEntry is one cached answer: the name, or Found=false for a
// profile Valve says does not exist (remembered for negativeTTL).
type authorEntry struct {
	FetchedAt int64  `json:"fetched_at"`
	Found     bool   `json:"found"`
	Name      string `json:"name,omitempty"`
}

// authorPath is the cache file for id, or "" when caching is disabled or
// id is not a steamid64.
func (c *metaCache) authorPath(id string) string {
	if c.dir == "" || !isSteamID64(id) {
		return ""
	}
	return filepath.Join(filepath.Dir(c.dir), "authors", id+".json")
}

// getAuthor returns the cached answer for id while it is within its TTL.
func (c *metaCache) getAuthor(id string) (authorEntry, bool) {
	path := c.authorPath(id)
	if path == "" {
		return authorEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return authorEntry{}, false
	}
	var entry authorEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return authorEntry{}, false
	}
	ttl := positiveTTL
	if !entry.Found {
		ttl = negativeTTL
	}
	age := c.now().Sub(time.Unix(entry.FetchedAt, 0))
	if age < 0 || age >= ttl {
		return authorEntry{}, false
	}
	return entry, true
}

// putAuthor records an answer for id; failures are silent, as put's are.
func (c *metaCache) putAuthor(id string, found bool, name string) {
	path := c.authorPath(id)
	if path == "" {
		return
	}
	data, err := json.Marshal(authorEntry{FetchedAt: c.now().Unix(), Found: found, Name: name})
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	writeFileAtomic(path, data)
}

// cachedAuthorNames answers from the cache alone: id -> name for every id
// with a fresh, found entry.
func (c *client) cachedAuthorNames(ids []string) map[string]string {
	out := make(map[string]string)
	for _, id := range ids {
		if entry, ok := c.cache.getAuthor(id); ok && entry.Found {
			out[id] = entry.Name
		}
	}
	return out
}

// authorNames resolves every steamid64 in ids it can, from the cache
// first, then through the keyed batch when a key is registered, then
// through the keyless profile for whatever is left (up to
// maxKeylessLookups). The map holds only the ids it found a name for.
func (c *client) authorNames(ctx context.Context, ids []string) map[string]string {
	out := make(map[string]string)
	var missing []string
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] || !isSteamID64(id) {
			continue
		}
		seen[id] = true
		if entry, ok := c.cache.getAuthor(id); ok {
			if entry.Found {
				out[id] = entry.Name
			}
			continue
		}
		missing = append(missing, id)
	}

	if len(missing) > 0 && c.names.IsAuthenticated() {
		missing = c.keyedAuthorNames(ctx, missing, out)
	}
	for i, id := range missing {
		if i >= maxKeylessLookups || ctx.Err() != nil {
			break
		}
		name, found, err := c.keylessAuthorName(ctx, id)
		if err != nil {
			// The community host is unreachable or throttling: every
			// further id would fail the same way, so stop asking.
			break
		}
		c.cache.putAuthor(id, found, name)
		if found {
			out[id] = name
		}
	}
	return out
}

// playerSummariesResponse is GetPlayerSummaries' envelope.
type playerSummariesResponse struct {
	Response struct {
		Players []struct {
			SteamID     string `json:"steamid"`
			PersonaName string `json:"personaname"`
		} `json:"players"`
	} `json:"response"`
}

// keyedAuthorNames asks GetPlayerSummaries for ids in batches of
// maxIDsPerRequest, recording each answer in out and the cache, and
// returns the ids it got no name for. A failed batch returns its ids
// unanswered, which sends them to the keyless route. It goes through
// c.names, never c.http: the two share a key but not a circuit breaker, so
// a failing lookup here cannot suspend GetPublishedFileDetails.
func (c *client) keyedAuthorNames(ctx context.Context, ids []string, out map[string]string) []string {
	var left []string
	for start := 0; start < len(ids); start += maxIDsPerRequest {
		batch := ids[start:min(start+maxIDsPerRequest, len(ids))]
		var resp playerSummariesResponse
		path := playerSummariesPath + "?steamids=" + strings.Join(batch, ",")
		if err := c.names.DoJSON(ctx, http.MethodGet, path, &resp); err != nil {
			left = append(left, batch...)
			continue
		}
		answered := make(map[string]bool, len(batch))
		for _, p := range resp.Response.Players {
			name := cleanPersona(p.PersonaName)
			if !isSteamID64(p.SteamID) || name == "" {
				continue
			}
			answered[p.SteamID] = true
			out[p.SteamID] = name
			c.cache.putAuthor(p.SteamID, true, name)
		}
		for _, id := range batch {
			if !answered[id] {
				// Valve omits an id it has no account for: that is an
				// answer, remembered for the negative TTL.
				c.cache.putAuthor(id, false, "")
			}
		}
	}
	return left
}

// communityProfile is the subset of a community profile document lmm
// reads. A missing profile answers <response><error>...</error></response>
// instead, which decodes to an empty SteamID64.
type communityProfile struct {
	SteamID64 string `xml:"steamID64"`
	SteamID   string `xml:"steamID"`
}

// errProfileUnreadable marks a profile answer lmm could not use for
// reasons that say nothing about the profile itself.
var errProfileUnreadable = errors.New("steam community profile unreadable")

// keylessAuthorName reads id's public community profile. found=false with
// no error is Valve saying there is no such profile; an error is a lookup
// that says nothing either way.
func (c *client) keylessAuthorName(ctx context.Context, id string) (name string, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, keylessLookupTimeout)
	defer cancel()
	resp, err := c.community.DoStream(ctx, http.MethodGet, "/profiles/"+id+"/?xml=1", nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", false, errProfileUnreadable
	}
	var profile communityProfile
	if err := xml.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return "", false, errProfileUnreadable
	}
	if profile.SteamID64 != id {
		return "", false, nil
	}
	name = cleanPersona(profile.SteamID)
	return name, name != "", nil
}

// withAuthorNames sets AuthorName on every mod whose Author resolves.
func (c *client) withAuthorNames(ctx context.Context, mods []domain.Mod) {
	ids := make([]string, 0, len(mods))
	for _, m := range mods {
		ids = append(ids, m.Author)
	}
	names := c.authorNames(ctx, ids)
	for i := range mods {
		mods[i].AuthorName = names[mods[i].Author]
	}
}

// CachedAuthorNames implements source.AuthorNameCache: the persona names
// already resolved for authors, read from the cache alone - no request of
// any kind - so an installed-mod listing can show them for free.
func (s *Source) CachedAuthorNames(authors []string) map[string]string {
	return s.client.cachedAuthorNames(authors)
}
