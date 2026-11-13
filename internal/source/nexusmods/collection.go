package nexusmods

// collection.go is NexusMods' half of the collection seam (#607, design
// docs/plans/2026-11-10-collections-design.md §2): ResolveGameCollection
// (the list, from GraphQL's collectionRevision), ManifestArchiveURL (the
// recipe archive's link, from the revision's download_link path) and
// ParseManifest (Vortex's collection.json, read with an allowlist).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// collectionRef is a parsed collection reference: the game domain a URL
// named (empty for a bare slug), the slug, and the revision asked for (0 is
// the latest published one).
type collectionRef struct {
	domain   string
	slug     string
	revision int
}

// slugPattern is a collection slug as NexusMods mints them (six lower-case
// letters and digits in every sample), with room to spare.
var slugPattern = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// domainPattern is a NexusMods game domain name.
var domainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// parseCollectionRef reads what a user typed for a collection: a slug, a
// collection page URL (https://www.nexusmods.com/games/<domain>/
// collections/<slug>, or the older next.nexusmods.com/<domain>/
// collections/<slug>), either optionally suffixed with "@<revision>". A
// URL's /revisions/<n> form is not accepted: no sample has confirmed it
// (design §1), and guessing at a URL is how the wrong thing is imported.
func parseCollectionRef(ref string) (collectionRef, error) {
	ref = strings.TrimSpace(ref)
	bad := func(why string) (collectionRef, error) {
		return collectionRef{}, fmt.Errorf("%w: %q is not a NexusMods collection (%s); give its slug, slug@revision, or its page's URL",
			source.ErrInvalidReference, ref, why)
	}
	if ref == "" {
		return bad("it is empty")
	}
	var out collectionRef
	body := ref
	if at := strings.LastIndex(body, "@"); at >= 0 {
		n, err := strconv.Atoi(body[at+1:])
		if err != nil || n <= 0 {
			return bad("the revision after @ is not a positive number")
		}
		out.revision, body = n, body[:at]
	}
	if strings.Contains(body, "://") {
		u, err := url.Parse(body)
		if err != nil {
			return bad("the URL does not parse")
		}
		if u.Scheme != "https" {
			return bad("the URL is not https")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch strings.ToLower(u.Hostname()) {
		case "www.nexusmods.com", "nexusmods.com":
			if len(parts) != 4 || parts[0] != "games" || parts[2] != "collections" {
				return bad("the URL is not a collection page")
			}
			out.domain, body = parts[1], parts[3]
		case "next.nexusmods.com":
			if len(parts) != 3 || parts[1] != "collections" {
				return bad("the URL is not a collection page")
			}
			out.domain, body = parts[0], parts[2]
		default:
			return bad("the URL is not on nexusmods.com")
		}
		if !domainPattern.MatchString(out.domain) {
			return bad("the URL's game is not a NexusMods game name")
		}
	}
	out.slug = strings.ToLower(body)
	if !slugPattern.MatchString(out.slug) {
		return bad("the slug is not letters and digits")
	}
	return out, nil
}

// collectionRevisionQuery reads one revision of a collection: its list,
// its external resources and the path of its recipe archive's link. Every
// field it selects is checked offline against a recorded introspection
// (testdata/collection-schema.json, collection_contract_test.go). $revision
// is omitted for the latest published revision.
const collectionRevisionQuery = `
query CollectionRevision($slug: String, $revision: Int, $domain: String, $adult: Boolean) {
  collectionRevision(slug: $slug, revision: $revision, domainName: $domain, viewAdultContent: $adult) {
    id
    revisionNumber
    downloadLink
    collection {
      id
      slug
      name
      game { domainName }
      user { name }
    }
    externalResources {
      name
      resourceType
      resourceUrl
      optional
    }
    modFiles {
      fileId
      optional
      updatePolicy
      version
      file {
        fileId
        modId
        name
        uri
        category
        sizeInBytes
        version
        mod { modId name }
      }
    }
  }
}`

// collectionRevisionResponse is collectionRevisionQuery's answer.
type collectionRevisionResponse struct {
	Data struct {
		CollectionRevision *struct {
			RevisionNumber int    `json:"revisionNumber"`
			DownloadLink   string `json:"downloadLink"`
			Collection     struct {
				Slug string `json:"slug"`
				Name string `json:"name"`
				Game struct {
					DomainName string `json:"domainName"`
				} `json:"game"`
				User *struct {
					Name string `json:"name"`
				} `json:"user"`
			} `json:"collection"`
			ExternalResources []struct {
				Name         string `json:"name"`
				ResourceType string `json:"resourceType"`
				ResourceURL  string `json:"resourceUrl"`
				Optional     bool   `json:"optional"`
			} `json:"externalResources"`
			ModFiles []struct {
				FileID       int    `json:"fileId"`
				Optional     bool   `json:"optional"`
				UpdatePolicy string `json:"updatePolicy"`
				Version      string `json:"version"`
				File         *struct {
					ModID       int         `json:"modId"`
					Name        string      `json:"name"`
					URI         string      `json:"uri"`
					Category    string      `json:"category"`
					SizeInBytes json.Number `json:"sizeInBytes"`
					Mod         *struct {
						Name string `json:"name"`
					} `json:"mod"`
				} `json:"file"`
			} `json:"modFiles"`
		} `json:"collectionRevision"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

// maxGraphQLBody caps a collectionRevision answer: a 2,400-item collection
// is about 1.5 MB.
const maxGraphQLBody = 64 << 20

// ResolveGameCollection implements source.GameCollectionResolver: ref (a
// slug, a collection URL, either with "@<revision>") resolved for the game
// NexusMods knows as gameSourceID. A URL naming another game, and a slug
// NexusMods has no collection for in this game, are the caller's input
// (source.ErrInvalidReference).
func (n *NexusMods) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	if !domainPattern.MatchString(gameSourceID) {
		return source.Collection{}, fmt.Errorf("%w: NexusMods game %q", source.ErrGameIdentifierInvalid, gameSourceID)
	}
	r, err := parseCollectionRef(ref)
	if err != nil {
		return source.Collection{}, err
	}
	if r.domain != "" && r.domain != gameSourceID {
		return source.Collection{}, fmt.Errorf("%w: the collection %s is for the NexusMods game %s, not %s",
			source.ErrInvalidReference, r.slug, r.domain, gameSourceID)
	}
	vars := map[string]any{"slug": r.slug, "domain": gameSourceID, "adult": true}
	if r.revision > 0 {
		vars["revision"] = r.revision
	}
	var resp collectionRevisionResponse
	if err := n.client.graphql(ctx, collectionRevisionQuery, vars, &resp); err != nil {
		return source.Collection{}, fmt.Errorf("resolving NexusMods collection %s: %w", r.slug, err)
	}
	for _, e := range resp.Errors {
		if e.Extensions.Code == "NOT_FOUND" {
			what := r.slug
			if r.revision > 0 {
				what = fmt.Sprintf("%s revision %d", r.slug, r.revision)
			}
			return source.Collection{}, fmt.Errorf("%w: NexusMods has no collection %s for %s", source.ErrInvalidReference, what, gameSourceID)
		}
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return source.Collection{}, fmt.Errorf("resolving NexusMods collection %s: GraphQL errors: %s", r.slug, strings.Join(msgs, "; "))
	}
	rev := resp.Data.CollectionRevision
	if rev == nil {
		return source.Collection{}, fmt.Errorf("%w: NexusMods has no collection %s for %s", source.ErrInvalidReference, r.slug, gameSourceID)
	}
	slug := rev.Collection.Slug
	if slug == "" {
		slug = r.slug
	}
	c := source.Collection{
		ID:       slug,
		Name:     rev.Collection.Name,
		URL:      collectionPageURL(gameSourceID, slug),
		Revision: rev.RevisionNumber,
		Handle:   rev.DownloadLink,
		Items:    make([]source.CollectionItem, 0, len(rev.ModFiles)+len(rev.ExternalResources)),
	}
	if rev.Collection.User != nil {
		c.Author = rev.Collection.User.Name
	}
	for _, mf := range rev.ModFiles {
		it := source.CollectionItem{
			Kind:         source.CollectionItemModFile,
			FileID:       strconv.Itoa(mf.FileID),
			Version:      mf.Version,
			Optional:     mf.Optional,
			UpdatePolicy: mf.UpdatePolicy,
		}
		if f := mf.File; f != nil {
			it.ModID = strconv.Itoa(f.ModID)
			it.FileName = f.Name
			it.FileCategory = f.Category
			if size, err := f.SizeInBytes.Int64(); err == nil {
				it.SizeBytes = size
			}
			if f.Mod != nil {
				it.Name = f.Mod.Name
			}
			it.URL = filePageURL(gameSourceID, it.ModID, it.FileID)
		}
		c.Items = append(c.Items, it)
	}
	for _, e := range rev.ExternalResources {
		c.Items = append(c.Items, source.CollectionItem{
			Kind:     source.CollectionItemExternal,
			Name:     e.Name,
			URL:      e.ResourceURL,
			Optional: e.Optional,
		})
	}
	return c, nil
}

// collectionPageURL is a collection's page on nexusmods.com.
func collectionPageURL(domain, slug string) string {
	return fmt.Sprintf("https://www.nexusmods.com/games/%s/collections/%s", domain, slug)
}

// filePageURL is a mod file's entry on its mod's files tab: where a free
// account downloads it by hand.
func filePageURL(domain, modID, fileID string) string {
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%s?tab=files&file_id=%s", domain, modID, fileID)
}

// handlePattern is the one shape of a revision's download_link path
// (VERIFIED in every surveyed revision): only this is ever requested, so a
// Handle cannot steer the client to another endpoint.
var handlePattern = regexp.MustCompile(`^/v2/collections/[0-9]+/revisions/[0-9]+/download_link$`)

// collectionDownloadLinks is the download_link endpoint's answer.
// download_links is what every surveyed revision returns; download_link is
// the older single form node-nexus-api still reads.
type collectionDownloadLinks struct {
	DownloadLinks []DownloadLink `json:"download_links"`
	DownloadLink  *DownloadLink  `json:"download_link"`
}

// ManifestArchiveURL implements source.CollectionManifestFetcher: the
// recipe archive's short-lived link, from the revision's download_link
// path. It needs an API key - a free account's is enough (VERIFIED
// 2026-11-12, design §11 Q1) - and no key is domain.ErrAuthRequired.
func (n *NexusMods) ManifestArchiveURL(ctx context.Context, c source.Collection) (string, error) {
	if !handlePattern.MatchString(c.Handle) {
		return "", fmt.Errorf("NexusMods collection %s: its recipe link %q is not a revision's download_link path", c.ID, c.Handle)
	}
	if !n.client.IsAuthenticated() {
		return "", fmt.Errorf("NexusMods serves a collection's recipe only to a signed-in account (a free one is enough): %w", domain.ErrAuthRequired)
	}
	var links collectionDownloadLinks
	if err := n.client.doRequest(ctx, http.MethodGet, c.Handle, &links); err != nil {
		return "", fmt.Errorf("getting the recipe link of NexusMods collection %s: %w", c.ID, err)
	}
	all := links.DownloadLinks
	if links.DownloadLink != nil {
		all = append(all, *links.DownloadLink)
	}
	for _, l := range all {
		if strings.HasPrefix(l.URI, "https://") {
			return l.URI, nil
		}
	}
	return "", fmt.Errorf("NexusMods returned no https link for collection %s's recipe", c.ID)
}

// graphql POSTs one GraphQL query and decodes the answer into out, which
// carries the response's own errors member: a GraphQL error arrives with
// HTTP 200, and the caller classifies it.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) (err error) {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := c.key(); key != "" {
		req.Header.Set("apikey", key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphQLBody+1))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if len(data) > maxGraphQLBody {
		return fmt.Errorf("GraphQL response is over %d bytes", maxGraphQLBody)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GraphQL error (status %d): %s", resp.StatusCode, truncate(string(data), 512))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// truncate cuts s to at most n bytes, for an error message.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// maxManifestBytes caps collection.json: the largest surveyed (2,444
// items) is about 2 MB.
const maxManifestBytes = 32 << 20

// manifestName is the recipe's file at the archive root.
const manifestName = "collection.json"

// Allowlists: the keys ParseManifest reads or knows to be informational, at
// each level of Vortex's collection.json (Vortex src/renderer/src/
// extensions/collections/types/ICollection.ts, read for facts, and the 71
// manifests of #607's survey). Any other non-empty key is a feature lmm
// cannot replay, so it is reported in Unknown.
var (
	topKeys = keySet("info", "mods", "modRules", "plugins", "pluginRules", "tools", "collectionConfig", "fbLoadOrders",
		// extensionVersion is the curator's Vortex collections-extension
		// version: informational (ruled 2026-11-13).
		"extensionVersion")
	infoKeys   = keySet("author", "authorUrl", "name", "description", "installInstructions", "domainName", "gameVersions")
	configKeys = keySet("recommendNewProfile", "excludePluginRules", "referenceTagScheme")
	modKeys    = keySet("name", "version", "optional", "domainName", "source", "author", "details", "phase",
		"choices", "hashes", "patches", "instructions", "fileOverrides")
	modSourceKeys = keySet("type", "modId", "fileId", "md5", "fileSize", "logicalFilename", "fileExpression",
		"updatePolicy", "tag", "adultContent", "url", "instructions")
	detailsKeys = keySet("type", "category")
	choicesKeys = keySet("type", "options")
	ruleKeys    = keySet("type", "source", "reference")
	// A rule reference is Vortex's IReference: the matching facts lmm
	// reads, and the ones it knows to be descriptive.
	refKeys = keySet("fileExpression", "fileMD5", "versionMatch", "logicalFileName", "idHint", "tag",
		"md5Hint", "fileSize", "gameId", "id", "repo", "description")
	pluginKeys        = keySet("name", "enabled")
	pluginRulesKeys   = keySet("plugins", "groups")
	pluginRuleKeys    = keySet("name", "group", "after")
	pluginGroupKeys   = keySet("name", "after")
	fileLoadOrderKeys = keySet("id", "entries")
)

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

// ParseManifest implements source.CollectionManifestFetcher: Vortex's
// collection.json at root's top, read into the source-neutral recipe. Every
// non-empty key outside the allowlists is reported in Unknown; an empty
// value (null, "", [] or {}) is the key's absence. A malformed recipe is
// source.ErrInvalidManifest.
func (n *NexusMods) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := readManifest(root)
	if err != nil {
		return source.CollectionManifest{}, err
	}
	p := manifestParser{modIndex: -1}
	m, err := p.parse(data)
	if err != nil {
		return source.CollectionManifest{}, fmt.Errorf("%w: %v", source.ErrInvalidManifest, err)
	}
	return m, nil
}

// readManifest reads collection.json from root, capped.
func readManifest(root fs.FS) ([]byte, error) {
	f, err := root.Open(manifestName)
	if err != nil {
		return nil, fmt.Errorf("%w: the recipe has no %s: %v", source.ErrInvalidManifest, manifestName, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %v", source.ErrInvalidManifest, manifestName, err)
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("%w: %s is over %d bytes", source.ErrInvalidManifest, manifestName, maxManifestBytes)
	}
	return data, nil
}

// manifestParser accumulates the unknown keys of one parse. modIndex is the
// index of the mod being read, -1 outside mods.
type manifestParser struct {
	unknown  []source.ManifestUnknownKey
	modIndex int
}

// object decodes raw as a JSON object; an empty value is an empty object.
func object(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	if isEmpty(raw) {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%s is not an object", what)
	}
	return obj, nil
}

// array decodes raw as a JSON array; an empty value is an empty array.
func array(raw json.RawMessage, what string) ([]json.RawMessage, error) {
	if isEmpty(raw) {
		return nil, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("%s is not a list", what)
	}
	return arr, nil
}

// isEmpty reports whether raw is absent, null, "", [] or {}.
func isEmpty(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" || string(t) == `""` {
		return true
	}
	if t[0] == '[' || t[0] == '{' {
		var v any
		if json.Unmarshal(t, &v) != nil {
			return false
		}
		switch x := v.(type) {
		case []any:
			return len(x) == 0
		case map[string]any:
			return len(x) == 0
		}
	}
	return false
}

// note records obj's non-empty keys outside known as unknown, under
// prefix, on item.
func (p *manifestParser) note(obj map[string]json.RawMessage, known map[string]bool, prefix, item string) {
	keys := make([]string, 0, len(obj))
	for k, v := range obj {
		if !known[k] && !isEmpty(v) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		idx := -1
		if item != "" || strings.HasPrefix(prefix, "mods.") {
			idx = p.modIndex
		}
		p.unknown = append(p.unknown, source.ManifestUnknownKey{Item: item, ModIndex: idx, Key: prefix + k})
	}
}

// str decodes a string member, "" when absent or null.
func str(obj map[string]json.RawMessage, key string) (string, error) {
	raw, ok := obj[key]
	if !ok || isEmpty(raw) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s is not a string", key)
	}
	return s, nil
}

// boolean decodes a bool member, def when absent or null.
func boolean(obj map[string]json.RawMessage, key string, def bool) (bool, error) {
	raw, ok := obj[key]
	if !ok || isEmpty(raw) {
		return def, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, fmt.Errorf("%s is not true or false", key)
	}
	return b, nil
}

// integer decodes an integer member, given as a JSON number or a string of
// digits (ids are both, across Vortex's history); 0 when absent.
func integer(obj map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := obj[key]
	if !ok || isEmpty(raw) {
		return 0, nil
	}
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, fmt.Errorf("%s is not a number", key)
	}
	switch x := v.(type) {
	case json.Number:
		num = x
	case string:
		num = json.Number(x)
	default:
		return 0, fmt.Errorf("%s is not a number", key)
	}
	n, err := num.Int64()
	if err != nil {
		if f, ferr := num.Float64(); ferr == nil && f == float64(int64(f)) {
			return int64(f), nil
		}
		return 0, fmt.Errorf("%s (%s) is not a whole number", key, num)
	}
	return n, nil
}

// id is integer as a decimal string, "" for 0 or absent.
func id(obj map[string]json.RawMessage, key string) (string, error) {
	n, err := integer(obj, key)
	if err != nil || n == 0 {
		return "", err
	}
	if n < 0 {
		return "", fmt.Errorf("%s (%d) is negative", key, n)
	}
	return strconv.FormatInt(n, 10), nil
}

// strings decodes a list-of-strings member.
func stringList(obj map[string]json.RawMessage, key string) ([]string, error) {
	raw, ok := obj[key]
	if !ok || isEmpty(raw) {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s is not a list of strings", key)
	}
	return out, nil
}

func (p *manifestParser) parse(data []byte) (source.CollectionManifest, error) {
	var m source.CollectionManifest
	top, err := object(data, manifestName)
	if err != nil {
		return m, err
	}
	if top == nil {
		return m, errors.New("collection.json is empty")
	}
	p.note(top, topKeys, "", "")

	info, err := object(top["info"], "info")
	if err != nil {
		return m, err
	}
	p.note(info, infoKeys, "info.", "")
	for key, dst := range map[string]*string{
		"name": &m.Name, "author": &m.Author, "description": &m.Description,
		"installInstructions": &m.InstallInstructions, "domainName": &m.GameSourceID,
	} {
		if *dst, err = str(info, key); err != nil {
			return m, fmt.Errorf("info.%v", err)
		}
	}
	if m.GameVersions, err = stringList(info, "gameVersions"); err != nil {
		return m, fmt.Errorf("info.%v", err)
	}

	config, err := object(top["collectionConfig"], "collectionConfig")
	if err != nil {
		return m, err
	}
	p.note(config, configKeys, "collectionConfig.", "")

	mods, err := array(top["mods"], "mods")
	if err != nil {
		return m, err
	}
	for i, raw := range mods {
		p.modIndex = i
		mod, err := p.mod(raw)
		if err != nil {
			return m, fmt.Errorf("mods[%d]: %v", i, err)
		}
		m.Mods = append(m.Mods, mod)
	}
	p.modIndex = -1

	rules, err := array(top["modRules"], "modRules")
	if err != nil {
		return m, err
	}
	for i, raw := range rules {
		r, err := p.rule(raw)
		if err != nil {
			return m, fmt.Errorf("modRules[%d]: %v", i, err)
		}
		m.ModRules = append(m.ModRules, r)
	}

	if m.Plugins, err = p.plugins(top["plugins"]); err != nil {
		return m, err
	}
	if m.PluginRules, err = p.pluginRules(top["pluginRules"]); err != nil {
		return m, err
	}
	if m.Tools, err = tools(top["tools"]); err != nil {
		return m, err
	}
	if m.FileLoadOrders, err = p.fileLoadOrders(top["fbLoadOrders"]); err != nil {
		return m, err
	}
	m.Unknown = p.unknown
	return m, nil
}

func (p *manifestParser) mod(raw json.RawMessage) (source.ManifestMod, error) {
	var mod source.ManifestMod
	obj, err := object(raw, "the entry")
	if err != nil {
		return mod, err
	}
	if mod.Name, err = str(obj, "name"); err != nil {
		return mod, err
	}
	p.note(obj, modKeys, "mods.", mod.Name)
	if mod.Version, err = str(obj, "version"); err != nil {
		return mod, err
	}
	if mod.Author, err = str(obj, "author"); err != nil {
		return mod, err
	}
	if mod.Optional, err = boolean(obj, "optional", false); err != nil {
		return mod, err
	}
	phase, err := integer(obj, "phase")
	if err != nil {
		return mod, err
	}
	if phase < 0 || phase > 1<<20 {
		return mod, fmt.Errorf("phase %d is out of range", phase)
	}
	mod.Phase = int(phase)
	if mod.Instructions, err = str(obj, "instructions"); err != nil {
		return mod, err
	}
	if mod.FileOverrides, err = stringList(obj, "fileOverrides"); err != nil {
		return mod, err
	}

	src, err := object(obj["source"], "source")
	if err != nil {
		return mod, err
	}
	p.note(src, modSourceKeys, "mods.source.", mod.Name)
	for key, dst := range map[string]*string{
		"type": &mod.SourceType, "md5": &mod.MD5, "logicalFilename": &mod.LogicalFilename,
		"fileExpression": &mod.FileExpression, "updatePolicy": &mod.UpdatePolicy, "url": &mod.URL,
	} {
		if *dst, err = str(src, key); err != nil {
			return mod, fmt.Errorf("source.%v", err)
		}
	}
	if mod.ModID, err = id(src, "modId"); err != nil {
		return mod, fmt.Errorf("source.%v", err)
	}
	if mod.FileID, err = id(src, "fileId"); err != nil {
		return mod, fmt.Errorf("source.%v", err)
	}
	if mod.SizeBytes, err = integer(src, "fileSize"); err != nil {
		return mod, fmt.Errorf("source.%v", err)
	}
	srcInstructions, err := str(src, "instructions")
	if err != nil {
		return mod, fmt.Errorf("source.%v", err)
	}
	if srcInstructions != "" {
		if mod.Instructions != "" {
			mod.Instructions += "\n\n"
		}
		mod.Instructions += srcInstructions
	}

	details, err := object(obj["details"], "details")
	if err != nil {
		return mod, err
	}
	p.note(details, detailsKeys, "mods.details.", mod.Name)
	if mod.ModType, err = str(details, "type"); err != nil {
		return mod, fmt.Errorf("details.%v", err)
	}

	choices, err := object(obj["choices"], "choices")
	if err != nil {
		return mod, err
	}
	if choices != nil {
		p.note(choices, choicesKeys, "mods.choices.", mod.Name)
		if mod.InstallerType, err = str(choices, "type"); err != nil {
			return mod, fmt.Errorf("choices.%v", err)
		}
		if mod.InstallerType == "" {
			mod.InstallerType = "unknown"
		}
		if raw := choices["options"]; !isEmpty(raw) {
			if err := json.Unmarshal(raw, &mod.Choices); err != nil {
				return mod, fmt.Errorf("choices.options is not a list of steps: %v", err)
			}
		}
	}

	hashes, err := array(obj["hashes"], "hashes")
	if err != nil {
		return mod, err
	}
	for _, raw := range hashes {
		var h struct {
			Path string `json:"path"`
			MD5  string `json:"md5"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			return mod, fmt.Errorf("hashes: %v", err)
		}
		mod.FileList = append(mod.FileList, source.ManifestFileHash{Path: h.Path, MD5: h.MD5})
	}

	if raw := obj["patches"]; !isEmpty(raw) {
		if err := json.Unmarshal(raw, &mod.Patches); err != nil {
			return mod, fmt.Errorf("patches is not a map of paths to checksums: %v", err)
		}
	}
	return mod, nil
}

