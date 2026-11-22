package serve_test

// Issue 672's web half: a check that answered mods from what lmm recorded
// the last time - because their source said they had not changed - says so
// in the same sentence the CLI prints, on the Updates line and in the Health
// card's coverage; and Re-verify (?refresh=1) asks every mod again.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// trackingFakeSource is fakeSource with a change signal: every mod is
// unchanged, and every mod its update check looks at is reported checked.
type trackingFakeSource struct {
	*fakeSource
}

func (s trackingFakeSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	updates, err := s.fakeSource.CheckUpdates(ctx, installed)
	for _, mod := range installed {
		source.ReportModChecked(ctx, mod.ID)
	}
	return updates, err
}

func (trackingFakeSource) UnchangedSince(_ context.Context, _ string, since map[string]time.Time) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(since))
	for id := range since {
		out[id] = time.Now()
	}
	return out, nil
}

// newUnchangedFixture is one installed mod, up to date at 1.0, whose
// update and version lmm has already checked once each, and a verify that
// then answered it from the record - with no checksum recorded, so the
// Health card has a finding to show and its coverage line with it.
func newUnchangedFixture(t *testing.T) (*core.Service, *domain.Game, *fakeSource) {
	t.Helper()
	sandboxE2EEnv(t)
	inner := newFakeSource("fake")
	inner.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "keep", SourceID: "fake", Name: "Keep Me", Version: "1.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "1.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, trackingFakeSource{inner})
	ctx := t.Context()
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "fake", ModID: "keep", Version: "1.0"}, "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "keep", SourceID: "fake", Name: "Keep Me", Version: "1.0", GameID: game.ID},
		ProfileName: "default", Enabled: true, FileIDs: []string{"f1"}, UpdatePolicy: domain.UpdateNotify,
	}))

	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	_, err = svc.CheckGameUpdateReport(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	for range 2 { // the first confirms the version; the second answers from it, and is the memo
		_, err = svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull, Force: true}, nil)
		require.NoError(t, err)
	}
	return svc, game, inner
}

func TestE2E_UnchangedModsAreSaidOnTheUpdatesLineAndTheHealthCard(t *testing.T) {
	svc, game, _ := newUnchangedFixture(t)
	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	var updatesLine, coverage string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="updates-unchanged"]`, chromedp.ByQuery),
		textContent(`[data-testid="updates-unchanged"]`, &updatesLine),
		chromedp.WaitVisible(`[data-testid="health-coverage"]`, chromedp.ByQuery),
		textContent(`[data-testid="health-coverage"]`, &coverage),
	)

	// The CLI's words (core.UnchangedSummary), on both cards.
	assert.Contains(t, updatesLine, core.UnchangedSummary(1)+". Check for updates asks about every mod.")
	assert.Contains(t, coverage, core.UnchangedSummary(1))
	assert.Empty(t, f.BrowserErrors())
}

// TestAPIHealth_RefreshAsksEveryModsSourceAgain: the plain forced read
// answers a confirmed, unchanged mod from its record; ?refresh=1 - what
// Re-verify sends - fetches its file list again.
func TestAPIHealth_RefreshAsksEveryModsSourceAgain(t *testing.T) {
	svc, game, inner := newUnchangedFixture(t)
	baseURL := startE2EServer(t, svc)
	get := func(query string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/health?game="+game.ID+"&profile=default"+query, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	before := inner.getModFilesCalls()
	get("&force=1")
	assert.Equal(t, before, inner.getModFilesCalls(), "confirmed and unchanged: not asked")
	get("&force=1&refresh=1")
	assert.Equal(t, before+1, inner.getModFilesCalls(), "Re-verify asks again")
}
