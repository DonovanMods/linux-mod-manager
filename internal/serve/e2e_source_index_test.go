package serve_test

// The browser half of issues 410, 423 and 436: the Setup page's local
// search indexes (their rows, Refresh and the prune), a throttled download
// reported on its job, a loader refusal's setup steps, and the search
// page's one-time index build notice. Each is a claim about what a BROWSER
// renders from a document the /api/v1 suites already pin.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const e2eIndexSourceID = "thunderstore"

// e2eIndexSource is a Thunderstore-shaped double over the search fixture: a
// local index per community (an in-memory "disk"), a slug-shaped
// identifier, and three switches a scenario flips - a loader requirement on
// every package, a download that reports a throttle and waits to be let go,
// and a search that waits to be let go.
type e2eIndexSource struct {
	*e2eSearchSource

	mu          sync.Mutex
	cached      map[string]source.CachedIndex
	removed     []string
	removeErr   map[string]error
	needsLoader bool
	// throttle, when non-nil, makes GetDownloadURL report a rate-limited
	// retry through the call's context and then wait for it to close.
	throttle chan struct{}
	// searchGate, when non-nil, makes Search wait for it to close - the
	// cold build a first search performs, held open for the browser to see.
	searchGate chan struct{}
	// holds is what Holds(ctx) reports in force (source.HoldReporter,
	// #436) - a scenario sets it before the server starts, the same way it
	// sets throttle/searchGate.
	holds []source.Hold
	// searchFailure, when non-nil, is returned by Search unconditionally -
	// the shape of a source refusing to be asked at all (a *source.
	// RetryLaterError wrapped the way thunderstore/index.go's own
	// indexUnavailable wraps one).
	searchFailure error
}

func (s *e2eIndexSource) Name() string                { return "Thunderstore" }
func (s *e2eIndexSource) IgnoresGameIdentifier() bool { return false }

func (s *e2eIndexSource) ValidateGameIdentifier(id string) error {
	if id == "" || strings.ToLower(id) != id {
		return fmt.Errorf("%q is not a community slug: %w", id, source.ErrGameIdentifierInvalid)
	}
	return nil
}

func (s *e2eIndexSource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	if s.searchFailure != nil {
		return source.SearchResult{}, s.searchFailure
	}
	if s.searchGate != nil {
		select {
		case <-s.searchGate:
		case <-ctx.Done():
			return source.SearchResult{}, ctx.Err()
		}
		s.mu.Lock()
		s.cached[q.GameID] = source.CachedIndex{GameID: q.GameID, Present: true, Packages: 3, Bytes: 2048, FetchedAt: time.Now(), Removable: true}
		s.mu.Unlock()
	}
	return s.e2eSearchSource.Search(ctx, q)
}

func (s *e2eIndexSource) LoaderRequirement(context.Context, *domain.Mod) (string, string, string, bool, error) {
	if !s.needsLoader {
		return "", "", "", false, nil
	}
	return domain.LoaderKindBepInEx, "5.4.2100", "BepInEx-BepInExPack-5.4.2100", true, nil
}

