package nexusmods

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collection fixtures under testdata/collections are REAL NexusMods
// recipes and revision lists (#607's fixture survey, recorded 2026-11-13;
// see docs/plans/2026-11-10-collections-spike-findings.md Part 6), reduced
// to names and ids: the curator's prose is replaced, author names are
// placeholders, and long hash lists and load orders are cut to three
// entries.

var (
	_ source.GameCollectionResolver    = (*NexusMods)(nil)
	_ source.CollectionManifestFetcher = (*NexusMods)(nil)
)

func TestParseCollectionRef(t *testing.T) {
	cases := []struct {
		ref     string
		domain  string
		slug    string
		rev     int
		wantErr bool
	}{
		{ref: "iszwwe", slug: "iszwwe"},
		{ref: "  iszwwe@484 ", slug: "iszwwe", rev: 484},
		{ref: "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe", domain: "cyberpunk2077", slug: "iszwwe"},
		{ref: "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe/", domain: "cyberpunk2077", slug: "iszwwe"},
		{ref: "https://nexusmods.com/games/cyberpunk2077/collections/iszwwe@484", domain: "cyberpunk2077", slug: "iszwwe", rev: 484},
		{ref: "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe?tab=mods", domain: "cyberpunk2077", slug: "iszwwe"},
		{ref: "https://next.nexusmods.com/skyrimspecialedition/collections/xk05aw", domain: "skyrimspecialedition", slug: "xk05aw"},
		{ref: "", wantErr: true},
		{ref: "iszwwe@", wantErr: true},
		{ref: "iszwwe@0", wantErr: true},
		{ref: "iszwwe@-3", wantErr: true},
		{ref: "iszwwe@abc", wantErr: true},
		{ref: "ISZWWE", slug: "iszwwe"},
		{ref: "not a slug", wantErr: true},
		{ref: "../etc", wantErr: true},
		{ref: "http://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe", wantErr: true},
		{ref: "https://evil.example/games/cyberpunk2077/collections/iszwwe", wantErr: true},
		{ref: "https://www.nexusmods.com/cyberpunk2077/mods/107", wantErr: true},
		// The /revisions/<n> URL form is unverified (design §1), so it is
		// not guessed at.
		{ref: "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe/revisions/484", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := parseCollectionRef(tc.ref)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, source.ErrInvalidReference)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, collectionRef{domain: tc.domain, slug: tc.slug, revision: tc.rev}, got)
		})
	}
}

