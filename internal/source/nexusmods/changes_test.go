package nexusmods

// #672: NexusMods answers "which of these mods changed since lmm last
// checked them" from its updated-mods listing - one request per game - and,
// for a mod last checked longer ago than the longest listing covers, from
// the mod's own updated time, which still saves the file list.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// changesNow is the fake clock's reading in these tests.
var changesNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// changesServer answers updated.json with listed (mod ID -> latest
// activity) and a mod's own document with modUpdated, recording every path
// asked.
type changesServer struct {
	mu         sync.Mutex
	paths      []string
	listed     map[int]time.Time
	modUpdated map[int]time.Time
}

func (c *changesServer) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func newChangesSource(t *testing.T, c *changesServer) *NexusMods {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.paths = append(c.paths, r.URL.Path+"?"+r.URL.RawQuery)
		c.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/mods/updated.json"):
			var rows []string
			for id, at := range c.listed {
				rows = append(rows, fmt.Sprintf(`{"mod_id":%d,"latest_file_update":%d,"latest_mod_activity":%d}`, id, at.Add(-time.Hour).Unix(), at.Unix()))
			}
			_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
		case strings.Contains(r.URL.Path, "/mods/") && strings.HasSuffix(r.URL.Path, ".json"):
			var id int
			_, _ = fmt.Sscanf(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "%d.json", &id)
			at, ok := c.modUpdated[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"No Mod Found"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"mod_id":%d,"name":"Mod %d","version":"1.0","status":"published","updated_time":%q}`, id, id, at.Format(time.RFC3339))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	nm := New(srv.Client(), "key")
	nm.client.SetBaseURL(srv.URL)
	nm.client.now = func() time.Time { return changesNow }
	return nm
}

func TestUnchangedSince_OneListingAnswersTheGame(t *testing.T) {
	c := &changesServer{listed: map[int]time.Time{
		1: changesNow.Add(-time.Hour),     // changed after its check two hours ago
		2: changesNow.Add(-5 * time.Hour), // listed, but changed before its check
	}}
	nm := newChangesSource(t, c)
	since := map[string]time.Time{
		"1": changesNow.Add(-2 * time.Hour),
		"2": changesNow.Add(-2 * time.Hour),
		"3": changesNow.Add(-3 * time.Hour), // not listed: unchanged all day
	}

	got, err := nm.UnchangedSince(context.Background(), "cyberpunk2077", since)
	require.NoError(t, err)
	assert.NotContains(t, got, "1", "a mod active since its check is checked in full")
	assert.Contains(t, got, "2")
	assert.Contains(t, got, "3")
	assert.Equal(t, []string{"/v1/games/cyberpunk2077/mods/updated.json?period=1d"}, c.asked(), "one request for the whole game")
	assert.Equal(t, changesNow.Add(-listingStaleness), got["3"],
		"vouched for up to when the listing - which NexusMods caches - can be trusted")
}

func TestUnchangedSince_ThePeriodCoversTheOldestCheck(t *testing.T) {
	for _, tc := range []struct {
		age    time.Duration
		period string
	}{
		{20 * time.Hour, "1d"},
		{3 * 24 * time.Hour, "1w"},
		{20 * 24 * time.Hour, "1m"},
	} {
		c := &changesServer{}
		nm := newChangesSource(t, c)
		_, err := nm.UnchangedSince(context.Background(), "skyrim", map[string]time.Time{"9": changesNow.Add(-tc.age)})
		require.NoError(t, err)
		assert.Equal(t, []string{"/v1/games/skyrim/mods/updated.json?period=" + tc.period}, c.asked(), "%s", tc.age)
	}
}

// TestUnchangedSince_AnOldCheckIsCheckedInFull: a mod last checked before
// any listing reaches is not answered - and so is checked in full, as
// Vortex does - because the mod's own updated_time does not follow every
// change (#672 review spike: it moves with a file upload, not with later
// page activity).
func TestUnchangedSince_AnOldCheckIsCheckedInFull(t *testing.T) {
	c := &changesServer{modUpdated: map[int]time.Time{4: changesNow.Add(-60 * 24 * time.Hour)}}
	nm := newChangesSource(t, c)

	got, err := nm.UnchangedSince(context.Background(), "skyrim", map[string]time.Time{"4": changesNow.Add(-40 * 24 * time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, c.asked(), "no listing reaches back forty days, and the mod's own page is no answer")
}

func TestUnchangedSince_OnlyTheRecentChecksShareTheListing(t *testing.T) {
	c := &changesServer{}
	nm := newChangesSource(t, c)
	got, err := nm.UnchangedSince(context.Background(), "skyrim", map[string]time.Time{
		"1": changesNow.Add(-2 * time.Hour),
		"2": changesNow.Add(-40 * 24 * time.Hour),
	})
	require.NoError(t, err)
	assert.Contains(t, got, "1")
	assert.NotContains(t, got, "2")
	assert.Equal(t, []string{"/v1/games/skyrim/mods/updated.json?period=1d"}, c.asked(),
		"the period is the recent check's, not stretched to the old one")
}

func TestUnchangedSince_AFailedListingAnswersNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	nm := New(srv.Client(), "key")
	nm.client.SetBaseURL(srv.URL)
	nm.client.now = func() time.Time { return changesNow }

	got, err := nm.UnchangedSince(context.Background(), "skyrim", map[string]time.Time{"1": changesNow.Add(-time.Hour)})
	require.NoError(t, err, "a cache that cannot answer only costs the full check")
	assert.Empty(t, got)
}

func TestCheckUpdates_ReportsEachModItFinished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/mods/1.json"):
			_, _ = w.Write([]byte(`{"mod_id":1,"name":"One","version":"1.0","status":"published"}`))
		case strings.HasSuffix(r.URL.Path, "/mods/1/files.json"):
			_, _ = w.Write([]byte(`{"files":[{"file_id":7,"version":"1.0","is_primary":true}],"file_updates":[]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	nm := New(srv.Client(), "key")
	nm.client.SetBaseURL(srv.URL)

	var checked []string
	ctx := source.WithModCheckedObserver(context.Background(), func(id string) { checked = append(checked, id) })
	_, err := nm.CheckUpdates(ctx, []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "One", Version: "1.0", GameID: "skyrim"}, FileIDs: []string{"7"}},
		{Mod: domain.Mod{ID: "2", Name: "Two", Version: "1.0", GameID: "skyrim"}, FileIDs: []string{"8"}},
	})
	require.Error(t, err, "mod 2's check failed")
	assert.Equal(t, []string{"1"}, checked, "only a mod checked without error has an answer to record")
}

var _ source.ChangeTracker = (*NexusMods)(nil)