func (s *e2eIndexSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if s.throttle != nil {
		source.Notify(ctx, source.Notice{
			Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryRateLimited,
			Status: 429, Attempt: 2, MaxAttempts: 3, Wait: 12 * time.Second,
		})
		select {
		case <-s.throttle:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.e2eSearchSource.GetDownloadURL(ctx, mod, fileID)
}

func (s *e2eIndexSource) IndexStatus(_ context.Context, id string) (source.IndexStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ci, ok := s.cached[id]
	if !ok {
		return source.IndexStatus{GameID: id}, nil
	}
	return source.IndexStatus{GameID: id, Present: ci.Present, Packages: ci.Packages, FetchedAt: ci.FetchedAt, Bytes: ci.Bytes}, nil
}

func (s *e2eIndexSource) RefreshIndex(_ context.Context, id string, _ bool, progress source.IndexProgressFunc) (source.IndexStatus, error) {
	progress(source.FetchPhaseStarted, "fetching", 0)
	s.mu.Lock()
	s.cached[id] = source.CachedIndex{GameID: id, Present: true, Packages: 50707, Bytes: 239075328, FetchedAt: time.Now(), Removable: true}
	s.mu.Unlock()
	progress(source.FetchPhaseDone, "done", 0)
	return s.IndexStatus(context.Background(), id)
}

func (s *e2eIndexSource) CachedIndexes(context.Context) ([]source.CachedIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]source.CachedIndex, 0, len(s.cached))
	for _, ci := range s.cached {
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

func (s *e2eIndexSource) RemoveIndex(_ context.Context, id string, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.removeErr[id]; err != nil {
		return 0, err
	}
	ci, ok := s.cached[id]
	if !ok {
		return 0, nil
	}
	delete(s.cached, id)
	s.removed = append(s.removed, id)
	return ci.Bytes, nil
}

func (s *e2eIndexSource) removedIndexes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removed...)
}

// Holds implements source.HoldReporter (#436): the holds the scenario
// configured, in force from server start - unlike throttle/searchGate,
// nothing in these tests trips a hold live, so there is no lock dance
// needed beyond what mu already gives every other field here.
func (s *e2eIndexSource) Holds(context.Context) []source.Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]source.Hold(nil), s.holds...)
}

var (
	_ source.LocalIndexSource        = (*e2eIndexSource)(nil)
	_ source.IndexInventory          = (*e2eIndexSource)(nil)
	_ source.LoaderRequirer          = (*e2eIndexSource)(nil)
	_ source.GameIdentifierValidator = (*e2eIndexSource)(nil)
	_ source.HoldReporter            = (*e2eIndexSource)(nil)
)

// e2eIndexModID is the one package the index scenarios install.
const e2eIndexModID = "RugbugRedfern-Skinwalkers"

// newE2EIndexFixture seeds a game ("lethal") mapping the index source to
// "lethal-company", with that index and one no game uses already on disk,
// and one installable package. configure flips the scenario's switches
// before the server starts.
func newE2EIndexFixture(t *testing.T, configure func(*e2eIndexSource)) (e2eFixture, *e2eIndexSource) {
	t.Helper()
	sandboxE2EEnv(t)

	base := newE2ESearchSource(t, e2eIndexSourceID)
	base.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: e2eIndexModID, SourceID: e2eIndexSourceID, Name: "Skinwalkers", Version: "3.0.2"},
		files:   []domain.DownloadableFile{{ID: "3.0.2", Name: "Skinwalkers 3.0.2", FileName: "skinwalkers.zip", Version: "3.0.2", Category: "MAIN", IsPrimary: true}},
		members: map[string]string{"3.0.2": "Mods/skinwalkers.pak"},
	})
	fetched := time.Now().Add(-2 * time.Hour)
	src := &e2eIndexSource{
		e2eSearchSource: base,
		cached: map[string]source.CachedIndex{
			"lethal-company":  {GameID: "lethal-company", Present: true, Packages: 12, Bytes: 4096, FetchedAt: fetched, Removable: true},
			"content-warning": {GameID: "content-warning", Present: true, Packages: 900, Bytes: 4096000, FetchedAt: fetched, Removable: true},
		},
	}
	if configure != nil {
		configure(src)
	}

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{e2eIndexSourceID: "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
		BrowserErrors: browserErrors,
	}, src
}

func indexRow(game string) string {
	return fmt.Sprintf(`[data-testid="source-indexes"] tr[data-index=%q]`, game)
}