// revisionServer serves a recorded collectionRevision response and records
// the variables of every query it was sent.
func revisionServer(t *testing.T, fixture string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "collections", fixture))
	require.NoError(t, err)
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Contains(t, req.Query, "collectionRevision")
		assert.Equal(t, "testapikey", r.Header.Get("apikey"))
		seen = append(seen, req.Variables)
		w.Header().Set("Content-Type", "application/json")
		if req.Variables["slug"] != "iszwwe" && req.Variables["slug"] != "xk05aw" {
			_, _ = io.WriteString(w, `{"errors":[{"message":"Collection not found","extensions":{"code":"NOT_FOUND"}}],"data":null}`)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestResolveGameCollection_RecordedRevision(t *testing.T) {
	srv, seen := revisionServer(t, "revision-cyberpunk2077-iszwwe-484.json")
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL

	c, err := nm.ResolveGameCollection(context.Background(), "cyberpunk2077", "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe@484")
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	assert.Equal(t, map[string]any{"slug": "iszwwe", "revision": float64(484), "domain": "cyberpunk2077", "adult": true}, (*seen)[0])

	assert.Equal(t, "iszwwe", c.ID)
	assert.Equal(t, "Welcome to Night City 2.5", c.Name)
	assert.Equal(t, 484, c.Revision)
	assert.Equal(t, "curator", c.Author)
	assert.Equal(t, "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe", c.URL)
	assert.Equal(t, "/v2/collections/11525/revisions/833991/download_link", c.Handle)
	require.Len(t, c.Items, 364)

	categories := map[string]int{}
	for _, it := range c.Items {
		assert.Equal(t, source.CollectionItemModFile, it.Kind)
		assert.NotEmpty(t, it.ModID)
		assert.NotEmpty(t, it.FileID)
		categories[it.FileCategory]++
	}
	assert.Equal(t, map[string]int{"MAIN": 327, "OLD_VERSION": 22, "OPTIONAL": 11, "ARCHIVED": 4}, categories)

	first := c.Items[0]
	assert.Equal(t, "exact", first.UpdatePolicy)
	assert.Equal(t, "https://www.nexusmods.com/cyberpunk2077/mods/"+first.ModID+"?tab=files&file_id="+first.FileID, first.URL)
	assert.Positive(t, first.SizeBytes)
}

func TestResolveGameCollection_LatestOmitsTheRevision(t *testing.T) {
	srv, seen := revisionServer(t, "revision-skyrimspecialedition-xk05aw-325.json")
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL

	c, err := nm.ResolveGameCollection(context.Background(), "skyrimspecialedition", "xk05aw")
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	_, hasRevision := (*seen)[0]["revision"]
	assert.False(t, hasRevision, "no revision asks for the latest published one")
	assert.Equal(t, 325, c.Revision, "the answer records the revision it resolved")
	assert.Len(t, c.Items, 60)
}

func TestResolveGameCollection_Refusals(t *testing.T) {
	srv, seen := revisionServer(t, "revision-cyberpunk2077-iszwwe-484.json")
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL
	ctx := context.Background()

	_, err := nm.ResolveGameCollection(ctx, "skyrimspecialedition", "https://www.nexusmods.com/games/cyberpunk2077/collections/iszwwe")
	require.ErrorIs(t, err, source.ErrInvalidReference, "a URL for another game is refused, never imported into this one")
	assert.Contains(t, err.Error(), "cyberpunk2077")
	assert.Empty(t, *seen, "refused before any request")

	_, err = nm.ResolveGameCollection(ctx, "cyberpunk2077", "zzzzzz")
	require.ErrorIs(t, err, source.ErrInvalidReference, "an unknown slug is the user's input")
	assert.Contains(t, err.Error(), "zzzzzz")

	_, err = nm.ResolveGameCollection(ctx, "", "iszwwe")
	require.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
}

func TestResolveGameCollection_ExternalResources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"collectionRevision":{"id":1,"revisionNumber":2,"downloadLink":"/v2/collections/9/revisions/1/download_link",
			"collection":{"id":9,"slug":"abcdef","name":"C","game":{"domainName":"fallout4"},"user":{"name":"u"}},
			"externalResources":[{"name":"F4SE","resourceType":"browse","resourceUrl":"https://f4se.silverlock.org","optional":true}],
			"modFiles":[{"fileId":5,"optional":false,"updatePolicy":"prefer","version":"1.0","file":null}]}}}`)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL

	c, err := nm.ResolveGameCollection(context.Background(), "fallout4", "abcdef@2")
	require.NoError(t, err)
	require.Len(t, c.Items, 2)
	assert.Equal(t, source.CollectionItem{Kind: source.CollectionItemModFile, FileID: "5", Version: "1.0", UpdatePolicy: "prefer"}, c.Items[0],
		"a file NexusMods no longer describes keeps its pin")
	assert.Equal(t, source.CollectionItem{Kind: source.CollectionItemExternal, Name: "F4SE", URL: "https://f4se.silverlock.org", Optional: true}, c.Items[1])
}

func TestManifestArchiveURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/collections/11525/revisions/833991/download_link", r.URL.Path)
		assert.Equal(t, "testapikey", r.Header.Get("apikey"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"download_links":[{"name":"x","short_name":"Nexus CDN","URI":"https://cdn.example/archive.7z?sig=1"}]}`)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(srv.URL)
	ctx := context.Background()

	got, err := nm.ManifestArchiveURL(ctx, source.Collection{Handle: "/v2/collections/11525/revisions/833991/download_link"})
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example/archive.7z?sig=1", got)

	for _, bad := range []string{"", "/v1/users/validate.json", "https://evil.example/v2/collections/1/revisions/2/download_link", "/v2/collections/1/revisions/2/download_link/../../x"} {
		_, err := nm.ManifestArchiveURL(ctx, source.Collection{Handle: bad})
		assert.Error(t, err, "handle %q", bad)
	}
}

func TestManifestArchiveURL_NoKey(t *testing.T) {
	nm := New(nil, "")
	_, err := nm.ManifestArchiveURL(context.Background(), source.Collection{Handle: "/v2/collections/1/revisions/2/download_link"})
	require.ErrorIs(t, err, domain.ErrAuthRequired, "the recipe needs a key, a free one included")
}