func (p *manifestParser) ref(raw json.RawMessage, side string) (source.ManifestModRef, error) {
	var r source.ManifestModRef
	obj, err := object(raw, side)
	if err != nil {
		return r, err
	}
	p.note(obj, refKeys, "modRules."+side+".", "")
	for key, dst := range map[string]*string{
		"fileMD5": &r.FileMD5, "logicalFileName": &r.LogicalFileName, "versionMatch": &r.VersionMatch,
		"fileExpression": &r.FileExpression, "idHint": &r.IDHint, "tag": &r.Tag,
	} {
		if *dst, err = str(obj, key); err != nil {
			return r, fmt.Errorf("%s.%v", side, err)
		}
	}
	return r, nil
}

func (p *manifestParser) rule(raw json.RawMessage) (source.ManifestModRule, error) {
	var r source.ManifestModRule
	obj, err := object(raw, "the rule")
	if err != nil {
		return r, err
	}
	p.note(obj, ruleKeys, "modRules.", "")
	if r.Type, err = str(obj, "type"); err != nil {
		return r, err
	}
	if r.Source, err = p.ref(obj["source"], "source"); err != nil {
		return r, err
	}
	if r.Reference, err = p.ref(obj["reference"], "reference"); err != nil {
		return r, err
	}
	return r, nil
}