// TestE2E_SetupSources_IndexRowsRefreshAndPrune is the Setup page's index
// section end to end: both indexes are listed with their footprint and the
// game that uses one; Refresh rebuilds the used one and the row re-reads;
// the prune previews only the unused index, and confirming removes exactly
// that one.
func TestE2E_SetupSources_IndexRowsRefreshAndPrune(t *testing.T) {
	f, src := newE2EIndexFixture(t, nil)

	var usedRow, unusedRow, outcome, packagesAfter, preview, total string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(`[data-testid="source-indexes"]`, chromedp.ByQuery),
		textContent(indexRow("lethal-company"), &usedRow),
		textContent(indexRow("content-warning"), &unusedRow),
		chromedp.WaitNotPresent(indexRow("content-warning")+` [data-action="refresh-index"]`, chromedp.ByQuery),

		clickWhenSettled(indexRow("lethal-company")+` [data-action="refresh-index"]`),
		chromedp.WaitVisible(indexRow("lethal-company")+` [data-testid="index-outcome"]`, chromedp.ByQuery),
		textContent(indexRow("lethal-company")+` [data-testid="index-outcome"]`, &outcome),
		pollUntil(fmt.Sprintf(`(document.querySelector(%q)?.textContent ?? '').trim() === '50707'`,
			indexRow("lethal-company")+` [data-testid="index-packages"]`)),
		textContent(indexRow("lethal-company")+` [data-testid="index-packages"]`, &packagesAfter),

		clickWhenSettled(`[data-action="prune-indexes"]`),
		chromedp.WaitVisible(`[data-testid="prune-preview"]`, chromedp.ByQuery),
		textContent(`[data-testid="prune-preview"]`, &preview),
		clickWhenSettled(`[data-action="confirm-prune"]`),
		chromedp.WaitVisible(`[data-testid="prune-result"]`, chromedp.ByQuery),
		waitGone(indexRow("content-warning")),
		textContent(`[data-testid="source-indexes-total"]`, &total),
	)

	assert.Contains(t, usedRow, "lethal-company")
	assert.Contains(t, usedRow, "lethal", "the game that uses it")
	assert.Contains(t, usedRow, "4.0 KB")
	assert.Contains(t, usedRow, "2 hours ago")
	assert.Contains(t, unusedRow, "3.9 MB")
	assert.Contains(t, outcome, "Updated: 50707 packages, 228 MB.")
	assert.Equal(t, "50707", strings.TrimSpace(packagesAfter))

	assert.Contains(t, preview, "Would remove 1 index(es)")
	assert.Contains(t, preview, "content-warning")
	assert.Contains(t, preview, "no game uses it")
	assert.Contains(t, preview, "used by lethal", "what is kept says why")
	assert.Equal(t, []string{"content-warning"}, src.removedIndexes(), "only what the preview showed")
	assert.Contains(t, total, "228 MB")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AThrottledDownloadSaysSoOnTheJob is issue 436's web half: a
