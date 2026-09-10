package steamworkshop_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiFixture serves one recorded GetPublishedFileDetails response and
// records every form it was posted, so a test can assert the batching
// contract without ever leaving the process.
type apiFixture struct {
	srv   *httptest.Server
	forms []url.Values
	calls int
}

func serveFixture(t *testing.T, files ...string) *apiFixture {
	t.Helper()
	fx := &apiFixture{}
	fx.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		fx.forms = append(fx.forms, form)
		idx := fx.calls
		fx.calls++
		if idx >= len(files) {
			idx = len(files) - 1
		}
		data, err := os.ReadFile(filepath.Join("testdata", "api", files[idx]))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(fx.srv.Close)
	return fx
}

func newTestSource(t *testing.T, baseURL, cacheDir string, now func() time.Time) *steamworkshop.Source {
	t.Helper()
	return steamworkshop.New(steamworkshop.Options{
		BaseURL:    baseURL,
		CacheDir:   cacheDir,
		Now:        now,
		SteamRoots: []string{t.TempDir()},
	})
}

func TestGetMod_MapsPublishedFileDetails(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	mod, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)

	assert.Equal(t, "3617086610", mod.ID)
	assert.Equal(t, "steamworkshop", mod.SourceID)
	assert.Equal(t, "Sample Workshop Item", mod.Name)
	assert.Equal(t, "An item subscribed in the Steam client.", mod.Summary)
	assert.Equal(t, "An item subscribed in the Steam client.", mod.Description)
	assert.Equal(t, "1133870", mod.GameID)
	assert.Equal(t, "Blueprint", mod.Category, "the first tag is the category")
	assert.Equal(t, "76561198000000000", mod.Author, "the raw creator steamid64: resolving it needs a key")
	assert.Equal(t, "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610", mod.SourceURL)
	assert.Equal(t, "https://steamuserimages-a.akamaihd.net/ugc/0000000000000000001/", mod.PictureURL)
	assert.Equal(t, "7987119735124793734", mod.Version, "the content id is the item's version identity")
	assert.Equal(t, time.Unix(1764767935, 0).UTC(), mod.UpdatedAt.UTC())

	require.Len(t, fx.forms, 1)
	assert.Equal(t, "1", fx.forms[0].Get("itemcount"))
	assert.Equal(t, "3617086610", fx.forms[0].Get("publishedfileids[0]"))
}

func TestGetMod_ItemWithResultNot1IsUnavailable(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_mixed_result9.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	_, err := src.GetMod(context.Background(), "1133870", "2900001111")
	require.Error(t, err)
	assert.ErrorIs(t, err, steamworkshop.ErrItemUnavailable)
}

func TestFetchDetails_BatchesAtOneHundredIDsPerRequest(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	ids := make([]string, 250)
	for i := range ids {
		ids[i] = strings.Repeat("0", 0) + itoa(3000000000+i)
	}
	_, err := src.FetchDetailsForTest(context.Background(), ids, false)
	require.NoError(t, err)

	require.Len(t, fx.forms, 3, "250 ids is three requests")
	assert.Equal(t, "100", fx.forms[0].Get("itemcount"))
	assert.Equal(t, "100", fx.forms[1].Get("itemcount"))
	assert.Equal(t, "50", fx.forms[2].Get("itemcount"))
	assert.Equal(t, ids[0], fx.forms[0].Get("publishedfileids[0]"))
	assert.Equal(t, ids[99], fx.forms[0].Get("publishedfileids[99]"))
	assert.Equal(t, ids[100], fx.forms[1].Get("publishedfileids[0]"))
}

func TestMetaCache_ServesWithinTTLAndRefreshBypasses(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	cacheDir := t.TempDir()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	src := newTestSource(t, fx.srv.URL, cacheDir, func() time.Time { return clock })

	_, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	require.Equal(t, 1, fx.calls)

	// The cache file lands under the "_" prefix, which no game slug can
	// ever produce, so it cannot collide with the mod cache sharing this root.
	assert.FileExists(t, filepath.Join(cacheDir, "_steamworkshop", "meta", "3617086610.json"))

	// Within the 6h positive TTL: served from disk.
	clock = clock.Add(5 * time.Hour)
	_, err = src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, 1, fx.calls, "a fresh cache entry makes no request")

	// --refresh bypasses it even while fresh.
	_, err = src.FetchDetailsForTest(context.Background(), []string{"3617086610"}, true)
	require.NoError(t, err)
	assert.Equal(t, 2, fx.calls)

	// Past the TTL: refetched.
	clock = clock.Add(7 * time.Hour)
	_, err = src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err)
	assert.Equal(t, 3, fx.calls)
}

