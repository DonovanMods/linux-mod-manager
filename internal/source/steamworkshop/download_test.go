package steamworkshop_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyFixture serves the recorded legacy-UGC metadata AND the file that
// metadata's file_url points at, from one httptest server, so Path A can be
// exercised end to end without ever naming a real Valve host.
type legacyFixture struct {
	srv     *httptest.Server
	payload []byte

	mu      sync.Mutex
	methods []string // every method the CDN half was asked for
}

// legacyPayload is 18 bytes, matching the fixture's declared file_size.
const legacyPayload = "legacy ugc bytes\n\n"

func serveLegacyFixture(t *testing.T, payload string) *legacyFixture {
	t.Helper()
	fx := &legacyFixture{payload: []byte(payload)}
	fx.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ugc/") {
			fx.mu.Lock()
			fx.methods = append(fx.methods, r.Method)
			fx.mu.Unlock()
			// The real CDN 404s a HEAD; reproduce that so a probe lmm must
			// never make would fail the test that made it.
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(fx.payload)
			return
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		_, err = url.ParseQuery(string(body))
		require.NoError(t, err)
		data, err := os.ReadFile(filepath.Join("testdata", "api", "getpublishedfiledetails_legacy_fileurl.json"))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(data), "FILE_URL_PLACEHOLDER", fx.srv.URL+"/ugc/68336872/legacy.zip")))
	}))
	t.Cleanup(fx.srv.Close)
	return fx
}

// seenMethods returns the HTTP methods the CDN half was asked for.
func (f *legacyFixture) seenMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// --- Path A: the legacy file_url ---

// TestGetModFiles_DescribesTheItemAsOneDownloadableFile pins Tier 3's file
// list: a Workshop item is exactly one file, identified by its own
// published file id, sized to the byte from the API's file_size.
func TestGetModFiles_DescribesTheItemAsOneDownloadableFile(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	files, err := src.GetModFiles(context.Background(), &domain.Mod{ID: "3617086610", GameID: "1133870"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "3617086610", files[0].ID)
	assert.True(t, files[0].IsPrimary)
	assert.Equal(t, int64(572330), files[0].Size)
	assert.Equal(t, "7987119735124793734", files[0].Version)
}

// TestGetDownloadURL_ReturnsTheLegacyFileURLWhenValveServesOne is Path A:
// an old UGC-era item Valve still publishes a plain URL for takes the
// ordinary download path, with no external tool anywhere near it.
func TestGetDownloadURL_ReturnsTheLegacyFileURLWhenValveServesOne(t *testing.T) {
	fx := serveLegacyFixture(t, legacyPayload)
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	url, err := src.GetDownloadURL(context.Background(), &domain.Mod{ID: "68336872", GameID: "72850"}, "68336872")
	require.NoError(t, err)
	assert.Equal(t, fx.srv.URL+"/ugc/68336872/legacy.zip", url)
}

// TestGetDownloadURL_ReportsNotSupportedForAModernItem is the other half:
// a modern item's file_url is empty, and saying ErrNotSupported is what
// sends core to the Fetcher seam instead of to a blank URL.
func TestGetDownloadURL_ReportsNotSupportedForAModernItem(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	_, err := src.GetDownloadURL(context.Background(), &domain.Mod{ID: "3617086610", GameID: "1133870"}, "3617086610")
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

// TestGetDownloadURL_NeverProbesTheCDNWithHEAD is a constraint, not a
// behaviour: the download CDN 404s a HEAD, so a source that "checked" a
// URL before returning it would break every legacy item.
func TestGetDownloadURL_NeverProbesTheCDNWithHEAD(t *testing.T) {
	fx := serveLegacyFixture(t, legacyPayload)
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	_, err := src.GetDownloadURL(context.Background(), &domain.Mod{ID: "68336872", GameID: "72850"}, "68336872")
	require.NoError(t, err)
	assert.Empty(t, fx.seenMethods(), "resolving a download URL must not touch the CDN at all")
}

// TestExactFileSizes_IsDeclared is what makes the API's file_size a real
// integrity check in core rather than a decoration: the Workshop's sizes
// are exact, and there is no checksum to fall back on.
func TestExactFileSizes_IsDeclared(t *testing.T) {
	src := newTestSource(t, "http://127.0.0.1:0", t.TempDir(), nil)
	sizer, ok := any(src).(source.ExactFileSizer)
	require.True(t, ok, "the Workshop source must declare its sizes exact")
	assert.True(t, sizer.ExactFileSizes())
}

// --- Path B: anonymous steamcmd ---

// fetchTo runs Fetch into a fresh directory, returning the path and error.
func fetchTo(t *testing.T, src *steamworkshop.Source, appID, fileID string) (string, string, error) {
	t.Helper()
	dest := t.TempDir()
	path, err := src.Fetch(context.Background(),
		&domain.Mod{ID: fileID, GameID: appID}, fileID, dest,
		func(string, string, int64) {})
	return dest, path, err
}

// newSteamcmdSource is a source whose cache dir is the isolated steamcmd
// home's parent, with the fake tool on PATH.
func newSteamcmdSource(t *testing.T) (*steamworkshop.Source, string) {
	t.Helper()
	testutil.FakeSteamcmdOnPath(t)
	cacheDir := t.TempDir()
	return newTestSource(t, "http://127.0.0.1:0", cacheDir, nil), cacheDir
}

// TestFetch_AnonymousDownloadLandsInTheStagingDirectory is Path B's happy
// path, and it also proves the two traps the #268 spike found: the tool is
// invoked with +force_install_dir pinned to the directory core handed over
// (the fake exits 3 if it is missing, 4 if it comes after +login), and with
// HOME pointed at lmm's own persistent steamcmd home (exit 5 otherwise).
func TestFetch_AnonymousDownloadLandsInTheStagingDirectory(t *testing.T) {
	src, cacheDir := newSteamcmdSource(t)

	dest, path, err := fetchTo(t, src, "1133870", "3000000001")
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dest, "steamapps", "workshop", "content", "1133870", "3000000001"), path)
	assert.FileExists(t, filepath.Join(path, "mod.txt"))
	assert.DirExists(t, filepath.Join(cacheDir, "_steamworkshop", "steamcmd-home"),
		"the isolated steamcmd home is persistent, under the cache dir")
}