func (p *manifestParser) plugins(raw json.RawMessage) ([]source.ManifestPlugin, error) {
	list, err := array(raw, "plugins")
	if err != nil {
		return nil, err
	}
	var out []source.ManifestPlugin
	for i, raw := range list {
		obj, err := object(raw, fmt.Sprintf("plugins[%d]", i))
		if err != nil {
			return nil, err
		}
		p.note(obj, pluginKeys, "plugins.", "")
		var pl source.ManifestPlugin
		if pl.Name, err = str(obj, "name"); err != nil {
			return nil, fmt.Errorf("plugins[%d].%v", i, err)
		}
		if pl.Enabled, err = boolean(obj, "enabled", true); err != nil {
			return nil, fmt.Errorf("plugins[%d].%v", i, err)
		}
		out = append(out, pl)
	}
	return out, nil
}

func (p *manifestParser) pluginRules(raw json.RawMessage) (source.ManifestPluginRules, error) {
	var out source.ManifestPluginRules
	obj, err := object(raw, "pluginRules")
	if err != nil || obj == nil {
		return out, err
	}
	p.note(obj, pluginRulesKeys, "pluginRules.", "")
	plugins, err := array(obj["plugins"], "pluginRules.plugins")
	if err != nil {
		return out, err
	}
	for i, raw := range plugins {
		o, err := object(raw, fmt.Sprintf("pluginRules.plugins[%d]", i))
		if err != nil {
			return out, err
		}
		p.note(o, pluginRuleKeys, "pluginRules.plugins.", "")
		var r source.ManifestPluginRule
		if r.Name, err = str(o, "name"); err != nil {
			return out, err
		}
		if r.Group, err = str(o, "group"); err != nil {
			return out, err
		}
		if r.After, err = stringList(o, "after"); err != nil {
			return out, err
		}
		out.Plugins = append(out.Plugins, r)
	}
	groups, err := array(obj["groups"], "pluginRules.groups")
	if err != nil {
		return out, err
	}
	for i, raw := range groups {
		o, err := object(raw, fmt.Sprintf("pluginRules.groups[%d]", i))
		if err != nil {
			return out, err
		}
		p.note(o, pluginGroupKeys, "pluginRules.groups.", "")
		var g source.ManifestPluginGroup
		if g.Name, err = str(o, "name"); err != nil {
			return out, err
		}
		if g.After, err = stringList(o, "after"); err != nil {
			return out, err
		}
		out.Groups = append(out.Groups, g)
	}
	return out, nil
}