func readManifestFixture(t *testing.T, name string) source.CollectionManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "collections", name))
	require.NoError(t, err)
	m, err := New(nil, "").ParseManifest(fstest.MapFS{"collection.json": {Data: data}})
	require.NoError(t, err)
	return m
}

func unknownKeys(m source.CollectionManifest) []string {
	var out []string
	for _, u := range m.Unknown {
		out = append(out, u.Key)
	}
	return out
}

func TestParseManifest_Cyberpunk(t *testing.T) {
	m := readManifestFixture(t, "cyberpunk2077-iszwwe-484.json")
	assert.Equal(t, "Welcome to Night City 2.5", m.Name)
	assert.Equal(t, "cyberpunk2077", m.GameSourceID)
	assert.Equal(t, []string{"2.5.0.0"}, m.GameVersions)
	require.Len(t, m.Mods, 364)
	assert.Len(t, m.ModRules, 27)
	assert.Empty(t, m.Unknown, "loadOrder: [] and tools: [] are empty, so absent")
	assert.Empty(t, m.Tools)

	phases := map[int]int{}
	var fileLists, choices int
	for _, mod := range m.Mods {
		phases[mod.Phase]++
		assert.Equal(t, "nexus", mod.SourceType)
		assert.Equal(t, "exact", mod.UpdatePolicy)
		assert.NotEmpty(t, mod.MD5)
		if len(mod.FileList) > 0 {
			fileLists++
		}
		if mod.InstallerType != "" {
			choices++
			assert.Equal(t, "fomod", mod.InstallerType)
			// A choice name's leading space is kept: matching never trims.
			assert.Equal(t, " Spicy Clean Hacking Screen", mod.Choices[0].Groups[0].Choices[0].Name)
		}
	}
	assert.Equal(t, map[int]int{0: 16, 1: 297, 2: 50, 3: 1}, phases)
	assert.Equal(t, 11, fileLists)
	assert.Equal(t, 1, choices)

	require.Len(t, m.FileLoadOrders, 1, "an fbLoadOrders entry with no entries is absent")
	assert.Equal(t, "cyberpunk2077-archive", m.FileLoadOrders[0].ID)
	assert.Positive(t, m.FileLoadOrders[0].Entries)

	r := m.ModRules[0]
	assert.Contains(t, []string{"before", "after"}, r.Type)
	assert.NotEmpty(t, r.Source.FileMD5)
	assert.NotEmpty(t, r.Reference.LogicalFileName)
}

func TestParseManifest_Skyrim(t *testing.T) {
	m := readManifestFixture(t, "skyrimspecialedition-xk05aw-325.json")
	require.Len(t, m.Mods, 60)
	assert.Empty(t, m.Unknown, "excludePluginRules is a known collectionConfig key")
	assert.Len(t, m.Plugins, 16)
	assert.True(t, m.Plugins[0].Enabled)
	assert.True(t, m.PluginRules.IsZero())
	require.Len(t, m.Tools, 2)
	assert.Contains(t, m.Tools[0].Exe, "SSEEdit")

	var dinput, fomods int
	for _, mod := range m.Mods {
		if mod.ModType == "dinput" {
			dinput++
		}
		if mod.InstallerType == "fomod" {
			fomods++
		}
		if mod.Name == "Rock Traps Trigger Fixes" {
			require.Len(t, mod.Choices, 1)
			assert.Equal(t, "", mod.Choices[0].Name, "an unnamed step is kept as empty")
			require.Len(t, mod.Choices[0].Groups, 1)
			assert.NotNil(t, mod.Choices[0].Groups[0].Choices, "an empty pick list is an answer: pick nothing")
			assert.Empty(t, mod.Choices[0].Groups[0].Choices)
		}
	}
	assert.Equal(t, 1, dinput)
	assert.Equal(t, 18, fomods)
	assert.Contains(t, []string{"after", "conflicts"}, m.ModRules[0].Type)
}

