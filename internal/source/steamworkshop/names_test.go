package steamworkshop_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The creator ids the recorded fixtures carry.
const (
	creatorA = "76561198000000000"
	creatorB = "76561198000000001"
)

// communityFixture serves recorded Steam Community profile documents by
// steamid64 and records every profile it was asked for. An id with no
// entry gets the "could not be found" document Valve really answers.
type communityFixture struct {
	srv *httptest.Server

	mu       sync.Mutex
	asked    []string
	profiles map[string]string // id -> testdata/community file
	status   int               // non-zero: answer every request with it
}

func serveCommunity(t *testing.T, profiles map[string]string) *communityFixture {
	t.Helper()
	fx := &communityFixture{profiles: profiles}
	fx.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/profiles/"), "/")
		fx.mu.Lock()
		fx.asked = append(fx.asked, id)
		status := fx.status
		fx.mu.Unlock()
		assert.Equal(t, "1", r.URL.Query().Get("xml"))
		assert.Empty(t, r.URL.Query().Get("key"), "the community profile is keyless")
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		fx.mu.Lock()
		file, ok := fx.profiles[id]
		fx.mu.Unlock()
		if !ok {
			file = "profile_missing.xml"
		}
		data, err := os.ReadFile(filepath.Join("testdata", "community", file))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write(data)
	}))
	t.Cleanup(fx.srv.Close)
	return fx
}

// set changes how the fixture answers, safely against the handler.
func (fx *communityFixture) set(status int, id, file string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.status = status
	if id != "" {
		if fx.profiles == nil {
			fx.profiles = map[string]string{}
		}
		fx.profiles[id] = file
	}
}

func (fx *communityFixture) requests() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), fx.asked...)
}

// TestAuthorName_KeylessProfileNamesTheCreator is #420's keyless route: with
// no key registered, GetMod reads the creator's public community profile,
// keeps the id in Author and puts the persona name in AuthorName - and the
// answer is cached, so a second read asks nobody.
func TestAuthorName_KeylessProfileNamesTheCreator(t *testing.T) {
	api := serveFixture(t, "getpublishedfiledetails_ok.json")
	community := serveCommunity(t, map[string]string{creatorA: "profile_ok.xml"})
	src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), nil)

	mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, creatorA, mod.Author, "the id stays in author")
	assert.Equal(t, "Cargo Captain", mod.AuthorName)
	assert.Equal(t, []string{creatorA}, community.requests())

	again, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, "Cargo Captain", again.AuthorName)
	assert.Len(t, community.requests(), 1, "a cached name costs no request")
	assert.Equal(t, map[string]string{creatorA: "Cargo Captain"},
		src.CachedAuthorNames([]string{creatorA, creatorB}), "the cache answers without a request")
}

// TestAuthorName_KeyedBatchNamesEveryCreatorInOneRequest is #420's keyed
// route: with the user's key registered, a search's creators are resolved
// by ONE GetPlayerSummaries call carrying all of them, and the community
// host is never asked. A creator Valve does not return keeps its id.
func TestAuthorName_KeyedBatchNamesEveryCreatorInOneRequest(t *testing.T) {
	var summaries []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := "queryfiles_page1.json"
		if strings.HasPrefix(r.URL.Path, "/ISteamUser/GetPlayerSummaries/") {
			assert.Equal(t, "k", r.URL.Query().Get("key"))
			summaries = append(summaries, r.URL.Query().Get("steamids"))
			file = "getplayersummaries_ok.json"
		}
		data, err := os.ReadFile(filepath.Join("testdata", "api", file))
		require.NoError(t, err)
		_, _ = w.Write(data)
	}))
	t.Cleanup(api.Close)
	community := serveCommunity(t, nil)
	src := newNamedSource(t, api.URL, community.srv.URL, t.TempDir(), nil)
	src.SetAPIKey("k")

	res, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
	require.NoError(t, err)
	require.Len(t, res.Mods, 2)
	assert.Equal(t, []string{creatorA + "," + creatorB}, summaries, "one batched request for every creator")
	assert.Equal(t, "Cargo Captain", res.Mods[0].AuthorName)
	assert.Equal(t, creatorB, res.Mods[1].Author)
	assert.Empty(t, res.Mods[1].AuthorName, "a creator Valve did not return keeps its id")
	assert.Empty(t, community.requests(), "a keyed lookup never falls back for an id Valve answered by omission")
}