func TestMetaCache_NegativeEntryExpiresAfterOneHour(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_mixed_result9.json")
	cacheDir := t.TempDir()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	src := newTestSource(t, fx.srv.URL, cacheDir, func() time.Time { return clock })

	_, err := src.GetMod(context.Background(), "1133870", "2900001111")
	require.ErrorIs(t, err, steamworkshop.ErrItemUnavailable)
	require.Equal(t, 1, fx.calls)

	// Inside the 1h negative TTL a delisted item must not hammer the API.
	clock = clock.Add(30 * time.Minute)
	_, err = src.GetMod(context.Background(), "1133870", "2900001111")
	require.ErrorIs(t, err, steamworkshop.ErrItemUnavailable)
	assert.Equal(t, 1, fx.calls, "a cached negative answers without a request")

	clock = clock.Add(90 * time.Minute)
	_, err = src.GetMod(context.Background(), "1133870", "2900001111")
	require.ErrorIs(t, err, steamworkshop.ErrItemUnavailable)
	assert.Equal(t, 2, fx.calls, "past the negative TTL it is asked again")
}

func TestFetchDetails_NoCacheDirStillWorks(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := steamworkshop.New(steamworkshop.Options{BaseURL: fx.srv.URL, SteamRoots: []string{t.TempDir()}})

	_, err := src.GetMod(context.Background(), "1133870", "3617086610")
	require.NoError(t, err, "an unconfigured cache is slower, never broken")
}

func TestScanWorkshopItems_ReadsEveryLibrary(t *testing.T) {
	lib := writeLibrary(t, t.TempDir(), "appworkshop_1133870.acf", map[string][]string{
		"3617086610": {"mod.pak"},
	})
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{lib}})

	scan, err := src.ScanWorkshopItems(context.Background(), "1133870")
	require.NoError(t, err)
	require.Len(t, scan.Items, 3)
	assert.Equal(t, []string{lib}, scan.Roots)
	assert.Empty(t, scan.Warnings)
}

func TestScanWorkshopItems_RejectsANonNumericAppID(t *testing.T) {
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{t.TempDir()}})
	_, err := src.ScanWorkshopItems(context.Background(), "not-an-appid")
	require.Error(t, err)
}

func TestSourceIdentityAndCapabilities(t *testing.T) {
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{t.TempDir()}})
	assert.Equal(t, "steamworkshop", src.ID())
	assert.Equal(t, "Steam Workshop", src.Name())
	assert.Equal(t, "built-in", src.TypeLabel())

	caps := src.Capabilities()
	assert.True(t, caps.Updates, "update checking is the whole point of Tier 1")
	assert.False(t, caps.Search, "search needs a user-supplied key (Tier 2)")
	assert.False(t, caps.Dependencies)
	assert.False(t, caps.Versions)

	// GetModFiles/GetDownloadURL are Tier 3's (download_test.go); what is
	// permanently unsupported is what a published file has no concept of.
	for name, call := range map[string]func() error{
		"Search": func() error { _, err := src.Search(context.Background(), source.SearchQuery{}); return err },
		"GetDependencies": func() error {
			_, err := src.GetDependencies(context.Background(), nil)
			return err
		},
		"ExchangeToken": func() error { _, err := src.ExchangeToken(context.Background(), "c"); return err },
	} {
		t.Run(name+" is not supported", func(t *testing.T) {
			require.ErrorIs(t, call(), source.ErrNotSupported)
		})
	}
}

// TestNoTestReachesTheProductionAPI is the design's "a package test asserts
// none does" guard: every test in this package must point the client at an
// httptest server. A literal production host anywhere in a _test.go file
// means some test is one misconfiguration away from calling Valve.
func TestNoTestReachesTheProductionAPI(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		// Split literals so this guard's own list is not a match.
		for _, host := range []string{"api.steam" + "powered.com", "steamcommunity.com" + "/dev", "steam" + "cdn"} {
			assert.NotContains(t, string(data), host,
				"%s names a live Steam host: tests must use an httptest server", e.Name())
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