func TestParseManifest_PluginRulesBundlesPatchesExternal(t *testing.T) {
	fo4 := readManifestFixture(t, "fallout4-kem16d-143.json")
	assert.NotEmpty(t, fo4.PluginRules.Plugins, "the survey's pluginRules sample")
	var withAfter int
	for _, p := range fo4.PluginRules.Plugins {
		assert.NotEmpty(t, p.Name)
		if len(p.After) > 0 {
			withAfter++
		}
	}
	assert.Positive(t, withAfter, "LOOT userlist entries spell their edges 'after'")

	sdv := readManifestFixture(t, "stardewvalley-tckf0m-114.json")
	var bundles, patched, smapi int
	for _, mod := range sdv.Mods {
		if mod.SourceType == "bundle" {
			bundles++
			assert.Empty(t, mod.ModID)
		}
		if len(mod.Patches) > 0 {
			patched++
			for _, crc := range mod.Patches {
				assert.Len(t, crc, 8)
			}
		}
		if mod.ModType == "SMAPI" {
			smapi++
		}
	}
	assert.Positive(t, bundles)
	assert.Positive(t, patched)
	assert.Positive(t, smapi)

	ext := readManifestFixture(t, "fallout4-pmmttm-2.json")
	var browse int
	for _, mod := range ext.Mods {
		if mod.SourceType == "browse" {
			browse++
			assert.Equal(t, "https://f4se.silverlock.org", mod.URL)
			assert.True(t, mod.Optional)
			assert.Equal(t, "dinput", mod.ModType)
			assert.NotEmpty(t, mod.Instructions)
		}
	}
	assert.Equal(t, 1, browse)
}

func TestParseManifest_UnknownAndEmptyKeys(t *testing.T) {
	doc := `{
		"info": {"name": "C", "domainName": "skyrimspecialedition", "futureInfo": "x"},
		"extensionVersion": "0.7.1",
		"loadOrder": [{"id": "a.pak"}],
		"mergedData": {"scriptMergedData": "504b"},
		"emptyNew": [],
		"nullNew": null,
		"blankNew": "",
		"collectionConfig": {"recommendNewProfile": false, "brandNew": true},
		"modRules": [{"type": "after", "source": {"fileMD5": "a"}, "reference": {"fileMD5": "b", "repo": {"modId": "1"}, "oddKey": 1}}],
		"mods": [
			{"name": "M", "version": "1", "optional": false, "domainName": "skyrimspecialedition", "phase": 0,
			 "source": {"type": "nexus", "modId": 1, "fileId": 2, "md5": "a", "updatePolicy": "exact", "sourceNew": 1},
			 "details": {"type": "", "detailNew": "y"},
			 "patches": [], "hashes": [], "fileOverrides": [], "modNew": {"k": 1}}
		]
	}`
	m, err := New(nil, "").ParseManifest(fstest.MapFS{"collection.json": {Data: []byte(doc)}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"info.futureInfo", "loadOrder", "mergedData", "collectionConfig.brandNew",
		"modRules.reference.oddKey", "mods.source.sourceNew", "mods.details.detailNew", "mods.modNew",
	}, unknownKeys(m), "extensionVersion is informational; empty values are absent")
	for _, u := range m.Unknown {
		if strings.HasPrefix(u.Key, "mods.") {
			assert.Equal(t, "M", u.Item)
			assert.Equal(t, 0, u.ModIndex)
		} else {
			assert.Empty(t, u.Item)
			assert.Equal(t, -1, u.ModIndex)
		}
	}
	require.Len(t, m.Mods, 1)
	assert.Equal(t, "1", m.Mods[0].ModID)
	assert.Equal(t, "2", m.Mods[0].FileID)
	assert.Nil(t, m.Mods[0].Patches, "patches: [] is no patches")
}

func TestParseManifest_Refusals(t *testing.T) {
	nm := New(nil, "")
	_, err := nm.ParseManifest(fstest.MapFS{})
	require.Error(t, err, "no collection.json")
	assert.True(t, errors.Is(err, source.ErrInvalidManifest), "%v", err)

	_, err = nm.ParseManifest(fstest.MapFS{"collection.json": {Data: []byte("{not json")}})
	assert.ErrorIs(t, err, source.ErrInvalidManifest)

	_, err = nm.ParseManifest(fstest.MapFS{"collection.json": {Data: []byte(`{"info":{},"mods":[{"name":"x","source":{"type":"nexus","modId":"one"}}]}`)}})
	assert.ErrorIs(t, err, source.ErrInvalidManifest, "an id that is not a number")

	big := make([]byte, maxManifestBytes+1)
	_, err = nm.ParseManifest(fstest.MapFS{"collection.json": {Data: big}})
	assert.ErrorIs(t, err, source.ErrInvalidManifest)
}