// TestAuthorName_FallsBackToTheIDWhenNothingAnswers is the fallback: a
// missing profile, an unreachable community host, and a creator that is
// not an individual steamid64 all leave AuthorName empty and fail nothing.
func TestAuthorName_FallsBackToTheIDWhenNothingAnswers(t *testing.T) {
	t.Run("no such profile", func(t *testing.T) {
		api := serveFixture(t, "getpublishedfiledetails_ok.json")
		community := serveCommunity(t, nil)
		src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), nil)

		mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
		require.NoError(t, err)
		assert.Equal(t, creatorA, mod.Author)
		assert.Empty(t, mod.AuthorName)

		_, err = src.GetMod(context.Background(), "1133870", "3617086610")
		require.NoError(t, err)
		assert.Len(t, community.requests(), 1, "Valve's 'not found' is remembered for the negative TTL")
	})

	t.Run("community host refuses", func(t *testing.T) {
		api := serveFixture(t, "getpublishedfiledetails_ok.json")
		community := serveCommunity(t, nil)
		community.set(http.StatusForbidden, "", "")
		src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), nil)

		mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
		require.NoError(t, err, "a failed name lookup never fails the call that wanted it")
		assert.Empty(t, mod.AuthorName)

		community.set(0, creatorA, "profile_ok.xml")
		mod, err = src.GetMod(context.Background(), "1133870", "3617086610")
		require.NoError(t, err)
		assert.Equal(t, "Cargo Captain", mod.AuthorName, "a refusal is not cached; the next read asks again")
	})

	t.Run("not a steamid64", func(t *testing.T) {
		api := serveFixture(t, "getpublishedfiledetails_undated.json")
		community := serveCommunity(t, nil)
		src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), nil)
		// The undated fixture's item has an empty creator: nothing to ask.
		mod, err := src.GetMod(context.Background(), "1133870", "3617086699")
		require.NoError(t, err)
		assert.Empty(t, mod.AuthorName)
		assert.Empty(t, community.requests(), "an empty creator is never looked up")
		// Nor is a group id or a path-shaped value, which would otherwise
		// reach a URL and a cache path.
		assert.Empty(t, src.CachedAuthorNames([]string{"", "103582791429521412", "../../etc"}))
	})
}

// TestAuthorName_APersonaNameIsMadeSafeToPrint pins the sanitising: a
// persona name is user-chosen text, and a terminal must never receive an
// escape sequence or a bidi override from it. A name that is nothing but
// invisible characters is no name.
func TestAuthorName_APersonaNameIsMadeSafeToPrint(t *testing.T) {
	t.Run("keyed", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			file := "queryfiles_page1.json"
			if strings.HasPrefix(r.URL.Path, "/ISteamUser/") {
				file = "getplayersummaries_hostile.json"
			}
			data, err := os.ReadFile(filepath.Join("testdata", "api", file))
			require.NoError(t, err)
			_, _ = w.Write(data)
		}))
		t.Cleanup(api.Close)
		community := serveCommunity(t, nil)
		src := newNamedSource(t, api.URL, community.srv.URL, t.TempDir(), nil)
		src.SetAPIKey("k")

		res, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 2)
		assert.Equal(t, "Evil Name", res.Mods[0].AuthorName, "no escape sequence, no BEL, no RLO, no newline")
		assert.Empty(t, res.Mods[1].AuthorName, "an invisible name is no name")
	})

	t.Run("keyless", func(t *testing.T) {
		api := serveFixture(t, "getpublishedfiledetails_ok.json")
		community := serveCommunity(t, map[string]string{creatorA: "profile_hostile.xml"})
		src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), nil)

		mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
		require.NoError(t, err)
		assert.Equal(t, "Evil Name", mod.AuthorName)
	})
}

// TestAuthorName_CacheHonoursTheMetadataTTLs pins that a name is served
// for the positive TTL and asked again after it.
func TestAuthorName_CacheHonoursTheMetadataTTLs(t *testing.T) {
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	api := serveFixture(t, "getpublishedfiledetails_ok.json")
	community := serveCommunity(t, map[string]string{creatorA: "profile_ok.xml"})
	src := newNamedSource(t, api.srv.URL, community.srv.URL, t.TempDir(), func() time.Time { return clock })

	_, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	clock = clock.Add(5 * time.Hour)
	assert.NotEmpty(t, src.CachedAuthorNames([]string{creatorA}), "within six hours")
	clock = clock.Add(2 * time.Hour)
	assert.Empty(t, src.CachedAuthorNames([]string{creatorA}), "past six hours the name is stale")

	mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, "Cargo Captain", mod.AuthorName)
	assert.Len(t, community.requests(), 2, "a stale name is asked for again")
}