// tools reads the curator's tool shortcuts: their names and executables,
// for a plan to list. Nothing else of them is read, because nothing of
// them is ever run.
func tools(raw json.RawMessage) ([]source.ManifestTool, error) {
	list, err := array(raw, "tools")
	if err != nil {
		return nil, err
	}
	var out []source.ManifestTool
	for i, raw := range list {
		obj, err := object(raw, fmt.Sprintf("tools[%d]", i))
		if err != nil {
			return nil, err
		}
		var t source.ManifestTool
		if t.Name, err = str(obj, "name"); err != nil {
			return nil, fmt.Errorf("tools[%d].%v", i, err)
		}
		if t.Exe, err = str(obj, "exe"); err != nil {
			return nil, fmt.Errorf("tools[%d].%v", i, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// fileLoadOrders reads fbLoadOrders, a list of {id, entries} (VERIFIED in
// the survey's seven manifests that carry it): per load-order id, how many
// entries it orders. A load order with no entries is absent.
func (p *manifestParser) fileLoadOrders(raw json.RawMessage) ([]source.ManifestFileLoadOrder, error) {
	list, err := array(raw, "fbLoadOrders")
	if err != nil {
		return nil, err
	}
	var out []source.ManifestFileLoadOrder
	for i, raw := range list {
		obj, err := object(raw, fmt.Sprintf("fbLoadOrders[%d]", i))
		if err != nil {
			return nil, err
		}
		p.note(obj, fileLoadOrderKeys, "fbLoadOrders.", "")
		id, err := str(obj, "id")
		if err != nil {
			return nil, fmt.Errorf("fbLoadOrders[%d].%v", i, err)
		}
		entries, err := array(obj["entries"], "fbLoadOrders."+id+".entries")
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			continue
		}
		out = append(out, source.ManifestFileLoadOrder{ID: id, Entries: len(entries)})
	}
	return out, nil
}
