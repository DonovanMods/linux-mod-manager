package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

// urlRecipient is recipient (the same game under another local id) with
// its downloads going through srv's client, which trusts srv's test
// certificate: no network.
func urlRecipient(t *testing.T, srv *httptest.Server) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{
		ID: "cyber", Name: "CP", ModPath: t.TempDir(),
		SourceIDs: map[string]string{"steamworkshop": "1091500"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// serveBytes is a TLS server answering every request with body.
func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestCollectionURLImport_ContainerRecordsTheAddressAndNeverTheHooks(t *testing.T) {
	data := exportedContainer(t)
	srv := serveBytes(t, data)
	svc, game := urlRecipient(t, srv)
	ctx := context.Background()
	link := srv.URL + "/shared/coop.lmmc?token=s3cret#readme"

	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: link})
	require.NoError(t, err)
	c := plan.Collection
	require.NotNil(t, c)
	require.True(t, c.Container, "sniffed by its first bytes")
	require.Equal(t, core.CollectionFromURL, c.Input)
	require.Equal(t, "cyber", c.GameID)
	require.False(t, c.HooksImportable, "a URL's hooks are never importable")
	require.Len(t, c.Hooks, 1, "the plan still lists them")
	require.Equal(t, srv.URL+"/shared/coop.lmmc?…", c.Origin.ImportURL, "the plan shows the address without its query")
	require.Equal(t, "curator", c.Origin.Author, "the curator's credit is kept beside it")
	require.Equal(t, srv.URL+"/shared/coop.lmmc?…", plan.Profile.Collection.Origin.ImportURL, "so does the profile preview")

	// --with-hooks is ignored for a URL, not refused: the result says so.
	result, err := svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true, WithHooks: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.HooksWithheld)

	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit, "a URL's hooks are never saved")
	require.Equal(t, srv.URL+"/shared/coop.lmmc?token=s3cret", saved.Collection.Origin.ImportURL,
		"the profile records the address to fetch again, query kept, fragment dropped")
	require.Equal(t, "curator", saved.Collection.Origin.Author)
	require.Equal(t, "2f8c7a3e-0000-4000-8000-000000000001", saved.Collection.ID)

	// A re-export never carries this machine's subscription.
	export, err := svc.ExportCollection(ctx, "cyber", "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	require.NotContains(t, string(export.Document), "import_url")
	require.NotContains(t, string(export.Document), "s3cret")
	require.Contains(t, string(export.Document), "curator")
	exported, err := svc.ExportProfile(ctx, "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, "curator", exported.Collection.Origin.Author)
	require.Empty(t, exported.Collection.Origin.ImportURL, "the profile document export leaves it out too")
	// Nor does a duplicate: it is a new collection, following no address.
	dup, err := svc.DuplicateProfile(ctx, "cyber", "coop", "coop-2")
	require.NoError(t, err)
	require.Empty(t, dup.Profile.Collection.Origin.ImportURL)
}

func TestCollectionURLImport_PlainYAMLDocument(t *testing.T) {
	svcA, gameA := exportFixture(t)
	export, err := svcA.ExportCollection(context.Background(), gameA.ID, "coop", core.CollectionExportOptions{})
	require.NoError(t, err)
	srv := serveBytes(t, export.Document)
	svc, _ := urlRecipient(t, srv)

	plan, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/coop.yaml", ProfileName: "mine"})
	require.NoError(t, err)
	require.False(t, plan.Collection.Container)
	require.Equal(t, 1, plan.Collection.FormatVersion)
	require.Equal(t, "mine", plan.Collection.ProfileName)
	require.Equal(t, srv.URL+"/coop.yaml", plan.Collection.Origin.ImportURL)
}

func TestCollectionURLImport_VersionZeroExport(t *testing.T) {
	profile := &domain.Profile{
		Name: "old", GameID: "cyber",
		Mods:          []domain.ModReference{{SourceID: "nexusmods", ModID: "1", Version: "1.0"}},
		Hooks:         domain.GameHooks{Install: domain.HookConfig{BeforeAll: "/usr/bin/false"}},
		HooksExplicit: domain.GameHooksExplicit{Install: domain.HookExplicitFlags{BeforeAll: true}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	srv := serveBytes(t, data)
	svc, game := urlRecipient(t, srv)
	ctx := context.Background()

	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/old.yaml"})
	require.NoError(t, err)
	c := plan.Collection
	require.Equal(t, 0, c.FormatVersion, "a version-0 export from a URL plans as one")
	require.Equal(t, "cyber", c.GameID)
	require.Equal(t, "game_id", c.MatchedBy, "matched by the export's own game id")
	require.False(t, c.HooksImportable)
	require.Len(t, c.Hooks, 1)

	result, err := svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true, WithHooks: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.HooksWithheld)
	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "old")
	require.NoError(t, err)
	require.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit)
	require.Equal(t, srv.URL+"/old.yaml", saved.Collection.Origin.ImportURL)

	// An export for a game this machine does not have names it.
	profile.GameID = "elsewhere"
	other, err := config.ExportProfile(profile)
	require.NoError(t, err)
	srv2 := serveBytes(t, other)
	svc2, _ := urlRecipient(t, srv2)
	_, err = svc2.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv2.URL + "/old.yaml"})
	require.True(t, core.IsCollectionGameUnresolved(err), "%v", err)
	plan, err = svc2.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv2.URL + "/old.yaml", GameID: "cyber"})
	require.NoError(t, err)
	require.Equal(t, "game", plan.Collection.MatchedBy)
	require.Contains(t, strings.Join(plan.Collection.Warnings, "\n"), `"elsewhere"`, "the plan says the export named another game")
}