// download that is waiting out a rate limit says so on the install's own
// progress readout, in core's sentence, while it waits - and then finishes.
func TestE2E_AThrottledDownloadSaysSoOnTheJob(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) { s.throttle = gate })

	var waiting string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Skinwalkers", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(searchResultRow(e2eIndexSourceID, e2eIndexModID), chromedp.ByQuery),
		chromedp.Click(searchResultRow(e2eIndexSourceID, e2eIndexModID)+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		pollUntil(`(document.querySelector('.job-progress__text')?.textContent ?? '').includes('Rate limited by Thunderstore')`),
		textContent(`.job-progress__text`, &waiting),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		chromedp.WaitVisible(searchResultRow(e2eIndexSourceID, e2eIndexModID)+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Contains(t, waiting, "Retrying", "the phase, humanized")
	assert.Contains(t, waiting, "Rate limited by Thunderstore; retrying in 12s (attempt 2 of 3).")
	assert.NotContains(t, waiting, "source_retrying")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ALoaderRefusalRendersItsSetupSteps is issue 423: installing a
// package that needs BepInEx into a game with no loader declared shows the
// refusal's setup steps - in order, verbatim, their commands set as code,
// and the version the package asked for - not a one-line error and a
// key/value dump.
func TestE2E_ALoaderRefusalRendersItsSetupSteps(t *testing.T) {
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) { s.needsLoader = true })

	var title string
	var steps []string
	var codes int
	var version string
	var versionOK bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Skinwalkers", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(searchResultRow(e2eIndexSourceID, e2eIndexModID), chromedp.ByQuery),
		chromedp.Click(searchResultRow(e2eIndexSourceID, e2eIndexModID)+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal [data-testid="loader-setup"]`, chromedp.ByQuery),
		textContent(`.modal .loader-setup__title`, &title),
		chromedp.Evaluate(`[...document.querySelectorAll('.modal .loader-setup__step')].map(li => li.textContent.replace(/\s+/g, ' ').trim())`, &steps),
		chromedp.Evaluate(`document.querySelectorAll('.modal .loader-setup__step code').length`, &codes),
		chromedp.AttributeValue(`.modal [data-testid="loader-setup"]`, "data-version", &version, &versionOK),
	)

	assert.Contains(t, title, "Skinwalkers needs BepInEx 5.4.2100")
	require.Len(t, steps, 3, "all three steps, in order")
	assert.Contains(t, steps[0], "This mod asks for BepInEx 5.4.2100.")
	assert.Contains(t, steps[1], "lmm game edit lethal --loader bepinex")
	assert.Contains(t, steps[2], "lmm game show lethal")
	assert.NotContains(t, strings.Join(steps, " "), "`", "the commands are code, not backticks")
	assert.GreaterOrEqual(t, codes, 3)
	assert.True(t, versionOK)
	assert.Equal(t, "5.4.2100", version)
	// A refused plan is a 409 the page reports as a failed request: the
	// console line is the browser's own, not the application's.
	for _, e := range f.BrowserErrors() {
		assert.Contains(t, e, "409", "the only browser error is the refused request itself: %s", e)
	}
}

// TestE2E_SearchPage_SaysAColdIndexIsBeingBuilt is design §2.7's approval
// note: while the first search against a community with no index is
// building one, the search page's progress area says so.
func TestE2E_SearchPage_SaysAColdIndexIsBeingBuilt(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		delete(s.cached, "lethal-company")
		s.searchGate = gate
	})

	var notice string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/search?q=Skinwalkers"),
		chromedp.WaitVisible(`[data-testid="index-notice"]`, chromedp.ByQuery),
		textContent(`[data-testid="index-notice"]`, &notice),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		chromedp.WaitVisible(searchResultRow(e2eIndexSourceID, e2eIndexModID), chromedp.ByQuery),
		waitGone(`[data-testid="index-notice"]`),
	)

	assert.Contains(t, notice, "Building the Thunderstore index for lethal-company (one-time)")
	assert.Empty(t, f.BrowserErrors())
}

// e2eRefusingFetchSource is the index source with its downloads taken
// over by a Fetcher that refuses with a loader requirement - the shape of a
// failure discovered while the job RUNS, which the browser must render on
// the failed job as it does on a failed plan.
type e2eRefusingFetchSource struct {
	*e2eIndexSource
	refusal error
}

func (s *e2eRefusingFetchSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", fmt.Errorf("source %q: downloads: %w", s.ID(), source.ErrNotSupported)
}

func (s *e2eRefusingFetchSource) Fetch(context.Context, *domain.Mod, string, string, source.FetchProgressFunc) (string, error) {
	return "", s.refusal
}

var _ source.Fetcher = (*e2eRefusingFetchSource)(nil)