// TestFetch_ReportsProgressFromTheToolsOwnOutput pins half the readout:
// every "Update state ... progress:" line the tool does print becomes a
// tick, bracketed by a started and a done.
func TestFetch_ReportsProgressFromTheToolsOwnOutput(t *testing.T) {
	src, _ := newSteamcmdSource(t)

	var mu sync.Mutex
	var phases []string
	var details []string
	dest := t.TempDir()
	_, err := src.Fetch(context.Background(), &domain.Mod{ID: "3000000001", GameID: "1133870"}, "3000000001", dest,
		func(phase, detail string, _ int64) {
			mu.Lock()
			defer mu.Unlock()
			phases = append(phases, phase)
			details = append(details, detail)
		})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, phases)
	assert.Equal(t, source.FetchPhaseStarted, phases[0])
	assert.Equal(t, source.FetchPhaseDone, phases[len(phases)-1])
	assert.Contains(t, phases, source.FetchPhaseProgress)
	assert.True(t, strings.Contains(strings.Join(details, "\n"), "78.90"),
		"the tool's own progress lines must reach the reader: %v", details)
}

// TestFetch_AnonymousRefusalIsTheTier1Fallback is the ruling's most
// important error: the publisher does not allow anonymous downloads, and
// the only thing lmm can honestly tell the user is to subscribe in Steam
// and let Tier 1 track it in place.
func TestFetch_AnonymousRefusalIsTheTier1Fallback(t *testing.T) {
	src, _ := newSteamcmdSource(t)

	_, _, err := fetchTo(t, src, "431960", "3000000002")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrWorkshopAnonymousRefused))

	var failure *domain.WorkshopFetchFailure
	require.True(t, errors.As(err, &failure))
	assert.Equal(t, "431960", failure.AppID)
	assert.Equal(t, "3000000002", failure.PublishedFileID)
	assert.Equal(t, "steamcmd", failure.Tool)
	assert.Contains(t, failure.Reason, "lmm import --workshop")
	assert.Contains(t, failure.Reason, "Subscribe")
}