// TestAuthorName_AFailingKeyedLookupNeverSuspendsMetadata pins that the
// keyed GetPlayerSummaries lookup has its own circuit breaker: three
// failing name lookups on a cached item must leave GetPublishedFileDetails
// reachable for the next one, and the key never reaches an error.
func TestAuthorName_AFailingKeyedLookupNeverSuspendsMetadata(t *testing.T) {
	okBody, err := os.ReadFile(filepath.Join("testdata", "api", "getpublishedfiledetails_ok.json"))
	require.NoError(t, err)
	var (
		mu      sync.Mutex
		metaIDs []string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ISteamUser/") {
			assert.NotEmpty(t, r.URL.Query().Get("key"), "the name lookup is the keyed route")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		assert.Empty(t, r.URL.Query().Get("key"), "metadata stays keyless")
		assert.NoError(t, r.ParseForm())
		id := r.PostForm.Get("publishedfileids[0]")
		mu.Lock()
		metaIDs = append(metaIDs, id)
		mu.Unlock()
		_, _ = w.Write([]byte(strings.ReplaceAll(string(okBody), "3617086610", id)))
	}))
	t.Cleanup(api.Close)
	community := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(community.Close)

	const key = "SECRETKEY123"
	src := newNamedSource(t, api.URL, community.URL, t.TempDir(), nil)
	src.SetAPIKey(key)
	ctx := context.Background()
	for i := range 3 {
		mod, err := src.GetMod(ctx, "1133870", "3617086610")
		require.NoError(t, err, "view %d", i)
		assert.Empty(t, mod.AuthorName, "nothing answered, so the id is shown")
	}

	mod, err := src.GetMod(ctx, "1133870", "3617086611")
	if err != nil {
		assert.NotContains(t, err.Error(), key)
	}
	require.NoError(t, err, "a failing name lookup must never suspend metadata")
	assert.Equal(t, "3617086611", mod.ID)
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, metaIDs, "3617086611", "item B's metadata request reached the server")
}

// TestAuthorName_AMalformedProfileFallsBackToTheID pins that a profile
// document that will not decode shows the id and is not cached.
func TestAuthorName_AMalformedProfileFallsBackToTheID(t *testing.T) {
	api := serveFixture(t, "getpublishedfiledetails_ok.json")
	community, hits := serveProfileBody(t, func() string {
		return "<profile><steamID64>" + creatorA + "</steamID64><steamID>unterminated"
	})
	src := newNamedSource(t, api.srv.URL, community.URL, t.TempDir(), nil)

	mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, creatorA, mod.Author)
	assert.Empty(t, mod.AuthorName)

	_, err = src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.EqualValues(t, 2, hits.Load(), "an unreadable profile is not cached, so it is asked again")
}

// TestAuthorName_AnOversizedProfileFallsBackToTheID pins the community
// client's response cap: a valid profile padded past it yields no name.
func TestAuthorName_AnOversizedProfileFallsBackToTheID(t *testing.T) {
	api := serveFixture(t, "getpublishedfiledetails_ok.json")
	body := "<profile><steamID64>" + creatorA + "</steamID64><steamID>Big</steamID><pad>" +
		strings.Repeat("A", 300<<10) + "</pad></profile>"
	community, _ := serveProfileBody(t, func() string { return body })
	src := newNamedSource(t, api.srv.URL, community.URL, t.TempDir(), nil)

	mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Empty(t, mod.AuthorName, "an oversized body must not yield a name")
}

// TestAuthorName_ARenameShowsOnceTheCachedNameExpires pins that the cached
// name is served within the positive TTL and the new one after it.
func TestAuthorName_ARenameShowsOnceTheCachedNameExpires(t *testing.T) {
	var (
		mu   sync.Mutex
		name = "Old Name"
	)
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	api := serveFixture(t, "getpublishedfiledetails_ok.json")
	community, _ := serveProfileBody(t, func() string {
		mu.Lock()
		defer mu.Unlock()
		return "<profile><steamID64>" + creatorA + "</steamID64><steamID><![CDATA[" + name + "]]></steamID></profile>"
	})
	src := newNamedSource(t, api.srv.URL, community.URL, t.TempDir(), func() time.Time { return clock })
	ctx := context.Background()

	mod, err := src.GetMod(ctx, "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, "Old Name", mod.AuthorName)

	mu.Lock()
	name = "New Name"
	mu.Unlock()
	clock = clock.Add(time.Hour)
	mod, err = src.GetMod(ctx, "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, "Old Name", mod.AuthorName, "within the TTL the cached name is served")

	clock = clock.Add(6 * time.Hour)
	mod, err = src.GetMod(ctx, "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, "New Name", mod.AuthorName, "after the TTL the new name is fetched")
}

// serveProfileBody answers every request with body() and counts them.
func serveProfileBody(t *testing.T, body func() string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body()))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}
