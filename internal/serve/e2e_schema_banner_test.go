package serve_test

// #648: the "a newer lmm upgraded the database" banner, in a real browser.

import (
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// TestE2E_SchemaBannerShowsOnceANewerLMMMigratedTheDatabase: a running
// lmm serve whose database a newer lmm migrated refuses every /api/v1
// request, and the page says why and what to do - a banner with no dismiss
// button, since nothing works until the server is restarted onto the newer
// binary. It appears for a page already open (its next request meets the
// refusal) and for a page loaded afterwards.
func TestE2E_SchemaBannerShowsOnceANewerLMMMigratedTheDatabase(t *testing.T) {
	sandboxE2EEnv(t)
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.CreateProfile(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	var present bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="schema-banner"]') !== null`, &present),
	)
	assert.False(t, present, "no banner while the schema is one this lmm knows")
	require.Empty(t, f.BrowserErrors())

	// A newer lmm migrates the database under the running server.
	other, err := db.New(filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	newer := other.SupportedSchemaVersion() + 1
	_, err = other.Exec("INSERT INTO schema_migrations (version) VALUES (?)", newer)
	require.NoError(t, err)
	require.NoError(t, other.Close())

	// The open page: its next read meets the refusal.
	var text, role string
	f.runInBrowser(t,
		// The page's own api module, as any action's read would go.
		chromedp.Evaluate(`import("/static/app/api.js").then((m) => m.get("/api/v1/games").catch(() => {})) && true`, nil),
		chromedp.WaitVisible(`[data-testid="schema-banner"]`, chromedp.ByQuery),
		textContent(`[data-testid="schema-banner"]`, &text),
		chromedp.AttributeValue(`[data-testid="schema-banner"]`, "role", &role, nil, chromedp.ByQuery),
	)
	assertSchemaBanner(t, text, newer)
	assert.Equal(t, "alert", role)

	// A page loaded afterwards: its own hydrate meets it.
	var dismissible bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="schema-banner"]`, chromedp.ByQuery),
		textContent(`[data-testid="schema-banner"]`, &text),
		chromedp.Evaluate(`document.querySelector('[data-testid="schema-banner"] button') !== null`, &dismissible),
	)
	assertSchemaBanner(t, text, newer)
	assert.False(t, dismissible, "a stop, not a notice: there is nothing to dismiss")

	// The browser's only complaints are the refused requests themselves.
	for _, e := range f.BrowserErrors() {
		assert.Contains(t, e, "503", "an unexpected browser error: %s", e)
		assert.False(t, strings.HasPrefix(e, "uncaught:"), "an uncaught exception: %s", e)
	}
}

// assertSchemaBanner checks the banner names both versions and the remedy.
func assertSchemaBanner(t *testing.T, text string, newer int) {
	t.Helper()
	assert.Contains(t, text, "A newer lmm upgraded the database")
	assert.Contains(t, text, "schema version "+strconv.Itoa(newer))
	assert.Contains(t, text, "this lmm reads up to "+strconv.Itoa(newer-1))
	assert.Contains(t, text, "lmm serve")
	assert.Contains(t, text, "lmm service install")
}