func TestCollectionURLImport_RefusesARedirectToPlainHTTP(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write(exportedContainer(t))
	}))
	t.Cleanup(plain.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/coop.lmmc?sig=s3cret", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)

	_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/coop.lmmc"})
	require.Error(t, err)
	require.True(t, core.IsCollectionFetchFailed(err), "%v", err)
	require.Contains(t, err.Error(), "plain-http")
	require.NotContains(t, err.Error(), "s3cret", "the redirect's query is never shown")
	require.Zero(t, plainHits.Load(), "the http address is never requested")
}

func TestCollectionURLImport_FollowsAnHTTPSRedirect(t *testing.T) {
	data := exportedContainer(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/coop.lmmc", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)
	plan, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/moved"})
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/moved", plan.Collection.Origin.ImportURL, "the address the user gave is the one recorded")
}

func TestCollectionURLImport_RefusesAnOversizedBodyWhileStreaming(t *testing.T) {
	var written atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No Content-Length: the cap must hold as the body arrives.
		chunk := []byte(strings.Repeat("# padding\n", 6554)) // ~64 KiB
		_, _ = w.Write([]byte("lmm_collection: 1\n"))
		for range 4096 { // ~256 MiB, if nothing stopped it
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)

	_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/big.yaml"})
	require.Error(t, err)
	require.True(t, core.IsInvalidCollection(err), "%v", err)
	require.Contains(t, err.Error(), "limit")
	srv.CloseClientConnections()
	require.Less(t, written.Load(), int64(64<<20), "the reader stopped at the document cap, not at the body's end")
}

func TestCollectionURLImport_RefusesAContainerDeclaredOverTheCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100000000")
		_, _ = w.Write([]byte("PK\x03\x04"))
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)
	_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/big.lmmc"})
	require.Error(t, err)
	require.True(t, core.IsInvalidCollection(err), "%v", err)
	require.Contains(t, err.Error(), "limit")
}

func TestCollectionURLImport_RefusesANonCollectionBody(t *testing.T) {
	for name, body := range map[string]string{
		"html":           "<!doctype html>\n<html><body>Sign in</body></html>\n",
		"yaml, no name":  "hello: world\n",
		"a plain string": "just some text\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv := serveBytes(t, []byte(body))
			svc, _ := urlRecipient(t, srv)
			_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/page"})
			require.Error(t, err)
			require.True(t, core.IsInvalidCollection(err), "%v", err)
			require.Contains(t, err.Error(), "not an lmm collection or a profile export")
		})
	}
}

func TestCollectionURLImport_StripsUserinfo(t *testing.T) {
	data := exportedContainer(t)
	var gotUser, gotPass string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	svc, game := urlRecipient(t, srv)
	u, err := url.Parse(srv.URL + "/coop.lmmc")
	require.NoError(t, err)
	u.User = url.UserPassword("alice", "hunter2")

	plan, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: u.String()})
	require.NoError(t, err)
	require.Equal(t, "alice", gotUser, "the credentials the user typed are used for this fetch")
	require.Equal(t, "hunter2", gotPass)
	require.Equal(t, srv.URL+"/coop.lmmc", plan.Collection.Origin.ImportURL)
	require.Contains(t, strings.Join(plan.Collection.Warnings, "\n"), "not recorded")
	for _, w := range plan.Collection.Warnings {
		require.NotContains(t, w, "hunter2")
	}

	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "coop")
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/coop.lmmc", saved.Collection.Origin.ImportURL, "userinfo is never stored")
}

func TestCollectionURLImport_RefusesAnAddressThatIsNotHTTPS(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)
	plainURL := strings.Replace(srv.URL, "https://", "http://", 1)
	for _, link := range []string{plainURL + "/c.lmmc", "ftp://example.com/c.lmmc", "file:///etc/passwd", "https:///nohost", "not a url", ""} {
		_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: link})
		require.Error(t, err, link)
		require.True(t, core.IsCollectionURLInvalid(err), "%q: %v", link, err)
	}
	require.Zero(t, hits.Load())
}

func TestCollectionURLImport_HTTPErrorNeverShowsTheQuery(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlRecipient(t, srv)
	_, err := svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/c.lmmc?token=s3cret"})
	require.Error(t, err)
	require.True(t, core.IsCollectionFetchFailed(err))
	require.Contains(t, err.Error(), "404")
	require.NotContains(t, err.Error(), "s3cret")

	// A connection failure quotes the address too: without its query.
	srv.Close()
	_, err = svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: srv.URL + "/c.lmmc?token=s3cret"})
	require.Error(t, err)
	require.True(t, core.IsCollectionFetchFailed(err))
	require.NotContains(t, err.Error(), "s3cret")
}

func TestIsCollectionURL(t *testing.T) {
	for in, want := range map[string]bool{
		"https://example.com/c.lmmc": true,
		"HTTPS://example.com/c.lmmc": true,
		"http://example.com/c.lmmc":  true,
		"coop.lmmc":                  false,
		"./https/coop.lmmc":          false,
		"/tmp/c.yaml":                false,
	} {
		require.Equal(t, want, core.IsCollectionURL(in), in)
	}
}

func TestCollectionImport_ADocumentCannotPlantAnImportURL(t *testing.T) {
	svc, game := recipient(t)
	doc := "lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\norigin:\n  author: someone\n  import_url: https://evil.example/c.lmmc\nmods:\n  - {source_id: s, mod_id: m}\n"
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc)})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Origin.ImportURL)
	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), "cyber", "p")
	require.NoError(t, err)
	require.Equal(t, &domain.CollectionOrigin{Author: "someone"}, saved.Collection.Origin, "only lmm records where it imported from")
}
