package serve_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ModDetail_RendersProseFilesVersions is /mods/{source}/{id}'s
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 4): the page
// must render the mod's name/author/summary, its deployed files
// (ModFiles), its available versions (AvailableModVersions), the CSRF
// token, and the install form's target route Task 8 (#322) will wire.
func TestServer_ModDetail_RendersProseFilesVersions(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{
			ID: "42", SourceID: "fake", Name: "Better Boots", Version: "1.2.0",
			Author: "Author McAuthorface", Summary: "Nicer boots.",
		},
		Files: []domain.DownloadableFile{
			{ID: "f1", Name: "Main", FileName: "boots.zip", Version: "1.0.0", IsPrimary: true},
			{ID: "f2", Name: "Main", FileName: "boots.zip", Version: "1.2.0", IsPrimary: true},
		},
	})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "42", SourceID: "fake", Name: "Better Boots", Version: "1.2.0", GameID: game.ID,
	}, true, map[string][]byte{"boots.esp": []byte("data")})

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods/fake/42", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Better Boots")
	assert.Contains(t, body, "Author McAuthorface")
	assert.Contains(t, body, "Nicer boots.")
	assert.Contains(t, body, "1.0.0")
	assert.Contains(t, body, "1.2.0")
	assert.Contains(t, body, `action="/mods/fake/42/install"`)
	assert.Contains(t, body, "coming in this release")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_ModDetail_EscapesDescriptionAndChangelog is the Global
// Constraints escaping ratchet's fixture test: a mod whose Description and
// Changelog both carry a script tag must render it as inert, escaped text -
// never live markup.
func TestServer_ModDetail_EscapesDescriptionAndChangelog(t *testing.T) {
	const payload = `<script>alert(1)</script>`

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{
			ID: "42", SourceID: "fake", Name: "Evil Mod", Version: "1.0",
			Description: payload,
		},
		Changelog: payload,
	})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods/fake/42", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, payload, "raw <script> markup must never reach the response")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", "the payload must render as escaped text")
}

// TestServer_ModDetail_UnknownMod_Renders404 proves a mod ID the source
// doesn't recognize answers 404 with a normal HTML page, not a bare error
// string (WEBUI.md).
func TestServer_ModDetail_UnknownMod_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods/fake/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
}