// TestE2E_ALoaderRefusalOnAFailedJobRendersItsSetupSteps is issue 423's
// other surface: when the refusal is a failed JOB rather than a failed
// plan, the setup steps render on the failure where the user started the
// install - the inline readout - from the job's own error envelope.
func TestE2E_ALoaderRefusalOnAFailedJobRendersItsSetupSteps(t *testing.T) {
	f, idx := newE2EIndexFixture(t, nil)
	refusal := &core.LoaderRequiredError{
		GameID: "lethal", Kind: domain.LoaderKindBepInEx, Version: "5.4.2100", ModName: "Skinwalkers",
		Layout: "wrapped in a single directory",
		Setup: []string{
			"Install BepInEx into the game directory yourself.",
			"Record it: `lmm game edit lethal --loader bepinex`.",
			"Then `lmm verify --game lethal` checks that the loader actually ran.",
		},
	}
	// The same Service, with the download side swapped for the refusing
	// Fetcher: re-registering under the same id replaces the source.
	f.Svc.RegisterSource(&e2eRefusingFetchSource{e2eIndexSource: idx, refusal: refusal})

	var steps []string
	var failure string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Skinwalkers", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(searchResultRow(e2eIndexSourceID, e2eIndexModID), chromedp.ByQuery),
		chromedp.Click(searchResultRow(e2eIndexSourceID, e2eIndexModID)+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="failed"] [data-testid="loader-setup"]`, chromedp.ByQuery),
		textContent(`.job-progress[data-state="failed"] .job-progress__text`, &failure),
		chromedp.Evaluate(`[...document.querySelectorAll('.job-progress .loader-setup__step')].map(li => li.textContent.replace(/\s+/g, ' ').trim())`, &steps),
	)

	assert.Contains(t, failure, "needs the BepInEx mod loader")
	require.Len(t, steps, 3)
	assert.Equal(t, "Record it: lmm game edit lethal --loader bepinex.", steps[1])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupSources_APruneThatCouldNotRemoveSaysWhy: a removal the
// source refused is shown with its reason after the prune - a prune that
// removed nothing must never read as one that had nothing to do - and the
// "also remove indexes in use" choice does not carry into the next prune.
func TestE2E_SetupSources_APruneThatCouldNotRemoveSaysWhy(t *testing.T) {
	f, src := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		s.removeErr = map[string]error{"content-warning": fmt.Errorf("the content-warning index directory holds notes.txt, which is not part of an index")}
	})

	var problems string
	var allChecked bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(`[data-testid="source-indexes"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="prune-indexes"]`),
		chromedp.WaitVisible(`[data-testid="prune-preview"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="prune-all"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll('[data-testid="prune-preview"] [data-prune]').length === 2`),
		clickWhenSettled(`[data-action="confirm-prune"]`),
		chromedp.WaitVisible(`[data-testid="prune-problems"]`, chromedp.ByQuery),
		textContent(`[data-testid="prune-problems"]`, &problems),
		clickWhenSettled(`[data-action="prune-indexes"]`),
		chromedp.WaitVisible(`[data-testid="prune-preview"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('input[name="prune-all"]').checked`, &allChecked),
	)

	assert.Contains(t, problems, "content-warning")
	assert.Contains(t, problems, "notes.txt")
	assert.Equal(t, []string{"lethal-company"}, src.removedIndexes())
	assert.False(t, allChecked, "the next prune starts from the safe default")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupSources_ShowsTheHoldsInForce is #436's T3 review F10 on the
// Setup page: a hold on the whole source and a hold on one index are both
// shown WITHOUT a request being made - "not asking ... again until ..." -
// and the one-index hold also marks its own row, while an index the hold
// does not name gets no marker at all.
func TestE2E_SetupSources_ShowsTheHoldsInForce(t *testing.T) {
	hostUntil := time.Now().Add(9*time.Minute + 20*time.Second)
	indexUntil := time.Now().Add(4 * time.Minute)
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		s.holds = []source.Hold{
			{
				Until:  hostUntil,
				Reason: "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s",
			},
			{
				GameID: "content-warning",
				Until:  indexUntil,
				Reason: "suspended after 3 failed requests in a row; the last: Thunderstore has no package index at /c/content-warning/api/v1/package/ (HTTP 404)",
			},
		}
	})

	var holds, rowMarker string
	var lethalMarkers int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(`[data-testid="source-index-holds"]`, chromedp.ByQuery),
		textContent(`[data-testid="source-index-holds"]`, &holds),
		chromedp.WaitVisible(indexRow("content-warning")+` [data-testid="index-held-until"]`, chromedp.ByQuery),
		textContent(indexRow("content-warning")+` [data-testid="index-held-until"]`, &rowMarker),
		chromedp.Evaluate(
			fmt.Sprintf(`document.querySelectorAll(%q).length`, indexRow("lethal-company")+` [data-testid="index-held-until"]`),
			&lethalMarkers,
		),
	)

	assert.Contains(t, holds, "Thunderstore", "the source's display name, not its bare id")
	assert.Contains(t, holds, "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s")
	assert.Contains(t, holds, "content-warning", "the one-index hold names its index")
	assert.Contains(t, holds, "suspended after 3 failed requests in a row")
	assert.Contains(t, rowMarker, "held until")
	assert.Zero(t, lethalMarkers, "only the index the hold names gets a row marker")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupSources_ARowAPruneWouldKeepSaysWhy is T3 review F7: an index
// the source cannot prove is only its own (here, a stray notes.txt) shows
// why a prune would keep it whatever its use or age, right on its row - not
// only inside a prune preview the user has to open first.
func TestE2E_SetupSources_ARowAPruneWouldKeepSaysWhy(t *testing.T) {
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		s.cached["old-mod-pack"] = source.CachedIndex{
			GameID:    "old-mod-pack",
			Bytes:     5120,
			Removable: false,
			Reason:    "the old-mod-pack index directory holds notes.txt, which is not part of an index",
		}
	})

	var reason string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(indexRow("old-mod-pack")+` [data-testid="index-keep-reason"]`, chromedp.ByQuery),
		textContent(indexRow("old-mod-pack")+` [data-testid="index-keep-reason"]`, &reason),
	)

	assert.Contains(t, reason, "Prune keeps this:")
	assert.Contains(t, reason, "notes.txt")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupSources_AConfirmedPruneThatKeptAnIndexSaysWhy is F11: the
// preview said "content-warning" would be removed, but between the preview
// and the confirm a game starts mapping it - the honest case where the
// confirmed run removes zero rather than the one the preview promised. The
// result must name the index and say why, not simply read as a prune that
// removed everything it said it would.
func TestE2E_SetupSources_AConfirmedPruneThatKeptAnIndexSaysWhy(t *testing.T) {
	f, src := newE2EIndexFixture(t, nil)

	var problems string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(`[data-testid="source-indexes"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="prune-indexes"]`),
		chromedp.WaitVisible(`[data-testid="prune-preview"] [data-prune="content-warning"]`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error {
			other := &domain.Game{
				ID: "other", Name: "Other Game",
				InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
				SourceIDs: map[string]string{e2eIndexSourceID: "content-warning"},
			}
			return f.Svc.SaveGame(context.Background(), other)
		}),
		clickWhenSettled(`[data-action="confirm-prune"]`),
		chromedp.WaitVisible(`[data-testid="prune-problems"] [data-kept-anyway="content-warning"]`, chromedp.ByQuery),
		textContent(`[data-testid="prune-problems"]`, &problems),
	)

	assert.Contains(t, problems, "content-warning")
	assert.Contains(t, problems, "was kept")
	assert.Contains(t, problems, "used by other")
	assert.Empty(t, src.removedIndexes(), "the preview's promise did not hold, so nothing was removed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchPage_AnIndexFailureShowsWhenToRetry is #423/#436 on the
// dedicated search page: a source that will not be asked before its own
// retry_at answers a search 502 with a *core.IndexUnavailableError, and the
// page must show it through ErrorDetails (errordetails.js) - the same
// component confirmplan.js already renders a plan refusal's details
// through - rather than only the raw error sentence.
func TestE2E_SearchPage_AnIndexFailureShowsWhenToRetry(t *testing.T) {
	// Seconds pinned away from a minute boundary (30, not whatever time.Now()
	// happens to carry): classifyIndexError rounds RetryAt UP to the next
	// whole second, which must never carry into the following minute and
	// make the "HH:MM" assertion below flaky.
	now := time.Now()
	until := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute()+10, 30, 0, now.Location())
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		retry := &source.RetryLaterError{
			Source: "Thunderstore",
			Until:  until,
			Reason: "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s",
		}
		// The exact wrap thunderstore/index.go's own indexUnavailable
		// produces for a cause that already IS the sentinel (RetryLaterError.Is
		// makes errors.Is(err, ErrIndexUnavailable) true on its own): a single
		// %w, not the double-%w join used for a cause that is not already the
		// sentinel.
		s.searchFailure = fmt.Errorf("source %q: the %s index could not be built: %w", e2eIndexSourceID, "lethal-company", retry)
	})

	var message, retryAt string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/search?q=Skinwalkers"),
		chromedp.WaitVisible(`.app-error`, chromedp.ByQuery),
		textContent(`.app-error`, &message),
		chromedp.WaitVisible(`[data-testid="retry-at"]`, chromedp.ByQuery),
		textContent(`[data-testid="retry-at"]`, &retryAt),
	)

	assert.Contains(t, message, "rate limited by Thunderstore (HTTP 429)")
	// ErrorDetails/retryAtFor (failures.js) names the source by its wire id
	// (core.IndexUnavailableError.Source, always the lowercase registry id)
	// - the same convention every other reader of that field follows - so
	// the capitalised "Thunderstore" a person reads comes from the message
	// line above, not this one.
	assert.Contains(t, retryAt, e2eIndexSourceID)
	assert.Contains(t, retryAt, until.Local().Format("3:04"), "a clock time close enough to be recognisable")
	// A refused search is a 502 the page reports as a failed request: the
	// console line is the browser's own, not the application's (mirrors
	// TestE2E_ALoaderRefusalRendersItsSetupSteps's own 409 case).
	for _, e := range f.BrowserErrors() {
		assert.Contains(t, e, "502", "the only browser error is the refused request itself: %s", e)
	}
}

// TestE2E_SetupSources_AnEmptyOrUnusableIndexSaysSo: a directory a failed
// cold build left empty is "not built yet", and one holding an index lmm
// cannot use (an older lmm's, or a damaged one) says so - the CLI's two
// readings of the same rows (T3 review F12, and the fix round's
// real-binary run).
func TestE2E_SetupSources_AnEmptyOrUnusableIndexSaysSo(t *testing.T) {
	f, _ := newE2EIndexFixture(t, func(s *e2eIndexSource) {
		s.cached["empty-dir"] = source.CachedIndex{GameID: "empty-dir", Removable: true}
		s.cached["old-format"] = source.CachedIndex{GameID: "old-format", Packages: 12, Bytes: 5000, Removable: true}
	})

	var emptyUpdated, emptyPackages, oldPackages string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(indexRow("old-format"), chromedp.ByQuery),
		textContent(indexRow("empty-dir")+` [data-testid="index-updated"]`, &emptyUpdated),
		textContent(indexRow("empty-dir")+` [data-testid="index-packages"]`, &emptyPackages),
		textContent(indexRow("old-format")+` [data-testid="index-packages"]`, &oldPackages),
	)
	assert.Contains(t, emptyUpdated, "not built yet")
	assert.Equal(t, "—", strings.TrimSpace(emptyPackages))
	assert.Contains(t, oldPackages, "unusable")
	assert.Empty(t, f.BrowserErrors())
}