// TestFetch_AccessDeniedIsTheUnavailableItem maps steamcmd's other named
// refusal onto the SAME sentinel the Web API's result 9 uses: an item that
// is delisted, deleted or private is one fact, however lmm learns it.
func TestFetch_AccessDeniedIsTheUnavailableItem(t *testing.T) {
	src, _ := newSteamcmdSource(t)

	_, _, err := fetchTo(t, src, "1133870", "3000000003")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrWorkshopItemUnavailable))
	assert.True(t, errors.Is(err, steamworkshop.ErrItemUnavailable),
		"steamcmd's (Access Denied) and the API's result 9 are the same sentinel")
}

// TestFetch_UnclassifiedFailureCarriesTheOutputTail is the honest fallback
// for a tool failure lmm has no name for: report what the tool said rather
// than invent a diagnosis.
func TestFetch_UnclassifiedFailureCarriesTheOutputTail(t *testing.T) {
	src, _ := newSteamcmdSource(t)

	_, _, err := fetchTo(t, src, "1133870", "3000000009")
	require.Error(t, err)
	assert.False(t, errors.Is(err, domain.ErrWorkshopAnonymousRefused))

	var failure *domain.WorkshopFetchFailure
	require.True(t, errors.As(err, &failure))
	assert.Contains(t, failure.OutputTail, "something went wrong that lmm has no name for")
}

// TestFetch_MissingToolIsReportedWithItsInstallHint pins the runtime
// probe: steamcmd is never vendored and never auto-installed, so "not
// installed" is a first-class, actionable answer.
func TestFetch_MissingToolIsReportedWithItsInstallHint(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	src := newTestSource(t, "http://127.0.0.1:0", t.TempDir(), nil)

	_, _, err := fetchTo(t, src, "1133870", "3000000001")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrExternalToolMissing))

	var failure *domain.WorkshopFetchFailure
	require.True(t, errors.As(err, &failure))
	assert.Equal(t, "steamcmd", failure.Tool)
	assert.Contains(t, failure.Reason, "SteamCMD")
}

// TestFetch_RefusesWithoutASteamAppID guards the one input lmm cannot
// invent: without the app id there is no download to ask for, and guessing
// one would point steamcmd at the wrong game.
func TestFetch_RefusesWithoutASteamAppID(t *testing.T) {
	src, _ := newSteamcmdSource(t)

	dest := t.TempDir()
	_, err := src.Fetch(context.Background(), &domain.Mod{ID: "3000000001"}, "3000000001", dest, func(string, string, int64) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app id")
}

// TestFetch_HeartbeatsWhileTheToolSaysNothing pins the other half, and the
// one that actually matters: steamcmd's output is not a dependable
// progress stream, so lmm ticks on a timer with the elapsed time and the
// bytes on disk. A silent multi-gigabyte download must still visibly move.
func TestFetch_HeartbeatsWhileTheToolSaysNothing(t *testing.T) {
	src, _ := newSteamcmdSource(t)
	steamworkshop.SetHeartbeatForTest(t, 20*time.Millisecond)

	var mu sync.Mutex
	var beats []string
	var beatBytes []int64
	dest := t.TempDir()
	_, err := src.Fetch(context.Background(), &domain.Mod{ID: "3000000005", GameID: "1133870"}, "3000000005", dest,
		func(phase, detail string, bytes int64) {
			if phase != source.FetchPhaseProgress {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			beats = append(beats, detail)
			beatBytes = append(beatBytes, bytes)
		})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, beats, "a tool that prints no progress must still produce heartbeats")
	assert.Contains(t, beats[len(beats)-1], "still downloading item 3000000005")
	assert.Contains(t, beats[len(beats)-1], "after")
	assert.Greater(t, beatBytes[len(beatBytes)-1], int64(0), "the heartbeat reports bytes on disk")
}
